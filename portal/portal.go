/* SPDX-License-Identifier: MIT
 *
 * CFBTEL VPN: talks to the company's VPN page. The manager service (SYSTEM) uses this to turn a finished
 * Microsoft 365 sign-in into a tunnel, so a standard Windows user can only ever add a tunnel that the
 * CFBTEL VPN page issued — never a tunnel file of their own choosing.
 */

package portal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"

	"golang.zx2c4.com/wireguard/windows/conf"
	"golang.zx2c4.com/wireguard/windows/l18n"
)

// URL is the CFBTEL staff VPN page, e.g. https://vpn.example.com. Set at build time
// (-ldflags "-X golang.zx2c4.com/wireguard/windows/portal.URL=https://…") so the host name isn't in the
// public source. An administrator can override it with HKLM\Software\CFBTEL\VPN PortalURL (REG_SZ).
var URL = ""

// Base returns the VPN page address without a trailing slash.
func Base() (string, error) {
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `Software\CFBTEL\VPN`, registry.QUERY_VALUE|registry.WOW64_64KEY); err == nil {
		v, _, err := k.GetStringValue("PortalURL")
		k.Close()
		if err == nil && strings.HasPrefix(v, "https://") {
			return strings.TrimRight(v, "/"), nil
		}
	}
	if strings.HasPrefix(URL, "https://") {
		return strings.TrimRight(URL, "/"), nil
	}
	return "", errors.New(l18n.Sprintf("This copy of CFBTEL VPN doesn't know your company's VPN page. Contact CFBTEL support."))
}

// Answer is what the VPN page returns for a finished sign-in.
type Answer struct {
	Name                string   `json:"name"`
	Office              string   `json:"office"`
	Device              string   `json:"device"`
	Address             string   `json:"address"`
	DNS                 []string `json:"dns"`
	MTU                 int      `json:"mtu"`
	PeerPublicKey       string   `json:"peer_public_key"`
	PresharedKey        string   `json:"preshared_key"`
	Endpoint            string   `json:"endpoint"`
	AllowedIPs          []string `json:"allowed_ips"`
	PersistentKeepalive int      `json:"persistent_keepalive"`
	Error               string   `json:"error"`
}

var safeConfValue = regexp.MustCompile(`^[A-Za-z0-9+/=:.,\[\]\-_ ]*$`)

// Exchange swaps the one-time sign-in code for the tunnel settings.
func Exchange(base, code, state string) (*Answer, error) {
	if code == "" || state == "" || len(code) > 512 || len(state) > 128 {
		return nil, errors.New(l18n.Sprintf("The sign-in didn't finish. Click Sign in to try again."))
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.PostForm(base+"/vpn/app/token", url.Values{"code": {code}, "state": {state}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var t Answer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&t); err != nil {
		return nil, errors.New(l18n.Sprintf("The VPN page sent an unexpected answer (HTTP %d).", resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		if t.Error != "" {
			return nil, errors.New(t.Error)
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	for _, v := range append([]string{t.Address, t.PeerPublicKey, t.PresharedKey, t.Endpoint}, append(t.DNS, t.AllowedIPs...)...) {
		if !safeConfValue.MatchString(v) {
			return nil, errors.New(l18n.Sprintf("The VPN page sent settings this app can't use."))
		}
	}
	return &t, nil
}

// Config builds the tunnel from the page's answer and the private key made on this computer.
func Config(key *conf.Key, t *Answer, name string) (*conf.Config, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s\n", key.String(), t.Address)
	if len(t.DNS) > 0 {
		fmt.Fprintf(&b, "DNS = %s\n", strings.Join(t.DNS, ", "))
	}
	if t.MTU > 0 {
		fmt.Fprintf(&b, "MTU = %d\n", t.MTU)
	}
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = %s\nEndpoint = %s\n", t.PeerPublicKey, t.PresharedKey, strings.Join(t.AllowedIPs, ", "), t.Endpoint)
	if t.PersistentKeepalive > 0 {
		fmt.Fprintf(&b, "PersistentKeepalive = %d\n", t.PersistentKeepalive)
	}
	return conf.FromWgQuick(b.String(), name)
}

// UniqueName keeps existing tunnels: CFBTEL-Office, CFBTEL-Office-2, …
func UniqueName(want string, existing []string) string {
	if !conf.TunnelNameIsValid(want) {
		want = "CFBTEL-VPN"
	}
	taken := map[string]bool{}
	for _, n := range existing {
		taken[strings.ToLower(n)] = true
	}
	if !taken[strings.ToLower(want)] {
		return want
	}
	for i := 2; i < 100; i++ {
		suffix := fmt.Sprintf("-%d", i)
		base := want
		if len(base)+len(suffix) > 32 {
			base = base[:32-len(suffix)]
		}
		if n := base + suffix; !taken[strings.ToLower(n)] && conf.TunnelNameIsValid(n) {
			return n
		}
	}
	return want
}
