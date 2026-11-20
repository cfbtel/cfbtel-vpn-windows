/* SPDX-License-Identifier: MIT
 *
 * CFBTEL VPN: sign in with the CFBTEL staff VPN page and get a tunnel without importing files.
 */

package ui

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"

	"golang.zx2c4.com/wireguard/windows/l18n"
	"golang.zx2c4.com/wireguard/windows/manager"
)

const signInTimeout = 10 * time.Minute

var signInRunning sync.Mutex

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
	// The manager service makes the key and, once the sign-in is done, fetches the settings from the VPN
	// page and saves the tunnel itself. That's what lets standard Windows users sign in safely.
	begin, err := manager.IPCClientSignInBegin()
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

	start := begin.Portal + "/vpn/app?" + url.Values{
		"state": {state},
		"port":  {fmt.Sprint(port)},
		"pub":   {begin.PublicKey},
		"name":  {computerName()},
	}.Encode()
	win.ShellExecute(0, nil, windows.StringToUTF16Ptr(start), nil, nil, win.SW_SHOWNORMAL)

	var code string
	select {
	case code = <-codes:
	case <-time.After(signInTimeout):
		return "", errors.New(l18n.Sprintf("The sign-in wasn't finished in time. Click Sign in to try again."))
	}

	tunnel, err := manager.IPCClientSignInFinish(begin.ID, code, state)
	if err != nil {
		return "", err
	}
	if err := tunnel.Start(); err != nil {
		return tunnel.Name, errors.New(l18n.Sprintf("The tunnel %s was added but couldn't be turned on: %v", tunnel.Name, err))
	}
	return tunnel.Name, nil
}
