package diag

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func TestConflicts(t *testing.T) {
	o := Office{Name: "CFBTEL-Office", Address: "10.0.70.9/32", AllowedIPs: []string{"0.0.0.0/0", "192.168.50.0/24"}}
	ifaces := []Interface{
		{Name: "Wi-Fi", Up: true, Addrs: []netip.Prefix{pfx("10.0.70.23/24")}},
		{Name: "Ethernet", Up: true, Addrs: []netip.Prefix{pfx("192.168.50.10/24"), pfx("fe80::1/64")}},
		{Name: "Hotspot", Up: true, Addrs: []netip.Prefix{pfx("172.20.10.2/28")}},
		{Name: "CFBTEL-Office", Up: true, Addrs: []netip.Prefix{pfx("10.0.70.9/32")}},
		{Name: "Old", Up: false, Addrs: []netip.Prefix{pfx("10.0.70.50/24")}},
	}
	got := conflicts(o, ifaces)
	if len(got) != 2 || !strings.Contains(got[0], "Wi-Fi") || !strings.Contains(got[1], "Ethernet") {
		t.Fatalf("conflicts: %q", got)
	}
}

func TestOtherVPNs(t *testing.T) {
	ifaces := []Interface{
		{Name: "OpenVPN TAP-Windows6", Up: true, Addrs: []netip.Prefix{pfx("10.8.0.2/24")}},
		{Name: "CFBTEL-Office", Up: true, Addrs: []netip.Prefix{pfx("10.0.70.9/32")}},
		{Name: "Tailscale", Up: false, Addrs: []netip.Prefix{pfx("100.64.0.1/32")}},
		{Name: "Wi-Fi", Up: true, Addrs: []netip.Prefix{pfx("192.168.1.5/24")}},
	}
	got := otherVPNs(ifaces, []Office{{Name: "CFBTEL-Office"}})
	if len(got) != 1 || got[0] != "OpenVPN TAP-Windows6" {
		t.Fatalf("other VPNs: %q", got)
	}
}

func TestDNSQuery(t *testing.T) {
	q := dnsQuery(7, "cfbtel.com")
	want := []byte{0, 7, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 6, 'c', 'f', 'b', 't', 'e', 'l', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
	if string(q) != string(want) {
		t.Fatalf("%v", q)
	}
}

// A fake DNS server over UDP that echoes the query id.
func udpServer(t *testing.T) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(buf[:n], addr)
		}
	}()
	return pc.LocalAddr().String()
}

func TestRunReportsFirewallSideAndHandshake(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("/vpn", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("/vpn/app/status", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["public_key"] == "gone" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(FirewallStatus{Office: "HQ", Reachable: true, TunnelFound: true, TunnelEnabled: true, ListenPort: 51820, PeerPresent: true, DeviceActive: true, AccessAllowed: true})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	env := DefaultEnv(srv.URL, func(ctx context.Context, ip netip.Addr) (time.Duration, error) {
		if ip.String() == "10.0.70.1" {
			return 0, errors.New("timed out")
		}
		return 12 * time.Millisecond, nil
	})
	env.InternetURL = srv.URL + "/ok"
	env.UDPServer = udpServer(t)
	env.DNSName = "localhost"
	offices := []Office{
		{Name: "HQ", State: "connected", Connected: true, Address: "10.0.70.9/32", Endpoint: "127.0.0.1:51820", PublicKey: "abc", Handshake: 10 * time.Minute, Rx: "1 MiB", Tx: "2 MiB"},
		{Name: "Branch", State: "off", Address: "10.0.71.9/32", Endpoint: "127.0.0.1:51821", PublicKey: "gone"},
	}
	var got []Check
	checks := Run(context.Background(), env, System{}, offices, func(c Check) { got = append(got, c) })
	if len(got) != len(checks) {
		t.Fatalf("progress %d vs %d", len(got), len(checks))
	}
	find := func(group, name string) Check {
		for _, c := range checks {
			if c.Group == group && strings.HasPrefix(c.Name, name) {
				return c
			}
		}
		t.Fatalf("no check %s / %s in %+v", group, name, checks)
		return Check{}
	}
	if c := find("This computer", "Internet"); c.Status != OK {
		t.Errorf("internet %+v", c)
	}
	if c := find("This computer", "UDP"); c.Status != OK {
		t.Errorf("udp %+v", c)
	}
	if c := find("HQ", "Office firewall (checked"); c.Status != OK {
		t.Errorf("firewall %+v", c)
	}
	if c := find("HQ", "Connection"); c.Status != Fail || !strings.Contains(c.Detail, "UDP port 51820") {
		t.Errorf("handshake %+v", c)
	}
	if c := find("Branch", "This device"); c.Status != Fail || !strings.Contains(c.Detail, "removed") {
		t.Errorf("removed device %+v", c)
	}

	report := Report(System{App: "1.0.6", Computer: "PC1", DeviceID: "dev-1"}, offices, checks, []string{"log line"}, time.Now())
	for _, want := range []string{"Device ID: dev-1", "Device public key: abc", "[HQ]", "PROBLEM", "log line"} {
		if !strings.Contains(report, want) {
			t.Errorf("report misses %q", want)
		}
	}
}

func TestUDPBlocked(t *testing.T) {
	// Nothing listens on this port: the read times out or is refused.
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	addr := pc.LocalAddr().String()
	pc.Close()
	if _, err := udpDNS(context.Background(), addr); err == nil {
		t.Fatal("expected an error")
	}
}
