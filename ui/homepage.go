/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2026 CFBTEL. All Rights Reserved.
 */

package ui

import (
	"bytes"
	_ "embed"
	"fmt"
	"image/png"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"

	"golang.zx2c4.com/wireguard/windows/conf"
	"golang.zx2c4.com/wireguard/windows/l18n"
	"golang.zx2c4.com/wireguard/windows/manager"
	"golang.zx2c4.com/wireguard/windows/updater"
	"golang.zx2c4.com/wireguard/windows/version"
)

// CFBTEL brand colours (logo navy and cyan) and the dark Home theme built on them.
var (
	brandNavy      = walk.RGB(0x0E, 0x22, 0x38)
	brandNavyLight = walk.RGB(0x17, 0x32, 0x4F)
	brandCyan      = walk.RGB(0x33, 0xC5, 0xF3)
	brandCyanText  = walk.RGB(0x9F, 0xD8, 0xF0)
	brandMuted     = walk.RGB(0x9F, 0xB3, 0xC8)
	brandWhite     = walk.RGB(0xFF, 0xFF, 0xFF)
	brandPage      = walk.RGB(0xF4, 0xF7, 0xFA)
	brandBorder    = walk.RGB(0xD5, 0xDE, 0xE7)
	brandInk       = walk.RGB(0x0E, 0x22, 0x38)
	brandInkMuted  = walk.RGB(0x5B, 0x6B, 0x7C)
	brandLink      = walk.RGB(0x1A, 0x8D, 0xBF)
	brandGreen     = walk.RGB(0x1D, 0x9E, 0x75)
	brandAmber     = walk.RGB(0xBA, 0x75, 0x17)
	brandGrey      = walk.RGB(0xB4, 0xC2, 0xCF)

	darkBg        = walk.RGB(0x0E, 0x22, 0x38) // page
	darkCard      = walk.RGB(0x13, 0x2C, 0x47) // the connection card and office rows
	darkCardHover = walk.RGB(0x18, 0x36, 0x56)
	darkTile      = walk.RGB(0x1E, 0x3A, 0x5C) // icon tile
	darkLine      = walk.RGB(0x24, 0x40, 0x5F) // card border when off, separators
	darkGrid      = walk.RGB(0x2A, 0x45, 0x66) // graph grid
	darkText      = walk.RGB(0xFF, 0xFF, 0xFF)
	darkText2     = walk.RGB(0xA9, 0xC1, 0xDA) // secondary, tinted from the navy (7.7:1 on the card)
	darkText3     = walk.RGB(0x8F, 0xA9, 0xC4) // labels (5.8:1 on the card)
	darkLinkText  = walk.RGB(0x7F, 0xD8, 0xF7)
	darkDown      = walk.RGB(0x2F, 0xD3, 0xA0) // download / connected green
	darkUp        = walk.RGB(0xF5, 0xA5, 0x24) // upload orange
	darkWait      = walk.RGB(0xF5, 0xC5, 0x4A)
	darkSlate     = walk.RGB(0x3B, 0x4D, 0x6B) // Disconnect button
	darkSlateHi   = walk.RGB(0x47, 0x5B, 0x7C)
	darkCyanHi    = walk.RGB(0x5C, 0xD3, 0xF7)
)

// The official CFBTEL wordmark (cyan), rendered from the company's SVG.
//
//go:embed icon/wordmark.png
var wordmarkPNG []byte

var (
	wordmarkOnce sync.Once
	wordmarkBmp  *walk.Bitmap
)

func wordmark() *walk.Bitmap {
	wordmarkOnce.Do(func() {
		if img, err := png.Decode(bytes.NewReader(wordmarkPNG)); err == nil {
			wordmarkBmp, _ = walk.NewBitmapFromImageForDPI(img, 96)
		}
	})
	return wordmarkBmp
}

// HomePage is the CFBTEL screen: status and connected time, the connection card (office, one big button, live
// traffic, details) and the person's other offices. The WireGuard tunnel editor stays on the "Advanced" tab.
type HomePage struct {
	*walk.TabPage

	scroll *walk.ScrollView
	view   *walk.CustomWidget
	footer *walk.CustomWidget

	mu       sync.Mutex
	tunnels  []string
	states   map[string]manager.TunnelState
	address  map[string]string
	endpoint map[string]string
	dns      map[string]string
	selected string

	// Live traffic for the connected office.
	statsFor       string
	since          time.Time
	rx, tx         string // totals
	shake          string
	prevRx, prevTx conf.Bytes
	prevAt         time.Time
	rxRate, txRate float64      // bytes per second, now
	history        [][2]float64 // last minute of [rx, tx] bytes per second

	details  bool
	hover    string
	targets  map[string]walk.Rectangle // clickable areas of the view, by name
	ftargets map[string]walk.Rectangle // clickable areas of the footer
	height   int

	update    bool
	updateMsg string
	updating  bool

	fonts map[string]*walk.Font

	// Set by the window.
	OpenDiagnostics func()
	OnInstalling    func() // the installer is about to replace the app: hide the window

	tunnelChangedCB  *manager.TunnelChangeCallback
	tunnelsChangedCB *manager.TunnelsChangeCallback
	updateProgressCB *manager.UpdateProgressCallback
	stop             chan struct{}
}

func NewHomePage() (*HomePage, error) {
	var err error
	var disposables walk.Disposables
	defer disposables.Treat()

	hp := &HomePage{states: map[string]manager.TunnelState{}, address: map[string]string{}, endpoint: map[string]string{}, dns: map[string]string{}, stop: make(chan struct{}), targets: map[string]walk.Rectangle{}, ftargets: map[string]walk.Rectangle{}}
	if hp.TabPage, err = walk.NewTabPage(); err != nil {
		return nil, err
	}
	disposables.Add(hp)
	hp.SetTitle(l18n.Sprintf("Home"))
	layout := walk.NewVBoxLayout()
	layout.SetMargins(walk.Margins{})
	layout.SetSpacing(0)
	hp.SetLayout(layout)
	bg, _ := walk.NewSolidColorBrush(darkBg)
	hp.SetBackground(bg)

	hp.fonts = map[string]*walk.Font{}
	for name, f := range map[string]struct {
		size  int
		style walk.FontStyle
	}{
		"status": {15, walk.FontBold}, "timer": {12, 0}, "name": {12, walk.FontBold}, "sub": {9, 0},
		"button": {12, walk.FontBold}, "small": {8, 0}, "rate": {10, walk.FontBold}, "label": {9, 0},
		"value": {9, walk.FontBold}, "row": {10, 0}, "pill": {9, walk.FontBold}, "footer": {8, 0}, "help": {10, walk.FontBold},
	} {
		if font, err := walk.NewFont("Segoe UI", f.size, f.style); err == nil {
			hp.fonts[name] = font
		}
	}

	// The view scrolls only if many offices make it taller than the window; the footer stays at the bottom.
	if hp.scroll, err = walk.NewScrollView(hp); err != nil {
		return nil, err
	}
	// Both directions on: walk only lets a ScrollView grow sideways when horizontal scrolling is on. With
	// vertical only it shrinks to its narrowest size and squeezes everything into a thin column.
	hp.scroll.SetScrollbars(true, true)
	inner := walk.NewVBoxLayout()
	inner.SetMargins(walk.Margins{})
	inner.SetSpacing(0)
	hp.scroll.SetLayout(inner)
	hp.scroll.SetBackground(bg)

	if hp.view, err = walk.NewCustomWidgetPixels(hp.scroll, 0, hp.paint); err != nil {
		return nil, err
	}
	hp.view.SetPaintMode(walk.PaintBuffered)
	hp.view.SetInvalidatesOnResize(true)
	hp.view.SetMinMaxSize(walk.Size{Height: 400}, walk.Size{Height: 400})
	hp.view.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			hp.onClick(hp.targetAt(hp.targets, x, y))
		}
	})
	hp.view.MouseMove().Attach(func(x, y int, button walk.MouseButton) {
		hp.setHover(hp.view, hp.targetAt(hp.targets, x, y))
	})

	if hp.footer, err = walk.NewCustomWidgetPixels(hp, 0, hp.paintFooter); err != nil {
		return nil, err
	}
	hp.footer.SetPaintMode(walk.PaintBuffered)
	hp.footer.SetInvalidatesOnResize(true)
	hp.footer.SetMinMaxSize(walk.Size{Height: 34}, walk.Size{Height: 34})
	hp.footer.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			hp.onClick(hp.targetAt(hp.ftargets, x, y))
		}
	})
	hp.footer.MouseMove().Attach(func(x, y int, button walk.MouseButton) {
		hp.setHover(hp.footer, hp.targetAt(hp.ftargets, x, y))
	})

	hp.selected = walkSetting("HomeOffice")
	hp.tunnelChangedCB = manager.IPCClientRegisterTunnelChange(func(tunnel *manager.Tunnel, state, globalState manager.TunnelState, err error) {
		if tunnel == nil {
			return
		}
		hp.mu.Lock()
		hp.states[tunnel.Name] = state
		hp.mu.Unlock()
		hp.Synchronize(hp.repaint)
	})
	hp.tunnelsChangedCB = manager.IPCClientRegisterTunnelsChange(func() { go hp.reload() })
	hp.updateProgressCB = manager.IPCClientRegisterUpdateProgress(func(dp updater.DownloadProgress) {
		hp.Synchronize(func() { hp.onUpdateProgress(dp) })
	})
	go hp.reload()
	go hp.pollStats()

	disposables.Spare()
	return hp, nil
}

func (hp *HomePage) Dispose() {
	if hp.stop != nil {
		close(hp.stop)
		hp.stop = nil
	}
	if hp.tunnelChangedCB != nil {
		hp.tunnelChangedCB.Unregister()
		hp.tunnelChangedCB = nil
	}
	if hp.tunnelsChangedCB != nil {
		hp.tunnelsChangedCB.Unregister()
		hp.tunnelsChangedCB = nil
	}
	if hp.updateProgressCB != nil {
		hp.updateProgressCB.Unregister()
		hp.updateProgressCB = nil
	}
	hp.TabPage.Dispose()
}

func hit(r walk.Rectangle, x, y int) bool {
	return r.Width > 0 && x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height
}

func (hp *HomePage) targetAt(targets map[string]walk.Rectangle, x, y int) string {
	for name, r := range targets {
		if hit(r, x, y) {
			return name
		}
	}
	return ""
}

func (hp *HomePage) setHover(w *walk.CustomWidget, name string) {
	if name != "" {
		w.SetCursor(walk.CursorHand())
	} else {
		w.SetCursor(walk.CursorArrow())
	}
	if name != hp.hover {
		hp.hover = name
		hp.view.Invalidate()
		hp.footer.Invalidate()
	}
}

// UI settings (chosen office, support name/email, offices to reconnect after an update) live in
// HKCU\Software\CFBTEL\VPN\UI. They are written at once, so they survive the installer closing the app.
const uiSettingsKey = `Software\CFBTEL\VPN\UI`

func walkSetting(key string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, uiSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue(key)
	return v
}

func saveWalkSetting(key, value string) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uiSettingsKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if value == "" {
		k.DeleteValue(key)
		return
	}
	k.SetStringValue(key, value)
}

// reload reads the offices (tunnels), their state, VPN address, office address and DNS from the manager.
func (hp *HomePage) reload() {
	tunnels, err := manager.IPCClientTunnels()
	if err != nil {
		return
	}
	names := make([]string, 0, len(tunnels))
	states := map[string]manager.TunnelState{}
	address, endpoint, dns := map[string]string{}, map[string]string{}, map[string]string{}
	for i := range tunnels {
		t := tunnels[i]
		names = append(names, t.Name)
		if st, err := t.State(); err == nil {
			states[t.Name] = st
		}
		if c, err := t.StoredConfig(); err == nil {
			if len(c.Interface.Addresses) > 0 {
				address[t.Name] = c.Interface.Addresses[0].Addr().String()
			}
			if len(c.Peers) > 0 {
				endpoint[t.Name] = c.Peers[0].Endpoint.String()
			}
			ds := make([]string, 0, len(c.Interface.DNS))
			for _, d := range c.Interface.DNS {
				ds = append(ds, d.String())
			}
			dns[t.Name] = strings.Join(ds, ", ")
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	hp.mu.Lock()
	hp.tunnels, hp.states, hp.address, hp.endpoint, hp.dns = names, states, address, endpoint, dns
	if !contains(names, hp.selected) && len(names) > 0 {
		hp.selected = names[0]
	}
	hp.mu.Unlock()
	hp.Synchronize(hp.repaint)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// tunnelStartTime is when the office's tunnel service started (for the connected timer), or zero.
func tunnelStartTime(name string) time.Time {
	svcName, err := conf.ServiceNameOfTunnel(name)
	if err != nil {
		return time.Time{}
	}
	m, err := mgr.ConnectRemote("")
	if err != nil {
		return time.Time{}
	}
	defer m.Disconnect()
	h, err := windows.OpenService(m.Handle, windows.StringToUTF16Ptr(svcName), windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return time.Time{}
	}
	defer windows.CloseServiceHandle(h)
	var st windows.SERVICE_STATUS_PROCESS
	var needed uint32
	if windows.QueryServiceStatusEx(h, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&st)), uint32(unsafe.Sizeof(st)), &needed) != nil || st.ProcessId == 0 {
		return time.Time{}
	}
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, st.ProcessId)
	if err != nil {
		return time.Time{}
	}
	defer windows.CloseHandle(p)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(p, &created, &exited, &kernel, &user) != nil {
		return time.Time{}
	}
	return time.Unix(0, created.Nanoseconds())
}

// pollStats refreshes traffic, speed and handshake for the connected office every second.
func (hp *HomePage) pollStats() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	stop := hp.stop
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		name := hp.connected()
		var r, sent conf.Bytes
		var last conf.HandshakeTime
		ok := false
		if name != "" {
			t := manager.Tunnel{Name: name}
			if c, err := t.RuntimeConfig(); err == nil {
				ok = true
				for _, p := range c.Peers {
					r += p.RxBytes
					sent += p.TxBytes
					if p.LastHandshakeTime > last {
						last = p.LastHandshakeTime
					}
				}
			}
		}
		now := time.Now()
		var startedAt time.Time
		hp.mu.Lock()
		fresh := ok && hp.states[name] == manager.TunnelStarted && name != hp.statsFor
		hp.mu.Unlock()
		if fresh {
			startedAt = tunnelStartTime(name) // outside the lock: it asks Windows
		}
		hp.mu.Lock()
		if !ok || hp.states[name] != manager.TunnelStarted {
			hp.statsFor, hp.history, hp.rxRate, hp.txRate, hp.since = "", nil, 0, 0, time.Time{}
		} else if fresh {
			hp.statsFor, hp.history, hp.rxRate, hp.txRate = name, nil, 0, 0
			hp.since = startedAt
			if hp.since.IsZero() || hp.since.After(now) {
				hp.since = now
			}
		} else if dt := now.Sub(hp.prevAt).Seconds(); dt > 0 && r >= hp.prevRx && sent >= hp.prevTx {
			hp.rxRate = float64(r-hp.prevRx) / dt
			hp.txRate = float64(sent-hp.prevTx) / dt
			hp.history = append(hp.history, [2]float64{hp.rxRate, hp.txRate})
			if len(hp.history) > 60 {
				hp.history = hp.history[len(hp.history)-60:]
			}
		}
		hp.prevRx, hp.prevTx, hp.prevAt = r, sent, now
		hp.rx, hp.tx, hp.shake = "", "", ""
		if ok {
			hp.rx, hp.tx = r.String(), sent.String()
			hp.shake = shortHandshake(last)
		}
		hp.mu.Unlock()
		hp.Synchronize(func() { hp.view.Invalidate() })
	}
}

// shortHandshake is "~12 sec ago", "~2 min ago" or "Waiting for the office".
func shortHandshake(t conf.HandshakeTime) string {
	if t.IsEmpty() {
		return l18n.Sprintf("Waiting for the office")
	}
	d := time.Since(time.Unix(0, 0).Add(time.Duration(t)))
	switch {
	case d < time.Minute:
		return l18n.Sprintf("~%d sec ago", max(1, int(d.Seconds())))
	case d < time.Hour:
		return l18n.Sprintf("~%d min ago", int(d.Minutes()))
	default:
		return l18n.Sprintf("~%d h ago", int(d.Hours()))
	}
}

func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	h, m, sec := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, sec)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}

// clock is a connected time like "07:48:39".
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s/60%60, s%60)
}

// byteRate is bytes per second as "0 KB/s", "36.5 KB/s", "369 KB/s", "1.24 MB/s".
func byteRate(b float64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.2f MB/s", b/(1<<20))
	case b >= 100<<10:
		return fmt.Sprintf("%.0f KB/s", b/(1<<10))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB/s", b/(1<<10))
	case b > 0:
		return fmt.Sprintf("%.2f KB/s", b/(1<<10))
	default:
		return "0 KB/s"
	}
}

// connected is the office that is on (or turning on/off), or "".
func (hp *HomePage) connected() string {
	hp.mu.Lock()
	defer hp.mu.Unlock()
	for _, n := range hp.tunnels {
		if st := hp.states[n]; st == manager.TunnelStarted || st == manager.TunnelStarting || st == manager.TunnelStopping {
			return n
		}
	}
	return ""
}

// repaint fits the view's height to its content and redraws it.
func (hp *HomePage) repaint() {
	if h := hp.contentHeight(); h != hp.height {
		hp.height = h
		hp.view.SetMinMaxSize(walk.Size{Height: h}, walk.Size{Height: h})
		hp.scroll.RequestLayout()
	}
	hp.view.Invalidate()
	hp.footer.Invalidate()
}

func (hp *HomePage) UpdateFound() {
	hp.update = true
	hp.footer.Invalidate()
}

func (hp *HomePage) setUpdateMsg(msg string) {
	hp.updateMsg = msg
	hp.footer.Invalidate()
}

func (hp *HomePage) onClick(target string) {
	switch {
	case target == "button":
		hp.onButton()
	case target == "details":
		hp.details = !hp.details
		hp.repaint()
	case target == "help", target == "diag":
		if hp.OpenDiagnostics != nil {
			hp.OpenDiagnostics()
		}
	case target == "add":
		onSignIn(hp.Form())
	case target == "update":
		hp.CheckForUpdates()
	case target == "mail":
		win.ShellExecute(hp.Form().Handle(), nil, windows.StringToUTF16Ptr("mailto:help@cfbtel.com?subject=CFBTEL%20VPN%20"+version.Number), nil, nil, win.SW_SHOWNORMAL)
	case target == "about":
		onAbout(hp.Form())
	case strings.HasPrefix(target, "office:"), strings.HasPrefix(target, "connect:"):
		name := target[strings.Index(target, ":")+1:]
		hp.mu.Lock()
		hp.selected = name
		state := hp.states[name]
		hp.mu.Unlock()
		saveWalkSetting("HomeOffice", name)
		if strings.HasPrefix(target, "connect:") && (state == manager.TunnelStopped || state == manager.TunnelUnknown) {
			hp.connect(name)
			return
		}
		hp.repaint()
	}
}

func (hp *HomePage) onButton() {
	name := hp.connected()
	hp.mu.Lock()
	state := hp.states[name]
	selected := hp.selected
	empty := len(hp.tunnels) == 0
	hp.mu.Unlock()
	switch {
	case empty:
		onSignIn(hp.Form())
	case name != "" && (state == manager.TunnelStarting || state == manager.TunnelStopping):
		return
	case name != "":
		hp.setState(name, manager.TunnelStopping)
		go func() {
			t := manager.Tunnel{Name: name}
			if err := t.Stop(); err != nil {
				hp.Synchronize(func() { showErrorCustom(hp.Form(), l18n.Sprintf("Disconnect"), err.Error()) })
			}
		}()
	case selected != "":
		hp.connect(selected)
	}
}

func (hp *HomePage) connect(name string) {
	hp.setState(name, manager.TunnelStarting)
	go func() {
		t := manager.Tunnel{Name: name}
		if err := t.Start(); err != nil {
			hp.Synchronize(func() { showErrorCustom(hp.Form(), l18n.Sprintf("Connect"), err.Error()) })
			go hp.reload()
		}
	}()
}

func (hp *HomePage) setState(name string, st manager.TunnelState) {
	hp.mu.Lock()
	hp.states[name] = st
	hp.mu.Unlock()
	hp.repaint()
}

// displayName turns a tunnel name like "CFBTEL-CFBTEL-Office-Costa-Mesa" into "CFBTEL Office Costa Mesa".
func displayName(name string) string {
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(name))
	out := make([]string, 0, len(words))
	for _, w := range words {
		if len(out) > 0 && strings.EqualFold(out[len(out)-1], w) {
			continue
		}
		out = append(out, w)
	}
	if len(out) == 0 {
		return name
	}
	return strings.Join(out, " ")
}

// column is the content column inside a widget of width w: side margins, at most 560px wide, centred.
func column(w int, s func(int) int) (x, width int) {
	width = w - s(40)
	if max := s(560); width > max {
		width = max
	}
	if width < s(120) {
		width = s(120)
	}
	return (w - width) / 2, width
}

// ---- layout ----

// Heights in 96 DPI units. The view is: header (status, timer), the card, then the office list.
const (
	hHeader     = 106
	hCardTop    = 88 // icon row
	hButton     = 46
	hGraph      = 100
	hRates      = 26
	hPill       = 32
	nDetails    = 5 // "More details" shows these rows where the graph is, so the card never changes height
	hCardPad    = 18
	hOfficesGap = 20
	hOfficeRow  = 44
)

type homeState struct {
	name, selected      string
	state               manager.TunnelState
	live, empty         bool
	tunnels             []string
	states              map[string]manager.TunnelState
	addr, endpoint, dns string
	rx, tx, shake       string
	rxRate, txRate      float64
	since               time.Time
	history             [][2]float64
}

func (hp *HomePage) snapshot() homeState {
	name := hp.connected()
	hp.mu.Lock()
	defer hp.mu.Unlock()
	st := homeState{
		name: name, selected: hp.selected, state: hp.states[name], empty: len(hp.tunnels) == 0,
		tunnels: append([]string(nil), hp.tunnels...), states: map[string]manager.TunnelState{},
		rx: hp.rx, tx: hp.tx, shake: hp.shake, rxRate: hp.rxRate, txRate: hp.txRate, since: hp.since,
		history: append([][2]float64(nil), hp.history...),
	}
	for k, v := range hp.states {
		st.states[k] = v
	}
	shown := name
	if shown == "" {
		shown = hp.selected
	}
	st.addr, st.endpoint, st.dns = hp.address[shown], hp.endpoint[shown], hp.dns[shown]
	st.live = name != "" && st.state == manager.TunnelStarted
	return st
}

func (hp *HomePage) cardHeight(st homeState) int {
	h := hCardTop + hButton + hCardPad
	if st.live {
		h += 20 + hGraph + 14 + hRates + 14 + hPill
	}
	return h
}

// contentHeight is the view's height in 96 DPI units.
func (hp *HomePage) contentHeight() int {
	st := hp.snapshot()
	h := hHeader + hp.cardHeight(st)
	if !st.empty {
		h += hOfficesGap + 22 + len(st.tunnels)*hOfficeRow + 34
	}
	return h + 16
}

// ---- painting ----

type painter struct {
	c *walk.Canvas
	s func(int) int
}

func (p painter) fill(color walk.Color, r walk.Rectangle, radius int) {
	b, err := walk.NewSolidColorBrush(color)
	if err != nil {
		return
	}
	defer b.Dispose()
	if radius > 0 {
		p.c.FillRoundedRectanglePixels(b, r, walk.Size{Width: p.s(radius), Height: p.s(radius)})
	} else {
		p.c.FillRectanglePixels(b, r)
	}
}

func (p painter) outline(color walk.Color, width int, r walk.Rectangle, radius int) {
	b, err := walk.NewSolidColorBrush(color)
	if err != nil {
		return
	}
	defer b.Dispose()
	pen, err := walk.NewGeometricPen(walk.PenSolid|walk.PenInsideFrame, max(1, p.s(width)), b)
	if err != nil {
		return
	}
	defer pen.Dispose()
	p.c.DrawRoundedRectanglePixels(pen, r, walk.Size{Width: p.s(radius), Height: p.s(radius)})
}

func (p painter) line(color walk.Color, width int, style walk.PenStyle, pts ...walk.Point) {
	b, err := walk.NewSolidColorBrush(color)
	if err != nil {
		return
	}
	defer b.Dispose()
	pen, err := walk.NewGeometricPen(style|walk.PenCapRound|walk.PenJoinRound, max(1, p.s(width)), b)
	if err != nil {
		return
	}
	defer pen.Dispose()
	p.c.DrawPolylinePixels(pen, pts)
}

func (p painter) dot(color walk.Color, cx, cy, d int) {
	b, err := walk.NewSolidColorBrush(color)
	if err != nil {
		return
	}
	defer b.Dispose()
	p.c.FillEllipsePixels(b, walk.Rectangle{X: cx - d/2, Y: cy - d/2, Width: d, Height: d})
}

func (p painter) text(s string, font *walk.Font, color walk.Color, r walk.Rectangle, format walk.DrawTextFormat) {
	p.c.DrawTextPixels(s, font, color, r, format|walk.TextSingleLine|walk.TextEndEllipsis|walk.TextNoPrefix)
}

func (p painter) textWidth(s string, font *walk.Font) int {
	return textExtent(p.c, s, font) // GDI directly: see textmeasure.go
}

// arrowBadge is the round down/up arrow before a speed (drawn, not a text glyph).
func (p painter) arrowBadge(color walk.Color, cx, cy int, down bool) {
	s := p.s
	r := s(9)
	b, _ := walk.NewSolidColorBrush(color)
	if b == nil {
		return
	}
	defer b.Dispose()
	pen, err := walk.NewGeometricPen(walk.PenSolid, max(1, s(1)+s(1)/2), b)
	if err != nil {
		return
	}
	defer pen.Dispose()
	p.c.DrawEllipsePixels(pen, walk.Rectangle{X: cx - r, Y: cy - r, Width: 2 * r, Height: 2 * r})
	top, bottom := cy-s(4), cy+s(4)
	if down {
		p.line(color, 1, walk.PenSolid, walk.Point{X: cx, Y: top}, walk.Point{X: cx, Y: bottom})
		p.line(color, 1, walk.PenSolid, walk.Point{X: cx - s(3), Y: bottom - s(3)}, walk.Point{X: cx, Y: bottom}, walk.Point{X: cx + s(3), Y: bottom - s(3)})
	} else {
		p.line(color, 1, walk.PenSolid, walk.Point{X: cx, Y: bottom}, walk.Point{X: cx, Y: top})
		p.line(color, 1, walk.PenSolid, walk.Point{X: cx - s(3), Y: top + s(3)}, walk.Point{X: cx, Y: top}, walk.Point{X: cx + s(3), Y: top + s(3)})
	}
}

// serverIcon: two stacked server units with status lights, drawn in the icon tile.
func (p painter) serverIcon(tile walk.Rectangle) {
	s := p.s
	w, h := s(22), s(8)
	x := tile.X + (tile.Width-w)/2
	y := tile.Y + (tile.Height-2*h-s(4))/2
	for i := 0; i < 2; i++ {
		r := walk.Rectangle{X: x, Y: y + i*(h+s(4)), Width: w, Height: h}
		p.outline(darkText, 2, r, 4)
		p.dot(darkText, r.X+s(5), r.Y+r.Height/2, s(3))
		p.dot(darkText, r.X+s(9), r.Y+r.Height/2, s(3))
	}
}

func (hp *HomePage) paint(canvas *walk.Canvas, _ walk.Rectangle) error {
	b := hp.view.ClientBoundsPixels()
	s := func(v int) int { return hp.view.IntFrom96DPI(v) }
	p := painter{canvas, s}
	cx, cw := column(b.Width, s)
	st := hp.snapshot()
	targets := map[string]walk.Rectangle{}
	p.fill(darkBg, b, 0)

	// Wordmark, and a help button that opens Diagnostics.
	if wm := wordmark(); wm != nil {
		h := s(24)
		canvas.DrawImageStretchedPixels(wm, walk.Rectangle{X: cx - s(4), Y: s(16), Width: h * 360 / 96, Height: h})
	}
	help := walk.Rectangle{X: cx + cw - s(34), Y: s(12), Width: s(34), Height: s(34)}
	targets["help"] = help
	if hp.hover == "help" {
		p.fill(darkCardHover, help, 10)
	}
	p.outline(darkLine, 1, help, 10)
	p.text("?", hp.fonts["help"], darkText, help, walk.TextCenter|walk.TextVCenter)

	// Status and connected time.
	title, sub, subColor := l18n.Sprintf("Not connected"), l18n.Sprintf("Choose an office and connect"), darkText3
	switch {
	case st.empty:
		title, sub = l18n.Sprintf("Welcome to CFBTEL VPN"), l18n.Sprintf("Sign in with your work account to add your office")
	case st.live:
		title, subColor = l18n.Sprintf("Securely connected"), darkText2
		sub = clock(time.Since(st.since))
		if st.since.IsZero() {
			sub = ""
		}
	case st.state == manager.TunnelStarting:
		title, sub = l18n.Sprintf("Connecting…"), l18n.Sprintf("Reaching %s", displayName(st.name))
	case st.state == manager.TunnelStopping:
		title, sub = l18n.Sprintf("Disconnecting…"), ""
	}
	p.text(title, hp.fonts["status"], darkText, walk.Rectangle{X: cx, Y: s(52), Width: cw, Height: s(30)}, walk.TextCenter)
	p.text(sub, hp.fonts["timer"], subColor, walk.Rectangle{X: cx, Y: s(78), Width: cw, Height: s(22)}, walk.TextCenter)

	// The card.
	card := walk.Rectangle{X: cx, Y: s(hHeader), Width: cw, Height: s(hp.cardHeight(st))}
	border := darkLine
	switch {
	case st.live:
		border = darkDown
	case st.state == manager.TunnelStarting || st.state == manager.TunnelStopping:
		border = darkWait
	}
	p.fill(darkCard, card, 18)
	p.outline(border, 1, card, 18)
	in := walk.Rectangle{X: card.X + s(hCardPad), Y: card.Y + s(hCardPad), Width: card.Width - 2*s(hCardPad)}

	tile := walk.Rectangle{X: in.X, Y: in.Y, Width: s(52), Height: s(52)}
	p.fill(darkTile, tile, 12)
	p.serverIcon(tile)
	dotColor := darkText3
	switch {
	case st.live:
		dotColor = darkDown
	case st.state == manager.TunnelStarting || st.state == manager.TunnelStopping:
		dotColor = darkWait
	}
	p.dot(darkCard, tile.X+tile.Width-s(2), tile.Y+tile.Height-s(2), s(16))
	p.dot(dotColor, tile.X+tile.Width-s(2), tile.Y+tile.Height-s(2), s(11))

	name, host := displayName(st.selected), st.endpoint
	if st.name != "" {
		name = displayName(st.name)
	}
	if st.empty {
		name, host = l18n.Sprintf("No office yet"), l18n.Sprintf("Your administrator gives you access")
	}
	if i := strings.LastIndex(host, ":"); i > 0 && !st.empty {
		host = host[:i]
	}
	tx := tile.X + tile.Width + s(14)
	p.text(name, hp.fonts["name"], darkText, walk.Rectangle{X: tx, Y: tile.Y + s(4), Width: in.X + in.Width - tx, Height: s(24)}, walk.TextLeft)
	p.text(host, hp.fonts["sub"], darkText2, walk.Rectangle{X: tx, Y: tile.Y + s(28), Width: in.X + in.Width - tx, Height: s(20)}, walk.TextLeft)

	// The one big button.
	btn := walk.Rectangle{X: in.X, Y: card.Y + s(hCardTop), Width: in.Width, Height: s(hButton)}
	label, fill, ink := l18n.Sprintf("Connect"), brandCyan, darkBg
	if hp.hover == "button" {
		fill = darkCyanHi
	}
	switch {
	case st.empty:
		label = l18n.Sprintf("Sign in to CFBTEL")
	case st.live:
		label, fill, ink = l18n.Sprintf("Disconnect"), darkSlate, darkText
		if hp.hover == "button" {
			fill = darkSlateHi
		}
	case st.state == manager.TunnelStarting || st.state == manager.TunnelStopping:
		label, fill, ink = l18n.Sprintf("Please wait…"), darkTile, darkText3
	}
	targets["button"] = btn
	p.fill(fill, btn, 10)
	p.text(label, hp.fonts["button"], ink, btn, walk.TextCenter|walk.TextVCenter)

	if st.live {
		y := btn.Y + btn.Height + s(20)
		// "More details" swaps the graph for the details in the same space, so nothing below moves.
		if hp.details {
			hp.paintDetails(p, walk.Rectangle{X: in.X, Y: y, Width: in.Width, Height: s(hGraph)}, st)
		} else {
			hp.paintGraph(p, walk.Rectangle{X: in.X, Y: y, Width: in.Width, Height: s(hGraph)}, st.history)
		}
		y += s(hGraph) + s(14)

		// Speeds now and the last handshake.
		third := in.Width / 3
		rows := []struct {
			color walk.Color
			text  string
			arrow int // 1 down, 2 up, 0 none
		}{{darkDown, byteRate(st.rxRate), 1}, {darkUp, byteRate(st.txRate), 2}, {darkText, st.shake, 0}}
		for i, r := range rows {
			x := in.X + i*third
			if r.arrow > 0 {
				p.arrowBadge(r.color, x+s(10), y+s(hRates)/2, r.arrow == 1)
				p.text(r.text, hp.fonts["rate"], r.color, walk.Rectangle{X: x + s(26), Y: y, Width: third - s(28), Height: s(hRates)}, walk.TextLeft|walk.TextVCenter)
			} else {
				p.text(r.text, hp.fonts["rate"], r.color, walk.Rectangle{X: x, Y: y, Width: in.X + in.Width - x, Height: s(hRates)}, walk.TextRight|walk.TextVCenter)
			}
		}
		y += s(hRates) + s(14)

		// More details.
		pillText := l18n.Sprintf("More details")
		if hp.details {
			pillText = l18n.Sprintf("Show graph")
		}
		pw := p.textWidth(pillText, hp.fonts["pill"]) + s(36)
		pill := walk.Rectangle{X: in.X + (in.Width-pw)/2, Y: y, Width: pw, Height: s(hPill)}
		targets["details"] = pill
		if hp.hover == "details" {
			p.fill(darkCardHover, pill, hPill)
		}
		p.outline(darkGrid, 1, pill, hPill)
		p.text(pillText, hp.fonts["pill"], darkText, pill, walk.TextCenter|walk.TextVCenter)
		y += s(hPill)
	}

	// Your offices.
	if !st.empty {
		y := card.Y + card.Height + s(hOfficesGap)
		p.text(l18n.Sprintf("Your offices"), hp.fonts["label"], darkText3, walk.Rectangle{X: cx, Y: y, Width: cw, Height: s(20)}, walk.TextLeft|walk.TextVCenter)
		y += s(22)
		for _, n := range st.tunnels {
			row := walk.Rectangle{X: cx, Y: y, Width: cw, Height: s(hOfficeRow - 6)}
			key := "office:" + n
			targets[key] = row
			fill := darkCard
			if hp.hover == key || hp.hover == "connect:"+n {
				fill = darkCardHover
			}
			p.fill(fill, row, 10)
			if n == st.selected {
				p.outline(brandCyan, 1, row, 10)
			}
			dc, right, rc := darkText3, l18n.Sprintf("Connect"), darkLinkText
			switch st.states[n] {
			case manager.TunnelStarted:
				dc, right, rc = darkDown, l18n.Sprintf("Connected"), darkDown
			case manager.TunnelStarting:
				dc, right, rc = darkWait, l18n.Sprintf("Connecting…"), darkWait
			case manager.TunnelStopping:
				dc, right, rc = darkWait, l18n.Sprintf("Disconnecting…"), darkWait
			}
			p.dot(dc, row.X+s(18), row.Y+row.Height/2, s(9))
			rw := p.textWidth(right, hp.fonts["label"]) + s(28)
			p.text(displayName(n), hp.fonts["row"], darkText, walk.Rectangle{X: row.X + s(34), Y: row.Y, Width: row.Width - s(34) - rw, Height: row.Height}, walk.TextLeft|walk.TextVCenter)
			rr := walk.Rectangle{X: row.X + row.Width - rw, Y: row.Y, Width: rw - s(14), Height: row.Height}
			p.text(right, hp.fonts["label"], rc, rr, walk.TextRight|walk.TextVCenter)
			if right == l18n.Sprintf("Connect") {
				targets["connect:"+n] = walk.Rectangle{X: rr.X - s(8), Y: row.Y, Width: rr.Width + s(22), Height: row.Height}
				delete(targets, key)
				targets[key] = walk.Rectangle{X: row.X, Y: row.Y, Width: rr.X - s(8) - row.X, Height: row.Height}
			}
			y += s(hOfficeRow)
		}
		add := walk.Rectangle{X: cx, Y: y, Width: s(200), Height: s(28)}
		targets["add"] = add
		color := darkLinkText
		if hp.hover == "add" {
			color = darkText
		}
		p.line(color, 2, walk.PenSolid, walk.Point{X: add.X + s(6), Y: add.Y + add.Height/2}, walk.Point{X: add.X + s(16), Y: add.Y + add.Height/2})
		p.line(color, 2, walk.PenSolid, walk.Point{X: add.X + s(11), Y: add.Y + add.Height/2 - s(5)}, walk.Point{X: add.X + s(11), Y: add.Y + add.Height/2 + s(5)})
		p.text(l18n.Sprintf("Add an office (sign in)"), hp.fonts["label"], color, walk.Rectangle{X: add.X + s(26), Y: add.Y, Width: add.Width - s(26), Height: add.Height}, walk.TextLeft|walk.TextVCenter)
	}
	hp.targets = targets
	return nil
}

// paintDetails lists the connection's details where the graph usually is.
func (hp *HomePage) paintDetails(p painter, r walk.Rectangle, st homeState) {
	since := ""
	if !st.since.IsZero() {
		since = st.since.Format("3:04 PM") + " (" + shortDuration(time.Since(st.since)) + ")"
	}
	rows := [][2]string{
		{l18n.Sprintf("Received in total"), st.rx},
		{l18n.Sprintf("Sent in total"), st.tx},
		{l18n.Sprintf("Connected since"), since},
		{l18n.Sprintf("Your VPN address"), st.addr},
		{l18n.Sprintf("Office address"), st.endpoint},
	}
	rh := r.Height / nDetails
	for i, row := range rows {
		ry := r.Y + i*rh
		if i > 0 {
			p.line(darkLine, 1, walk.PenSolid, walk.Point{X: r.X, Y: ry}, walk.Point{X: r.X + r.Width, Y: ry})
		}
		half := r.Width / 2
		p.text(row[0], hp.fonts["label"], darkText3, walk.Rectangle{X: r.X, Y: ry, Width: half, Height: rh}, walk.TextLeft|walk.TextVCenter)
		v := row[1]
		if v == "" {
			v = "—"
		}
		p.text(v, hp.fonts["value"], darkText, walk.Rectangle{X: r.X + half, Y: ry, Width: r.Width - half, Height: rh}, walk.TextRight|walk.TextVCenter)
	}
}

// paintGraph draws the last minute of download (green) and upload (orange) on dashed guide lines.
func (hp *HomePage) paintGraph(p painter, r walk.Rectangle, history [][2]float64) {
	s := p.s
	peak := 0.0
	for _, h := range history {
		peak = max(peak, h[0], h[1])
	}
	labelH := s(18)
	area := walk.Rectangle{X: r.X, Y: r.Y + labelH, Width: r.Width, Height: r.Height - labelH}
	p.text(byteRate(peak), hp.fonts["small"], darkText3, walk.Rectangle{X: r.X, Y: r.Y, Width: r.Width, Height: labelH - s(2)}, walk.TextRight|walk.TextBottom)
	p.line(darkGrid, 1, walk.PenSolid, walk.Point{X: area.X, Y: area.Y}, walk.Point{X: area.X + area.Width, Y: area.Y})
	for i := 1; i <= 2; i++ {
		y := area.Y + i*area.Height/3
		p.line(darkGrid, 1, walk.PenDash, walk.Point{X: area.X, Y: y}, walk.Point{X: area.X + area.Width, Y: y})
	}
	base := area.Y + area.Height
	p.line(darkLine, 2, walk.PenSolid, walk.Point{X: area.X, Y: base}, walk.Point{X: area.X + area.Width, Y: base})
	if len(history) < 2 {
		p.text(l18n.Sprintf("Measuring traffic…"), hp.fonts["small"], darkText3, area, walk.TextCenter|walk.TextVCenter)
		return
	}
	if peak <= 0 {
		peak = 1
	}
	top := area.Y + s(4)
	h := base - top - s(2)
	for _, series := range []struct {
		i     int
		color walk.Color
	}{{1, darkUp}, {0, darkDown}} {
		pts := make([]walk.Point, 0, len(history))
		for i, v := range history {
			x := area.X + area.Width - (len(history)-1-i)*area.Width/59
			pts = append(pts, walk.Point{X: x, Y: base - s(2) - int(v[series.i]/peak*float64(h))})
		}
		p.line(series.color, 2, walk.PenSolid, pts...)
	}
}

// paintFooter: version (or update progress), then Check for updates · Diagnostics · Help · About.
func (hp *HomePage) paintFooter(canvas *walk.Canvas, _ walk.Rectangle) error {
	b := hp.footer.ClientBoundsPixels()
	s := func(v int) int { return hp.footer.IntFrom96DPI(v) }
	p := painter{canvas, s}
	p.fill(darkBg, b, 0)
	p.line(darkLine, 1, walk.PenSolid, walk.Point{X: 0, Y: 0}, walk.Point{X: b.Width, Y: 0})
	cx, cw := column(b.Width, s)

	lead := l18n.Sprintf("CFBTEL VPN %s", version.Number)
	switch {
	case hp.updateMsg != "":
		lead = hp.updateMsg
	case hp.update:
		lead += l18n.Sprintf(" · update ready")
	}
	items := []struct{ id, text string }{
		{"update", l18n.Sprintf("Check for updates")}, {"diag", l18n.Sprintf("Diagnostics")},
		{"mail", l18n.Sprintf("Help")}, {"about", l18n.Sprintf("About")},
	}
	font := hp.fonts["footer"]
	gap := s(12)
	x := cx + cw
	targets := map[string]walk.Rectangle{}
	for i := len(items) - 1; i >= 0; i-- {
		w := p.textWidth(items[i].text, font)
		x -= w
		r := walk.Rectangle{X: x, Y: 0, Width: w, Height: b.Height}
		targets[items[i].id] = r
		color := darkLinkText
		if hp.hover == items[i].id {
			color = darkText
		}
		p.text(items[i].text, font, color, r, walk.TextLeft|walk.TextVCenter)
		x -= gap
	}
	p.text(lead, font, darkText3, walk.Rectangle{X: cx, Y: 0, Width: max(0, x-cx-s(4)), Height: b.Height}, walk.TextLeft|walk.TextVCenter)
	hp.ftargets = targets
	return nil
}

// ---- updates ----

// CheckForUpdates looks for a newer CFBTEL VPN now. If there is one, it asks, then disconnects, closes the
// window and installs it; the app opens again afterwards and reconnects the offices that were on.
func (hp *HomePage) CheckForUpdates() {
	if hp.updating {
		return
	}
	hp.setUpdateMsg(l18n.Sprintf("Checking for updates…"))
	go func() {
		u, err := updater.CheckForUpdate()
		hp.Synchronize(func() {
			switch {
			case err != nil:
				hp.setUpdateMsg(l18n.Sprintf("Couldn't check for updates"))
				showErrorCustom(hp.Form(), l18n.Sprintf("Check for updates"), l18n.Sprintf("Couldn't check for updates: %v", err))
			case u == nil:
				hp.setUpdateMsg(l18n.Sprintf("You have the latest version (%s)", version.Number))
			default:
				hp.confirmUpdate(u.Version())
			}
		})
	}()
}

func (hp *HomePage) confirmUpdate(newVersion string) {
	var on []string
	hp.mu.Lock()
	for _, n := range hp.tunnels {
		if st := hp.states[n]; st == manager.TunnelStarted || st == manager.TunnelStarting {
			on = append(on, n)
		}
	}
	hp.mu.Unlock()
	text := l18n.Sprintf("CFBTEL VPN %s is ready to install. You have %s.\n\n", newVersion, version.Number)
	if len(on) > 0 {
		names := make([]string, len(on))
		for i, n := range on {
			names[i] = displayName(n)
		}
		text += l18n.Sprintf("To install it, the VPN (%s) will be disconnected and CFBTEL VPN will close. After about a minute it opens again and reconnects by itself.\n\n", strings.Join(names, ", "))
	} else {
		text += l18n.Sprintf("CFBTEL VPN will close while it installs and open again after about a minute.\n\n")
	}
	text += l18n.Sprintf("Install the update now?")
	if walk.MsgBox(hp.Form(), l18n.Sprintf("Update CFBTEL VPN"), text, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion|walk.MsgBoxDefButton1) != walk.DlgCmdYes {
		hp.setUpdateMsg(l18n.Sprintf("Update %s waits — it installs by itself later", newVersion))
		return
	}
	hp.updating = true
	hp.setUpdateMsg(l18n.Sprintf("Disconnecting to update…"))
	saveWalkSetting("ReconnectAfterUpdate", strings.Join(on, "\n"))
	go func() {
		for _, n := range on {
			t := manager.Tunnel{Name: n}
			t.Stop()
		}
		// Wait (up to 20 s) until the tunnels are down, then let the service download and install.
		for i := 0; i < 40; i++ {
			if gs, err := manager.IPCClientGlobalState(); err == nil && gs == manager.TunnelStopped {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		err := manager.IPCClientUpdate()
		hp.Synchronize(func() {
			if err != nil {
				hp.updating = false
				hp.setUpdateMsg(l18n.Sprintf("Update failed"))
				showErrorCustom(hp.Form(), l18n.Sprintf("Update CFBTEL VPN"), err.Error())
				hp.reconnectAfterUpdate()
				return
			}
			hp.setUpdateMsg(l18n.Sprintf("Downloading the update…"))
		})
	}()
}

// reconnectAfterUpdate turns the offices that were on before an update back on (once).
func (hp *HomePage) reconnectAfterUpdate() {
	list := walkSetting("ReconnectAfterUpdate")
	if list == "" {
		return
	}
	saveWalkSetting("ReconnectAfterUpdate", "")
	for _, n := range strings.Split(list, "\n") {
		if n = strings.TrimSpace(n); n != "" {
			hp.connect(n)
		}
	}
}

func (hp *HomePage) onUpdateProgress(dp updater.DownloadProgress) {
	switch {
	case dp.Error != nil && strings.Contains(dp.Error.Error(), "No update was found"):
		hp.setUpdateMsg(l18n.Sprintf("You have the latest version (%s)", version.Number))
	case dp.Error != nil && strings.Contains(dp.Error.Error(), "already in progress"):
		hp.setUpdateMsg(l18n.Sprintf("An update is already installing"))
	case dp.Error != nil:
		hp.updating = false
		hp.setUpdateMsg(l18n.Sprintf("Update failed"))
		if hp.Form() != nil && hp.Form().Visible() {
			showErrorCustom(hp.Form(), l18n.Sprintf("Update CFBTEL VPN"), l18n.Sprintf("The update didn't install: %v", dp.Error))
		}
		hp.reconnectAfterUpdate()
	case dp.Complete:
		hp.setUpdateMsg(l18n.Sprintf("Update installed, restarting…"))
	case dp.BytesTotal > 0:
		hp.setUpdateMsg(l18n.Sprintf("Downloading the update… %d%%", dp.BytesDownloaded*100/dp.BytesTotal))
	case dp.Activity == "Installing update":
		hp.setUpdateMsg(l18n.Sprintf("Installing the update…"))
		if hp.OnInstalling != nil {
			hp.OnInstalling()
		}
	}
}
