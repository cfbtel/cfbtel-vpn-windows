/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2026 CFBTEL. All Rights Reserved.
 */

// Package diag runs the CFBTEL VPN connection checks and writes the support report.
// It doesn't talk to the tunnel manager: the UI passes in what it knows about each office.
package diag

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type Status int

const (
	OK Status = iota
	Warn
	Fail
	Info
)

func (s Status) String() string {
	return [...]string{"OK", "WARNING", "PROBLEM", "INFO"}[s]
}

type Check struct {
	Group  string // "This computer" or an office name
	Name   string
	Status Status
	Detail string
}

// Office is one VPN connection on this computer. No private or preshared keys.
type Office struct {
	Name       string
	State      string // "connected", "off", …
	Connected  bool
	Address    string // this device's VPN address, e.g. 10.0.70.9/32
	Endpoint   string // host:port of the office firewall
	DNS        []string
	AllowedIPs []string
	PublicKey  string // this device's public key (its ID on the firewall)
	ServerKey  string
	Handshake  time.Duration // since the last handshake; -1 = never
	Rx, Tx     string
}

// Interface is a network adapter on this computer.
type Interface struct {
	Name  string
	Up    bool
	Addrs []netip.Prefix
}

type System struct {
	App        string
	Windows    string
	Computer   string
	User       string
	DeviceID   string
	Interfaces []Interface
}

// FirewallStatus is AdmixCentral's view of the office firewall for one device.
type FirewallStatus struct {
	Office        string `json:"office"`
	Reachable     bool   `json:"firewall_reachable"`
	TunnelFound   bool   `json:"tunnel_found"`
	TunnelEnabled bool   `json:"tunnel_enabled"`
	ListenPort    int    `json:"listen_port"`
	PeerPresent   bool   `json:"peer_present"`
	DeviceActive  bool   `json:"device_active"`
	AccessAllowed bool   `json:"access_allowed"`
	Message       string `json:"message"`
}

// Env holds what the checks need from outside, so they can be tested.
type Env struct {
	Portal   string // https://… staff VPN page (AdmixCentral)
	Client   *http.Client
	Ping     func(ctx context.Context, ip netip.Addr) (time.Duration, error) // ICMP echo
	Resolver *net.Resolver
	// URLs and servers used by the general checks.
	InternetURL string // returns 200 over HTTPS
	UDPServer   string // a public DNS server, host:port
	DNSName     string // a name that always resolves
}

func DefaultEnv(portal string, ping func(context.Context, netip.Addr) (time.Duration, error)) Env {
	return Env{
		Portal:      strings.TrimRight(portal, "/"),
		Client:      &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}},
		Ping:        ping,
		Resolver:    net.DefaultResolver,
		InternetURL: "https://www.cloudflare.com/cdn-cgi/trace",
		UDPServer:   "1.1.1.1:53",
		DNSName:     "www.microsoft.com",
	}
}

// Run does every check. progress (may be nil) gets each check as it finishes.
func Run(ctx context.Context, env Env, sys System, offices []Office, progress func(Check)) []Check {
	var mu sync.Mutex
	var out []Check
	add := func(c Check) {
		mu.Lock()
		out = append(out, c)
		mu.Unlock()
		if progress != nil {
			progress(c)
		}
	}
	const me = "This computer"

	// General internet.
	if err := httpOK(ctx, env.Client, env.InternetURL); err != nil {
		add(Check{me, "Internet (HTTPS)", Fail, "Can't open a secure web page: " + short(err) + ". The internet connection itself is down or blocked — the VPN can't work until it's back."})
	} else {
		add(Check{me, "Internet (HTTPS)", OK, "Web pages open."})
	}
	if addrs, err := env.Resolver.LookupHost(timeout(ctx, 5*time.Second), env.DNSName); err != nil || len(addrs) == 0 {
		add(Check{me, "Name lookup (DNS)", Fail, "Can't look up " + env.DNSName + ": " + short(err)})
	} else {
		add(Check{me, "Name lookup (DNS)", OK, env.DNSName + " → " + addrs[0]})
	}
	if rtt, err := udpDNS(ctx, env.UDPServer); err != nil {
		add(Check{me, "UDP traffic out", Fail, "No answer over UDP from " + env.UDPServer + ": " + short(err) + ". This network may block UDP, which the VPN needs. Try another network (e.g. a phone hotspot)."})
	} else {
		add(Check{me, "UDP traffic out", OK, fmt.Sprintf("UDP works (%d ms).", rtt.Milliseconds())})
	}
	if env.Portal != "" {
		if err := httpOK(ctx, env.Client, env.Portal+"/vpn"); err != nil {
			add(Check{me, "CFBTEL VPN service", Warn, "The CFBTEL VPN page doesn't answer: " + short(err)})
		} else {
			add(Check{me, "CFBTEL VPN service", OK, "Reachable."})
		}
	}
	if others := otherVPNs(sys.Interfaces, offices); len(others) > 0 {
		add(Check{me, "Other VPN software", Warn, "These adapters are on and may take over the traffic: " + strings.Join(others, ", ") + ". Turn other VPNs off while using CFBTEL VPN."})
	}

	if len(offices) == 0 {
		add(Check{me, "Offices", Warn, "No office is set up in CFBTEL VPN. Use “Add an office (sign in)” on the Home tab."})
	}
	for _, o := range offices {
		checkOffice(ctx, env, sys, o, add)
	}
	return out
}

func checkOffice(ctx context.Context, env Env, sys System, o Office, add func(Check)) {
	g := o.Name
	host, port, err := net.SplitHostPort(o.Endpoint)
	if err != nil {
		add(Check{g, "Office address", Fail, "The connection has no valid office address (" + o.Endpoint + ")."})
		return
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		addrs, lerr := env.Resolver.LookupHost(timeout(ctx, 5*time.Second), host)
		if lerr != nil || len(addrs) == 0 {
			add(Check{g, "Office address", Fail, "Can't look up " + host + ": " + short(lerr)})
			return
		}
		ip, _ = netip.ParseAddr(addrs[0])
		add(Check{g, "Office address", OK, host + " → " + addrs[0] + ", UDP port " + port})
	} else {
		add(Check{g, "Office address", Info, o.Endpoint + " (UDP)"})
	}
	if env.Ping != nil && ip.IsValid() {
		if rtt, err := env.Ping(timeout(ctx, 4*time.Second), ip); err != nil {
			add(Check{g, "Office firewall answers ping", Warn, "No ping answer from " + ip.String() + " (" + short(err) + "). Many firewalls don't answer ping, so this alone isn't a problem."})
		} else {
			add(Check{g, "Office firewall answers ping", OK, fmt.Sprintf("%s in %d ms.", ip, rtt.Milliseconds())})
		}
	}

	// The firewall side, as AdmixCentral sees it.
	if env.Portal != "" && o.PublicKey != "" {
		st, err := firewallStatus(ctx, env, o.PublicKey)
		switch {
		case err != nil:
			add(Check{g, "Office firewall (checked by CFBTEL)", Warn, "Couldn't ask CFBTEL about the firewall: " + short(err)})
		case !st.DeviceActive:
			add(Check{g, "This device at the office", Fail, nonEmpty(st.Message, "This device was removed from the VPN. Sign in again with “Add an office”, or ask your administrator for access.")})
		case !st.AccessAllowed:
			add(Check{g, "This device at the office", Fail, nonEmpty(st.Message, "You don't have access to this office any more. Ask your administrator.")})
		case !st.Reachable:
			add(Check{g, "Office firewall (checked by CFBTEL)", Warn, nonEmpty(st.Message, "CFBTEL can't reach the office firewall right now. The office internet or firewall may be down.")})
		case !st.TunnelFound || !st.TunnelEnabled:
			add(Check{g, "Office firewall (checked by CFBTEL)", Fail, nonEmpty(st.Message, "The VPN is not running on the office firewall.")})
		case !st.PeerPresent:
			add(Check{g, "Office firewall (checked by CFBTEL)", Fail, nonEmpty(st.Message, "The firewall doesn't know this device yet. It's added within 15 minutes; if it stays like this, contact support.")})
		default:
			detail := "The office firewall is up, the VPN is running and this device is allowed."
			if st.ListenPort > 0 && port != "" && fmt.Sprint(st.ListenPort) != port {
				add(Check{g, "Office firewall (checked by CFBTEL)", Warn, fmt.Sprintf("The firewall listens on UDP %d but this device uses %s. Sign in again to get the new settings.", st.ListenPort, port)})
				break
			}
			add(Check{g, "Office firewall (checked by CFBTEL)", OK, detail})
		}
	}

	// Network clashes: the office network can't be reached if this computer's own network uses the same addresses.
	for _, c := range conflicts(o, sys.Interfaces) {
		add(Check{g, "Address clash", Warn, c})
	}

	if !o.Connected {
		add(Check{g, "Connection", Info, "Not connected (" + o.State + ")."})
		return
	}
	switch {
	case o.Handshake < 0:
		add(Check{g, "Connection", Fail, "Connected, but the office firewall never answered. UDP port " + port + " may be blocked by this network, or the office firewall is down."})
	case o.Handshake > 3*time.Minute:
		add(Check{g, "Connection", Fail, fmt.Sprintf("No answer from the office firewall for %s. UDP port %s may be blocked, or the office internet is down.", o.Handshake.Round(time.Second), port)})
	default:
		add(Check{g, "Connection", OK, fmt.Sprintf("Office firewall answered %s ago. Received %s, sent %s.", o.Handshake.Round(time.Second), o.Rx, o.Tx)})
	}
	for _, d := range o.DNS {
		dip, err := netip.ParseAddr(d)
		if err != nil {
			continue
		}
		if env.Ping != nil {
			if rtt, err := env.Ping(timeout(ctx, 3*time.Second), dip); err != nil {
				add(Check{g, "Office network (" + d + ")", Warn, "No ping answer through the VPN: " + short(err)})
			} else {
				add(Check{g, "Office network (" + d + ")", OK, fmt.Sprintf("Answers through the VPN in %d ms.", rtt.Milliseconds())})
			}
		}
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dl net.Dialer
			return dl.DialContext(ctx, "udp", net.JoinHostPort(d, "53"))
		}}
		if addrs, err := r.LookupHost(timeout(ctx, 4*time.Second), env.DNSName); err != nil || len(addrs) == 0 {
			add(Check{g, "Office DNS (" + d + ")", Warn, "Name lookups through the office DNS fail: " + short(err)})
		} else {
			add(Check{g, "Office DNS (" + d + ")", OK, "Name lookups work."})
		}
		break // the first DNS server is enough
	}
}

func firewallStatus(ctx context.Context, env Env, publicKey string) (FirewallStatus, error) {
	var st FirewallStatus
	body, _ := json.Marshal(map[string]string{"public_key": publicKey})
	req, err := http.NewRequestWithContext(timeout(ctx, 20*time.Second), http.MethodPost, env.Portal+"/vpn/app/status", bytes.NewReader(body))
	if err != nil {
		return st, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := env.Client.Do(req)
	if err != nil {
		return st, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusNotFound {
		return FirewallStatus{Message: "CFBTEL doesn't know this device. It was probably removed — sign in again with “Add an office”."}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return st, fmt.Errorf("CFBTEL answered %s", resp.Status)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, err
	}
	return st, nil
}

func httpOK(ctx context.Context, c *http.Client, url string) error {
	req, err := http.NewRequestWithContext(timeout(ctx, 10*time.Second), http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("server error %s", resp.Status)
	}
	return nil
}

// udpDNS sends one DNS query over UDP and waits for the answer.
func udpDNS(ctx context.Context, server string) (time.Duration, error) {
	var dl net.Dialer
	conn, err := dl.DialContext(timeout(ctx, 3*time.Second), "udp", server)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	q := dnsQuery(0x4346, "cfbtel.com")
	start := time.Now()
	if _, err := conn.Write(q); err != nil {
		return 0, err
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return 0, err
	}
	if n < 12 || binary.BigEndian.Uint16(buf) != 0x4346 {
		return 0, errors.New("bad answer")
	}
	return time.Since(start), nil
}

func dnsQuery(id uint16, name string) []byte {
	b := make([]byte, 12, 64)
	binary.BigEndian.PutUint16(b[0:], id)
	b[2] = 0x01 // recursion desired
	binary.BigEndian.PutUint16(b[4:], 1)
	for _, label := range strings.Split(strings.Trim(name, "."), ".") {
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	return append(b, 0, 0, 1, 0, 1) // root, type A, class IN
}

var vpnWords = []string{"tap", "tun", "openvpn", "anyconnect", "cisco", "forti", "globalprotect", "pangp", "zerotier", "tailscale", "nordlynx", "nordvpn", "expressvpn", "proton", "wireguard", "wintun", "sonicwall", "pulse", "juniper", "checkpoint", "softether", "hamachi"}

// otherVPNs lists adapters that are up and look like another VPN (not our own offices).
func otherVPNs(ifaces []Interface, offices []Office) []string {
	ours := map[string]bool{}
	for _, o := range offices {
		ours[strings.ToLower(o.Name)] = true
	}
	var out []string
	for _, i := range ifaces {
		n := strings.ToLower(i.Name)
		if !i.Up || ours[n] || len(i.Addrs) == 0 {
			continue
		}
		for _, w := range vpnWords {
			if strings.Contains(n, w) {
				out = append(out, i.Name)
				break
			}
		}
	}
	return out
}

// conflicts finds this computer's own networks that overlap the office's VPN or routed networks.
func conflicts(o Office, ifaces []Interface) []string {
	var office []netip.Prefix
	if p, err := netip.ParsePrefix(o.Address); err == nil && p.Addr().Is4() {
		// The VPN network itself is at least the /24 around this device's address.
		if p.Bits() > 24 {
			p, _ = p.Addr().Prefix(24)
		}
		office = append(office, p.Masked())
	}
	for _, a := range o.AllowedIPs {
		if p, err := netip.ParsePrefix(a); err == nil && p.Addr().Is4() && p.Bits() > 0 {
			office = append(office, p.Masked())
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, i := range ifaces {
		if !i.Up || strings.EqualFold(i.Name, o.Name) {
			continue
		}
		for _, a := range i.Addrs {
			if !a.Addr().Is4() || a.Addr().IsLoopback() || a.Addr().IsLinkLocalUnicast() {
				continue
			}
			for _, p := range office {
				if p.Overlaps(a.Masked()) {
					msg := fmt.Sprintf("This computer's network “%s” (%s) uses the same addresses as the office (%s). Office computers in that range can't be reached from this network.", i.Name, a, p)
					if !seen[msg] {
						seen[msg] = true
						out = append(out, msg)
					}
				}
			}
		}
	}
	return out
}

func timeout(ctx context.Context, d time.Duration) context.Context {
	c, cancel := context.WithTimeout(ctx, d)
	time.AfterFunc(d, cancel) // the timeout ends it anyway; this just releases it
	return c
}

func short(err error) string {
	if err == nil {
		return "no answer"
	}
	s := err.Error()
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// Summary is one line: how many problems and warnings.
func Summary(checks []Check) (problems, warnings int) {
	for _, c := range checks {
		switch c.Status {
		case Fail:
			problems++
		case Warn:
			warnings++
		}
	}
	return
}

// Report is the text sent to CFBTEL support. It has no private keys.
func Report(sys System, offices []Office, checks []Check, log []string, at time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CFBTEL VPN diagnostic report\n")
	fmt.Fprintf(&b, "Created:   %s\n", at.Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintf(&b, "App:       %s\n", sys.App)
	fmt.Fprintf(&b, "Windows:   %s\n", sys.Windows)
	fmt.Fprintf(&b, "Computer:  %s\n", sys.Computer)
	fmt.Fprintf(&b, "User:      %s\n", sys.User)
	fmt.Fprintf(&b, "Device ID: %s\n", sys.DeviceID)
	p, w := Summary(checks)
	fmt.Fprintf(&b, "Result:    %d problem(s), %d warning(s)\n", p, w)

	b.WriteString("\n== Offices ==\n")
	if len(offices) == 0 {
		b.WriteString("(none)\n")
	}
	for _, o := range offices {
		fmt.Fprintf(&b, "- %s: %s\n", o.Name, o.State)
		fmt.Fprintf(&b, "    VPN address: %s   Office: %s   DNS: %s\n", o.Address, o.Endpoint, strings.Join(o.DNS, ", "))
		fmt.Fprintf(&b, "    Routed: %s\n", strings.Join(o.AllowedIPs, ", "))
		fmt.Fprintf(&b, "    Device public key: %s\n", o.PublicKey)
		fmt.Fprintf(&b, "    Office public key: %s\n", o.ServerKey)
		if o.Connected {
			hs := "never"
			if o.Handshake >= 0 {
				hs = o.Handshake.Round(time.Second).String() + " ago"
			}
			fmt.Fprintf(&b, "    Last handshake: %s   Received: %s   Sent: %s\n", hs, o.Rx, o.Tx)
		}
	}

	b.WriteString("\n== Checks ==\n")
	group := ""
	for _, c := range checks {
		if c.Group != group {
			group = c.Group
			fmt.Fprintf(&b, "[%s]\n", group)
		}
		fmt.Fprintf(&b, "  %-8s %s: %s\n", c.Status, c.Name, c.Detail)
	}

	b.WriteString("\n== Network adapters ==\n")
	for _, i := range sys.Interfaces {
		state := "down"
		if i.Up {
			state = "up"
		}
		addrs := make([]string, 0, len(i.Addrs))
		for _, a := range i.Addrs {
			addrs = append(addrs, a.String())
		}
		fmt.Fprintf(&b, "- %s (%s) %s\n", i.Name, state, strings.Join(addrs, ", "))
	}

	b.WriteString("\n== Log (latest) ==\n")
	for _, l := range log {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}
