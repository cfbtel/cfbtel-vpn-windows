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

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"

	"golang.zx2c4.com/wireguard/windows/conf"
	"golang.zx2c4.com/wireguard/windows/l18n"
	"golang.zx2c4.com/wireguard/windows/manager"
	"golang.zx2c4.com/wireguard/windows/updater"
	"golang.zx2c4.com/wireguard/windows/version"
)

// CFBTEL brand colours (logo navy and cyan).
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

// HomePage is the simple CFBTEL screen: status, one big button and the person's offices.
// The full WireGuard tunnel editor stays on the "Advanced" tab.
type HomePage struct {
	*walk.TabPage

	scroll  *walk.ScrollView
	header  *walk.CustomWidget
	offices *walk.CustomWidget
	footer  *walk.LinkLabel

	mu       sync.Mutex
	tunnels  []string
	states   map[string]manager.TunnelState
	address  map[string]string
	selected string
	rx, tx   string // totals since connected
	shake    string
	update   bool

	// Live traffic for the connected office.
	statsFor       string
	since          time.Time
	prevRx, prevTx conf.Bytes
	prevAt         time.Time
	rxRate, txRate float64      // bytes per second, now
	history        [][2]float64 // last minute of [rx, tx] bytes per second

	buttonRect walk.Rectangle
	rowRects   []walk.Rectangle
	addRect    walk.Rectangle

	fonts map[string]*walk.Font

	updateMsg string

	// OpenDiagnostics switches to the Diagnostics tab (set by the window).
	OpenDiagnostics func()

	tunnelChangedCB  *manager.TunnelChangeCallback
	tunnelsChangedCB *manager.TunnelsChangeCallback
	updateProgressCB *manager.UpdateProgressCallback
	stop             chan struct{}
}

func NewHomePage() (*HomePage, error) {
	var err error
	var disposables walk.Disposables
	defer disposables.Treat()

	hp := &HomePage{states: map[string]manager.TunnelState{}, address: map[string]string{}, stop: make(chan struct{})}
	if hp.TabPage, err = walk.NewTabPage(); err != nil {
		return nil, err
	}
	disposables.Add(hp)
	hp.SetTitle(l18n.Sprintf("Home"))
	layout := walk.NewVBoxLayout()
	layout.SetMargins(walk.Margins{})
	layout.SetSpacing(0)
	hp.SetLayout(layout)
	if bg, err := walk.NewSolidColorBrush(brandPage); err == nil {
		hp.SetBackground(bg)
	}

	hp.fonts = map[string]*walk.Font{}
	for name, f := range map[string]struct {
		size  int
		style walk.FontStyle
	}{"title": {16, walk.FontBold}, "sub": {10, 0}, "small": {8, 0}, "stat": {11, walk.FontBold}, "button": {11, walk.FontBold}, "row": {10, 0}, "rowSmall": {9, 0}} {
		if font, err := walk.NewFont("Segoe UI", f.size, f.style); err == nil {
			hp.fonts[name] = font
		}
	}

	// Header and offices scroll when the window is short; the footer stays at the bottom.
	if hp.scroll, err = walk.NewScrollView(hp); err != nil {
		return nil, err
	}
	hp.scroll.SetScrollbars(false, true)
	inner := walk.NewVBoxLayout()
	inner.SetMargins(walk.Margins{})
	inner.SetSpacing(0)
	hp.scroll.SetLayout(inner)
	if bg, err := walk.NewSolidColorBrush(brandPage); err == nil {
		hp.scroll.SetBackground(bg)
	}

	if hp.header, err = walk.NewCustomWidgetPixels(hp.scroll, 0, hp.paintHeader); err != nil {
		return nil, err
	}
	hp.header.SetPaintMode(walk.PaintBuffered)
	hp.header.SetInvalidatesOnResize(true)
	hp.header.SetMinMaxSize(walk.Size{Width: 0, Height: headerCompact}, walk.Size{Width: 0, Height: headerCompact})
	hp.header.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton && hit(hp.buttonRect, x, y) {
			hp.onButton()
		}
	})
	hp.header.MouseMove().Attach(func(x, y int, button walk.MouseButton) {
		if hit(hp.buttonRect, x, y) {
			hp.header.SetCursor(walk.CursorHand())
		} else {
			hp.header.SetCursor(walk.CursorArrow())
		}
	})

	if hp.offices, err = walk.NewCustomWidgetPixels(hp.scroll, 0, hp.paintOffices); err != nil {
		return nil, err
	}
	hp.offices.SetPaintMode(walk.PaintBuffered)
	hp.offices.SetInvalidatesOnResize(true)
	hp.offices.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			hp.onOfficeClick(x, y)
		}
	})
	hp.offices.MouseMove().Attach(func(x, y int, button walk.MouseButton) {
		over := hit(hp.addRect, x, y)
		for _, r := range hp.rowRects {
			over = over || hit(r, x, y)
		}
		if over {
			hp.offices.SetCursor(walk.CursorHand())
		} else {
			hp.offices.SetCursor(walk.CursorArrow())
		}
	})

	if hp.footer, err = walk.NewLinkLabel(hp); err != nil {
		return nil, err
	}
	hp.footer.SetAlignment(walk.AlignHNearVCenter)
	hp.footer.LinkActivated().Attach(func(link *walk.LinkLabelLink) {
		switch link.Id() {
		case "help":
			win.ShellExecute(hp.Form().Handle(), nil, windows.StringToUTF16Ptr("mailto:help@cfbtel.com?subject=CFBTEL%20VPN%20"+version.Number), nil, nil, win.SW_SHOWNORMAL)
		case "about":
			onAbout(hp.Form())
		case "update":
			hp.CheckForUpdates()
		case "diag":
			if hp.OpenDiagnostics != nil {
				hp.OpenDiagnostics()
			}
		}
	})
	hp.updateFooter()

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

func walkSetting(key string) string {
	if s := walk.App().Settings(); s != nil {
		v, _ := s.Get(key)
		return v
	}
	return ""
}

func saveWalkSetting(key, value string) {
	if s := walk.App().Settings(); s != nil {
		s.Put(key, value)
	}
}

// reload reads the offices (tunnels), their state and VPN address from the manager.
func (hp *HomePage) reload() {
	tunnels, err := manager.IPCClientTunnels()
	if err != nil {
		return
	}
	names := make([]string, 0, len(tunnels))
	states := map[string]manager.TunnelState{}
	address := map[string]string{}
	for i := range tunnels {
		t := tunnels[i]
		names = append(names, t.Name)
		if st, err := t.State(); err == nil {
			states[t.Name] = st
		}
		if c, err := t.StoredConfig(); err == nil && len(c.Interface.Addresses) > 0 {
			address[t.Name] = c.Interface.Addresses[0].Addr().String()
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	hp.mu.Lock()
	hp.tunnels, hp.states, hp.address = names, states, address
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
		hp.mu.Lock()
		if !ok || name != hp.statsFor || hp.states[name] != manager.TunnelStarted {
			// New connection (or none): start counting again.
			hp.statsFor, hp.history, hp.rxRate, hp.txRate = "", nil, 0, 0
			hp.since = time.Time{}
			if ok && hp.states[name] == manager.TunnelStarted {
				hp.statsFor, hp.since = name, now
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
		hp.Synchronize(func() { hp.header.Invalidate() })
	}
}

// shortHandshake is "12s ago", "1m 49s ago", "2h 5m ago" or "Waiting".
func shortHandshake(t conf.HandshakeTime) string {
	if t.IsEmpty() {
		return l18n.Sprintf("Waiting")
	}
	return shortDuration(time.Since(time.Unix(0, 0).Add(time.Duration(t)))) + l18n.Sprintf(" ago")
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

// rate shows bytes per second as bits per second, like speed tests do: "850 kbps", "12.4 Mbps", "1.02 Gbps".
func rate(bytesPerSec float64) string {
	bits := bytesPerSec * 8
	switch {
	case bits >= 1e9:
		return fmt.Sprintf("%.2f Gbps", bits/1e9)
	case bits >= 1e7:
		return fmt.Sprintf("%.0f Mbps", bits/1e6)
	case bits >= 1e6:
		return fmt.Sprintf("%.1f Mbps", bits/1e6)
	default:
		return fmt.Sprintf("%.0f kbps", bits/1e3)
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

// Header heights (96 DPI): compact when not connected, with live traffic when connected.
const (
	headerCompact   = 196
	headerConnected = 424
)

func (hp *HomePage) repaint() {
	name := hp.connected()
	hp.mu.Lock()
	rows := len(hp.tunnels)
	live := name != "" && hp.states[name] == manager.TunnelStarted
	hp.mu.Unlock()
	hh := headerCompact
	if live {
		hh = headerConnected
	}
	hp.header.SetMinMaxSize(walk.Size{Width: 0, Height: hh}, walk.Size{Width: 0, Height: hh})
	h := 52 + rows*44 + 44
	hp.offices.SetMinMaxSize(walk.Size{Width: 0, Height: h}, walk.Size{Width: 0, Height: 0})
	hp.scroll.RequestLayout()
	hp.header.Invalidate()
	hp.offices.Invalidate()
}

func (hp *HomePage) UpdateFound() {
	hp.update = true
	hp.updateFooter()
}

func (hp *HomePage) updateFooter() {
	text := l18n.Sprintf("CFBTEL VPN %s", version.Number)
	switch {
	case hp.updateMsg != "":
		text += " · " + hp.updateMsg
	case hp.update:
		text += l18n.Sprintf(" · a new version installs by itself")
	}
	text += ` · <a id="update" href="update">Check for updates</a> · <a id="diag" href="diag">Diagnostics</a> · <a id="help" href="mailto:help@cfbtel.com">Help</a> · <a id="about" href="about">About</a>`
	hp.footer.SetText(text)
}

// CheckForUpdates looks for a newer CFBTEL VPN now and installs it if there is one. (The app also checks every
// hour on its own and installs updates by itself.)
func (hp *HomePage) CheckForUpdates() {
	hp.updateMsg = l18n.Sprintf("checking for updates…")
	hp.updateFooter()
	go func() {
		if err := manager.IPCClientUpdate(); err != nil {
			hp.Synchronize(func() {
				hp.updateMsg = l18n.Sprintf("couldn't check: %v", err)
				hp.updateFooter()
			})
		}
	}()
}

func (hp *HomePage) onUpdateProgress(dp updater.DownloadProgress) {
	switch {
	case dp.Error != nil && strings.Contains(dp.Error.Error(), "No update was found"):
		hp.updateMsg = l18n.Sprintf("you have the latest version")
	case dp.Error != nil && strings.Contains(dp.Error.Error(), "already in progress"):
		hp.updateMsg = l18n.Sprintf("an update is already installing")
	case dp.Error != nil:
		hp.updateMsg = l18n.Sprintf("update failed: %v", dp.Error)
	case dp.Complete:
		hp.updateMsg = l18n.Sprintf("update installed, restarting…")
	case dp.BytesTotal > 0:
		hp.updateMsg = l18n.Sprintf("downloading update %d%%", dp.BytesDownloaded*100/dp.BytesTotal)
	case dp.Activity == "Installing update":
		hp.updateMsg = l18n.Sprintf("installing update…")
	default:
		return
	}
	hp.updateFooter()
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

func (hp *HomePage) onOfficeClick(x, y int) {
	if hit(hp.addRect, x, y) {
		onSignIn(hp.Form())
		return
	}
	hp.mu.Lock()
	var name string
	for i, r := range hp.rowRects {
		if hit(r, x, y) && i < len(hp.tunnels) {
			name = hp.tunnels[i]
		}
	}
	state := hp.states[name]
	hp.mu.Unlock()
	if name == "" {
		return
	}
	hp.mu.Lock()
	hp.selected = name
	hp.mu.Unlock()
	saveWalkSetting("HomeOffice", name)
	// The link on the right of a row connects (switches) to that office straight away.
	if state == manager.TunnelStopped || state == manager.TunnelUnknown {
		if r := hp.rowRects; len(r) > 0 && x > r[0].X+r[0].Width-hp.offices.IntFrom96DPI(110) {
			hp.connect(name)
			return
		}
	}
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

// column is the content column inside a widget of width w: 22px side margins, at most 560px wide, centred
// when the window is wider (maximized), so the button and tiles never stretch across a big screen.
func column(w int, s func(int) int) (x, width int) {
	width = w - s(44)
	if max := s(560); width > max {
		width = max
	}
	if width < s(120) {
		width = s(120)
	}
	return (w - width) / 2, width
}

func (hp *HomePage) paintHeader(canvas *walk.Canvas, _ walk.Rectangle) error {
	// The widget's own size. (Canvas.BoundsPixels is the whole screen's size, which pushed everything
	// on the right out of the window.)
	b := hp.header.ClientBoundsPixels()
	s := func(v int) int { return hp.header.IntFrom96DPI(v) }
	cx, cw := column(b.Width, s)
	navy, _ := walk.NewSolidColorBrush(brandNavy)
	defer navy.Dispose()
	canvas.FillRectanglePixels(navy, b)

	name := hp.connected()
	hp.mu.Lock()
	state := hp.states[name]
	empty := len(hp.tunnels) == 0
	selected := hp.selected
	addr := hp.address[name]
	rx, tx, shake := hp.rx, hp.tx, hp.shake
	rxRate, txRate, since := hp.rxRate, hp.txRate, hp.since
	history := append([][2]float64(nil), hp.history...)
	hp.mu.Unlock()

	// The CFBTEL wordmark, and a status dot on the right.
	if wm := wordmark(); wm != nil {
		h := s(30)
		canvas.DrawImageStretchedPixels(wm, walk.Rectangle{X: cx - s(6), Y: s(14), Width: h * 360 / 96, Height: h})
	}
	cyan, _ := walk.NewSolidColorBrush(brandCyan)
	defer cyan.Dispose()
	grey, _ := walk.NewSolidColorBrush(brandNavyLight)
	defer grey.Dispose()
	dotColor := brandMuted
	switch state {
	case manager.TunnelStarted:
		dotColor = brandGreen
	case manager.TunnelStarting, manager.TunnelStopping:
		dotColor = brandAmber
	}
	dot, _ := walk.NewSolidColorBrush(dotColor)
	defer dot.Dispose()
	canvas.FillEllipsePixels(dot, walk.Rectangle{X: cx + cw - s(12), Y: s(24), Width: s(12), Height: s(12)})

	title, sub := l18n.Sprintf("Not connected"), ""
	switch {
	case empty:
		title, sub = l18n.Sprintf("Welcome to CFBTEL VPN"), l18n.Sprintf("Sign in with your work account to add your office.")
	case state == manager.TunnelStarted:
		title, sub = l18n.Sprintf("Connected"), displayName(name)
		if addr != "" {
			sub += " · " + addr
		}
	case state == manager.TunnelStarting:
		title, sub = l18n.Sprintf("Connecting…"), displayName(name)
	case state == manager.TunnelStopping:
		title, sub = l18n.Sprintf("Disconnecting…"), displayName(name)
	default:
		sub = l18n.Sprintf("Office: %s", displayName(selected))
	}
	canvas.DrawTextPixels(title, hp.fonts["title"], brandWhite, walk.Rectangle{X: cx, Y: s(60), Width: cw, Height: s(30)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)
	canvas.DrawTextPixels(sub, hp.fonts["sub"], brandCyanText, walk.Rectangle{X: cx, Y: s(90), Width: cw, Height: s(22)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)

	buttonY := s(124)
	if state == manager.TunnelStarted {
		// Live traffic: two rows of three tiles, then the last minute as a graph.
		gap := s(10)
		w := (cw - 2*gap) / 3
		dash := func(v string) string {
			if v == "" {
				return "—"
			}
			return v
		}
		connectedFor := ""
		if !since.IsZero() {
			connectedFor = shortDuration(time.Since(since))
		}
		tiles := [][3]string{
			{l18n.Sprintf("↓ Download now"), rate(rxRate), "cyan"},
			{l18n.Sprintf("↑ Upload now"), rate(txRate), ""},
			{l18n.Sprintf("Connected for"), dash(connectedFor), ""},
			{l18n.Sprintf("Received in total"), dash(rx), ""},
			{l18n.Sprintf("Sent in total"), dash(tx), ""},
			{l18n.Sprintf("Last handshake"), dash(shake), ""},
		}
		for i, t := range tiles {
			r := walk.Rectangle{X: cx + (i%3)*(w+gap), Y: s(124) + (i/3)*s(64), Width: w, Height: s(56)}
			canvas.FillRoundedRectanglePixels(grey, r, walk.Size{Width: s(10), Height: s(10)})
			canvas.DrawTextPixels(t[0], hp.fonts["small"], brandMuted, walk.Rectangle{X: r.X + s(12), Y: r.Y + s(8), Width: r.Width - s(16), Height: s(18)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)
			color := brandWhite
			if t[2] == "cyan" {
				color = brandCyan
			}
			canvas.DrawTextPixels(t[1], hp.fonts["stat"], color, walk.Rectangle{X: r.X + s(12), Y: r.Y + s(27), Width: r.Width - s(16), Height: s(22)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)
		}
		hp.paintGraph(canvas, walk.Rectangle{X: cx, Y: s(256), Width: cw, Height: s(84)}, history, s)
		buttonY = s(356)
	}

	// The one big button.
	label, fill, ink := l18n.Sprintf("Connect"), brandCyan, brandNavy
	switch {
	case empty:
		label = l18n.Sprintf("Sign in to CFBTEL")
	case state == manager.TunnelStarted:
		label, fill = l18n.Sprintf("Disconnect"), brandWhite
	case state == manager.TunnelStarting || state == manager.TunnelStopping:
		label, fill, ink = l18n.Sprintf("Please wait…"), brandNavyLight, brandMuted
	default:
		label = l18n.Sprintf("Connect to %s", displayName(selected))
	}
	hp.buttonRect = walk.Rectangle{X: cx, Y: buttonY, Width: cw, Height: s(48)}
	btn, _ := walk.NewSolidColorBrush(fill)
	defer btn.Dispose()
	canvas.FillRoundedRectanglePixels(btn, hp.buttonRect, walk.Size{Width: s(12), Height: s(12)})
	canvas.DrawTextPixels(label, hp.fonts["button"], ink, hp.buttonRect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	return nil
}

// paintGraph draws the last minute of download (cyan) and upload (white) speed.
func (hp *HomePage) paintGraph(canvas *walk.Canvas, r walk.Rectangle, history [][2]float64, s func(int) int) {
	bg, _ := walk.NewSolidColorBrush(brandNavyLight)
	defer bg.Dispose()
	canvas.FillRoundedRectanglePixels(bg, r, walk.Size{Width: s(10), Height: s(10)})
	peak := 0.0
	for _, h := range history {
		if h[0] > peak {
			peak = h[0]
		}
		if h[1] > peak {
			peak = h[1]
		}
	}
	canvas.DrawTextPixels(l18n.Sprintf("Traffic, last minute"), hp.fonts["small"], brandMuted, walk.Rectangle{X: r.X + s(12), Y: r.Y + s(6), Width: r.Width / 2, Height: s(16)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)
	legend := l18n.Sprintf("peak %s", rate(peak))
	canvas.DrawTextPixels(legend, hp.fonts["small"], brandMuted, walk.Rectangle{X: r.X + r.Width/2, Y: r.Y + s(6), Width: r.Width/2 - s(12), Height: s(16)}, walk.TextRight|walk.TextSingleLine|walk.TextEndEllipsis)
	area := walk.Rectangle{X: r.X + s(12), Y: r.Y + s(26), Width: r.Width - s(24), Height: r.Height - s(34)}
	if len(history) < 2 || peak <= 0 {
		canvas.DrawTextPixels(l18n.Sprintf("Measuring…"), hp.fonts["small"], brandMuted, area, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
		return
	}
	for series, color := range []walk.Color{brandCyan, brandWhite} {
		brush, err := walk.NewSolidColorBrush(color)
		if err != nil {
			continue
		}
		pen, err := walk.NewGeometricPen(walk.PenSolid|walk.PenCapRound|walk.PenJoinRound, s(2), brush)
		if err != nil {
			brush.Dispose()
			continue
		}
		pts := make([]walk.Point, 0, len(history))
		for i, h := range history {
			// Always 60 slots wide, newest on the right.
			x := area.X + area.Width - (len(history)-1-i)*area.Width/59
			y := area.Y + area.Height - int(h[series]/peak*float64(area.Height))
			pts = append(pts, walk.Point{X: x, Y: y})
		}
		canvas.DrawPolylinePixels(pen, pts)
		pen.Dispose()
		brush.Dispose()
	}
}

func (hp *HomePage) paintOffices(canvas *walk.Canvas, _ walk.Rectangle) error {
	b := hp.offices.ClientBoundsPixels()
	s := func(v int) int { return hp.offices.IntFrom96DPI(v) }
	cx, cw := column(b.Width, s)
	page, _ := walk.NewSolidColorBrush(brandPage)
	defer page.Dispose()
	canvas.FillRectanglePixels(page, b)

	hp.mu.Lock()
	tunnels := append([]string(nil), hp.tunnels...)
	states := map[string]manager.TunnelState{}
	for k, v := range hp.states {
		states[k] = v
	}
	selected := hp.selected
	hp.mu.Unlock()

	canvas.DrawTextPixels(l18n.Sprintf("Your offices"), hp.fonts["rowSmall"], brandInkMuted, walk.Rectangle{X: cx, Y: s(16), Width: cw, Height: s(20)}, walk.TextLeft|walk.TextSingleLine)

	white, _ := walk.NewSolidColorBrush(brandWhite)
	defer white.Dispose()
	sel, _ := walk.NewSolidColorBrush(walk.RGB(0xE6, 0xF6, 0xFD))
	defer sel.Dispose()
	border, _ := walk.NewCosmeticPen(walk.PenSolid, brandBorder)
	defer border.Dispose()

	rows := make([]walk.Rectangle, 0, len(tunnels))
	y := s(42)
	for _, n := range tunnels {
		r := walk.Rectangle{X: cx, Y: y, Width: cw, Height: s(40)}
		if n == selected {
			canvas.FillRoundedRectanglePixels(sel, r, walk.Size{Width: s(8), Height: s(8)})
		} else {
			canvas.FillRoundedRectanglePixels(white, r, walk.Size{Width: s(8), Height: s(8)})
		}
		canvas.DrawRoundedRectanglePixels(border, r, walk.Size{Width: s(8), Height: s(8)})

		dotColor, right, rightColor := brandGrey, l18n.Sprintf("Connect"), brandLink
		switch states[n] {
		case manager.TunnelStarted:
			dotColor, right, rightColor = brandGreen, l18n.Sprintf("Connected"), brandGreen
		case manager.TunnelStarting:
			dotColor, right, rightColor = brandAmber, l18n.Sprintf("Connecting…"), brandAmber
		case manager.TunnelStopping:
			dotColor, right, rightColor = brandAmber, l18n.Sprintf("Disconnecting…"), brandAmber
		}
		dot, _ := walk.NewSolidColorBrush(dotColor)
		canvas.FillEllipsePixels(dot, walk.Rectangle{X: r.X + s(14), Y: r.Y + s(16), Width: s(9), Height: s(9)})
		dot.Dispose()
		canvas.DrawTextPixels(displayName(n), hp.fonts["row"], brandInk, walk.Rectangle{X: r.X + s(34), Y: r.Y, Width: r.Width - s(34) - s(130), Height: r.Height}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
		canvas.DrawTextPixels(right, hp.fonts["rowSmall"], rightColor, walk.Rectangle{X: r.X + r.Width - s(124), Y: r.Y, Width: s(110), Height: r.Height}, walk.TextRight|walk.TextVCenter|walk.TextSingleLine)
		rows = append(rows, r)
		y += s(44)
	}
	hp.rowRects = rows

	hp.addRect = walk.Rectangle{X: cx, Y: y + s(4), Width: s(220), Height: s(26)}
	canvas.DrawTextPixels(l18n.Sprintf("+  Add an office (sign in)"), hp.fonts["rowSmall"], brandLink, hp.addRect, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine)
	return nil
}
