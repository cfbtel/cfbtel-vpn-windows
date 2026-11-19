/* SPDX-License-Identifier: MIT
 *
 * CFBTEL VPN: sign in with the CFBTEL staff VPN page and get a tunnel without importing files.
 */

package ui

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"golang.zx2c4.com/wireguard/windows/conf"
	"golang.zx2c4.com/wireguard/windows/l18n"
	"golang.zx2c4.com/wireguard/windows/manager"
)

// portalURL is the CFBTEL staff VPN page, e.g. https://vpn.example.com. Set at build time
// (-ldflags "-X golang.zx2c4.com/wireguard/windows/ui.portalURL=https://…") so the host name isn't in the
// source; an administrator can override it with HKLM\Software\CFBTEL\VPN "PortalURL".
var portalURL = ""

const signInTimeout = 10 * time.Minute

var signInRunning sync.Mutex

func portal() (string, error) {
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `Software\CFBTEL\VPN`, registry.QUERY_VALUE); err == nil {
		v, _, err := k.GetStringValue("PortalURL")
		k.Close()
		if err == nil && strings.HasPrefix(v, "https://") {
			return strings.TrimRight(v, "/"), nil
		}
	}
	if strings.HasPrefix(portalURL, "https://") {
		return strings.TrimRight(portalURL, "/"), nil
	}
	return "", errors.New(l18n.Sprintf("This copy of CFBTEL VPN doesn't know your company's VPN page. Contact CFBTEL support."))
}

type signedInTunnel struct {
	Name               string   `json:"name"`
	Office             string   `json:"office"`
	Device             string   `json:"device"`
	Address            string   `json:"address"`
	DNS                []string `json:"dns"`
	MTU                int      `json:"mtu"`
	PeerPublicKey      string   `json:"peer_public_key"`
	PresharedKey       string   `json:"preshared_key"`
	Endpoint           string   `json:"endpoint"`
	AllowedIPs         []string `json:"allowed_ips"`
	PersistentKeepalive int     `json:"persistent_keepalive"`
	Error              string   `json:"error"`
}

var safeConfValue = regexp.MustCompile(`^[A-Za-z0-9+/=:.,\[\]\-_ ]*$`)

func randomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func computerName() string {
	name, err := os.Hostname()
	if err != nil {
		return "Windows PC"
	}
	name = regexp.MustCompile(`[^A-Za-z0-9 ._-]+`).ReplaceAllString(name, "-")
	if len(name) > 32 {
		name = name[:32]
	}
	if name == "" {
		return "Windows PC"
	}
	return name
}

// onSignIn runs the browser sign-in in the background and adds (and starts) the tunnel it returns.
func onSignIn(owner walk.Form) {
	if !signInRunning.TryLock() {
		showWarningCustom(owner, l18n.Sprintf("Sign in"), l18n.Sprintf("A sign-in is already open in your browser. Finish it there, or wait a few minutes and try again."))
		return
	}
	go func() {
		defer signInRunning.Unlock()
		name, err := signIn()
		owner.Synchronize(func() {
			if err != nil {
				showErrorCustom(owner, l18n.Sprintf("Sign in"), err.Error())
				return
			}
			walk.MsgBox(owner, l18n.Sprintf("CFBTEL VPN"), l18n.Sprintf("You're connected. The tunnel %s was added and turned on.", name), walk.MsgBoxIconInformation)
		})
	}()
}

func signIn() (string, error) {
	base, err := portal()
	if err != nil {
		return "", err
	}
	key, err := conf.NewPrivateKey()
	if err != nil {
		return "", err
	}
	state := randomToken(24)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	codes := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state || q.Get("code") == "" {
			http.Error(w, "This sign-in doesn't belong to this CFBTEL VPN window.", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>CFBTEL VPN</title><body style="font-family:Segoe UI,sans-serif;text-align:center;margin-top:15vh"><h1>%s</h1><p>%s</p></body>`,
			html.EscapeString(l18n.Sprintf("Done")), html.EscapeString(l18n.Sprintf("You can close this window and go back to CFBTEL VPN.")))
		select {
		case codes <- q.Get("code"):
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Shutdown(context.Background())

	start := base + "/vpn/app?" + url.Values{
		"state": {state},
		"port":  {fmt.Sprint(port)},
		"pub":   {key.Public().String()},
		"name":  {computerName()},
	}.Encode()
	win.ShellExecute(0, nil, windows.StringToUTF16Ptr(start), nil, nil, win.SW_SHOWNORMAL)

	var code string
	select {
	case code = <-codes:
	case <-time.After(signInTimeout):
		return "", errors.New(l18n.Sprintf("The sign-in wasn't finished in time. Click Sign in to try again."))
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.PostForm(base+"/vpn/app/token", url.Values{"code": {code}, "state": {state}})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var t signedInTunnel
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&t); err != nil {
		return "", errors.New(l18n.Sprintf("The VPN page sent an unexpected answer (HTTP %d).", resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		if t.Error != "" {
			return "", errors.New(t.Error)
		}
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	for _, v := range append([]string{t.Address, t.PeerPublicKey, t.PresharedKey, t.Endpoint}, append(t.DNS, t.AllowedIPs...)...) {
		if !safeConfValue.MatchString(v) {
			return "", errors.New(l18n.Sprintf("The VPN page sent settings this app can't use."))
		}
	}
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

	name := uniqueTunnelName(t.Name)
	config, err := conf.FromWgQuick(b.String(), name)
	if err != nil {
		return "", err
	}
	tunnel, err := manager.IPCClientNewTunnel(config)
	if err != nil {
		return "", err
	}
	if err := tunnel.Start(); err != nil {
		return name, errors.New(l18n.Sprintf("The tunnel %s was added but couldn't be turned on: %v", name, err))
	}
	return name, nil
}

// uniqueTunnelName keeps existing tunnels: CFBTEL-Office, CFBTEL-Office-2, …
func uniqueTunnelName(want string) string {
	if !conf.TunnelNameIsValid(want) {
		want = "CFBTEL-VPN"
	}
	taken := map[string]bool{}
	if existing, err := manager.IPCClientTunnels(); err == nil {
		for _, t := range existing {
			taken[strings.ToLower(t.Name)] = true
		}
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
