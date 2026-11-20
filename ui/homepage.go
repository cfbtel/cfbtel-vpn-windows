/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2026 CFBTEL. All Rights Reserved.
 */

package ui

import (
	"bytes"
	_ "embed"
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

	header  *walk.CustomWidget
	offices *walk.CustomWidget
	footer  *walk.LinkLabel

	mu       sync.Mutex
	tunnels  []string
	states   map[string]manager.TunnelState
	address  map[string]string
	selected string
	rx, tx   string
	shake    string
	update   bool

	buttonRect walk.Rectangle
	rowRects   []walk.Rectangle
	addRect    walk.Rectangle

	fonts map[string]*walk.Font

	updateMsg string

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

	if hp.header, err = walk.NewCustomWidgetPixels(hp, 0, hp.paintHeader); err != nil {
		return nil, err
	}
	hp.header.SetClearsBackground(true)
	hp.header.SetInvalidatesOnResize(true)
	hp.header.SetMinMaxSize(walk.Size{Width: 0, Height: 268}, walk.Size{Width: 0, Height: 268})
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

	if hp.offices, err = walk.NewCustomWidgetPixels(hp, 0, hp.paintOffices); err != nil {
		return nil, err
	}
	hp.offices.SetClearsBackground(true)
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

// pollStats refreshes traffic and handshake for the connected office every second.
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
		rx, tx, shake := "", "", ""
		if name != "" {
			t := manager.Tunnel{Name: name}
			if c, err := t.RuntimeConfig(); err == nil {
				var r, s conf.Bytes
				var last conf.HandshakeTime
				for _, p := range c.Peers {
					r += p.RxBytes
					s += p.TxBytes
					if p.LastHandshakeTime > last {
						last = p.LastHandshakeTime
					}
				}
				rx, tx = r.String(), s.String()
				if last.IsEmpty() {
					shake = l18n.Sprintf("Waiting")
				} else {
					shake = last.String()
				}
			}
		}
		hp.mu.Lock()
		changed := rx != hp.rx || tx != hp.tx || shake != hp.shake
		hp.rx, hp.tx, hp.shake = rx, tx, shake
		hp.mu.Unlock()
		if changed {
			hp.Synchronize(func() { hp.header.Invalidate() })
		}
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

func (hp *HomePage) repaint() {
	hp.mu.Lock()
	rows := len(hp.tunnels)
	hp.mu.Unlock()
	h := 52 + rows*44 + 44
	hp.offices.SetMinMaxSize(walk.Size{Width: 0, Height: h}, walk.Size{Width: 0, Height: 0})
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
	text += ` · <a id="update" href="update">Check for updates</a> · <a id="help" href="mailto:help@cfbtel.com">Help</a> · <a id="about" href="about">About</a>`
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

func displayName(name string) string {
	return strings.NewReplacer("_", " ").Replace(name)
}

func (hp *HomePage) paintHeader(canvas *walk.Canvas, _ walk.Rectangle) error {
	b := canvas.BoundsPixels()
	s := func(v int) int { return hp.header.IntFrom96DPI(v) }
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
	hp.mu.Unlock()

	// The CFBTEL wordmark, and a status dot on the right.
	if wm := wordmark(); wm != nil {
		h := s(30)
		canvas.DrawImageStretchedPixels(wm, walk.Rectangle{X: s(16), Y: s(14), Width: h * 360 / 96, Height: h})
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
	canvas.FillEllipsePixels(dot, walk.Rectangle{X: b.Width - s(36), Y: s(24), Width: s(12), Height: s(12)})

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
	textX := s(22)
	canvas.DrawTextPixels(title, hp.fonts["title"], brandWhite, walk.Rectangle{X: textX, Y: s(60), Width: b.Width - textX - s(20), Height: s(30)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)
	canvas.DrawTextPixels(sub, hp.fonts["sub"], brandCyanText, walk.Rectangle{X: textX, Y: s(90), Width: b.Width - textX - s(20), Height: s(22)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)

	// Three stat tiles while connected.
	if state == manager.TunnelStarted {
		gap := s(10)
		w := (b.Width - s(44) - 2*gap) / 3
		tiles := [][2]string{{l18n.Sprintf("Last handshake"), shake}, {l18n.Sprintf("Received"), rx}, {l18n.Sprintf("Sent"), tx}}
		for i, t := range tiles {
			r := walk.Rectangle{X: s(22) + i*(w+gap), Y: s(124), Width: w, Height: s(54)}
			canvas.FillRoundedRectanglePixels(grey, r, walk.Size{Width: s(10), Height: s(10)})
			canvas.DrawTextPixels(t[0], hp.fonts["small"], brandMuted, walk.Rectangle{X: r.X + s(12), Y: r.Y + s(8), Width: r.Width - s(16), Height: s(18)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)
			v := t[1]
			if v == "" {
				v = "—"
			}
			canvas.DrawTextPixels(v, hp.fonts["stat"], brandWhite, walk.Rectangle{X: r.X + s(12), Y: r.Y + s(26), Width: r.Width - s(16), Height: s(22)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)
		}
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
	hp.buttonRect = walk.Rectangle{X: s(22), Y: s(196), Width: b.Width - s(44), Height: s(48)}
	btn, _ := walk.NewSolidColorBrush(fill)
	defer btn.Dispose()
	canvas.FillRoundedRectanglePixels(btn, hp.buttonRect, walk.Size{Width: s(12), Height: s(12)})
	canvas.DrawTextPixels(label, hp.fonts["button"], ink, hp.buttonRect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	return nil
}

func (hp *HomePage) paintOffices(canvas *walk.Canvas, _ walk.Rectangle) error {
	b := canvas.BoundsPixels()
	s := func(v int) int { return hp.offices.IntFrom96DPI(v) }
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

	canvas.DrawTextPixels(l18n.Sprintf("Your offices"), hp.fonts["rowSmall"], brandInkMuted, walk.Rectangle{X: s(22), Y: s(16), Width: b.Width - s(44), Height: s(20)}, walk.TextLeft|walk.TextSingleLine)

	white, _ := walk.NewSolidColorBrush(brandWhite)
	defer white.Dispose()
	sel, _ := walk.NewSolidColorBrush(walk.RGB(0xE6, 0xF6, 0xFD))
	defer sel.Dispose()
	border, _ := walk.NewCosmeticPen(walk.PenSolid, brandBorder)
	defer border.Dispose()

	rows := make([]walk.Rectangle, 0, len(tunnels))
	y := s(42)
	for _, n := range tunnels {
		r := walk.Rectangle{X: s(22), Y: y, Width: b.Width - s(44), Height: s(40)}
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
		canvas.DrawTextPixels(displayName(n), hp.fonts["row"], brandInk, walk.Rectangle{X: r.X + s(34), Y: r.Y, Width: r.Width - s(160), Height: r.Height}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
		canvas.DrawTextPixels(right, hp.fonts["rowSmall"], rightColor, walk.Rectangle{X: r.X + r.Width - s(124), Y: r.Y, Width: s(110), Height: r.Height}, walk.TextRight|walk.TextVCenter|walk.TextSingleLine)
		rows = append(rows, r)
		y += s(44)
	}
	hp.rowRects = rows

	hp.addRect = walk.Rectangle{X: s(22), Y: y + s(4), Width: s(220), Height: s(26)}
	canvas.DrawTextPixels(l18n.Sprintf("+  Add an office (sign in)"), hp.fonts["rowSmall"], brandLink, hp.addRect, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine)
	return nil
}
