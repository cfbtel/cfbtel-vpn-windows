/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2026 CFBTEL. All Rights Reserved.
 */

package ui

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"

	"golang.zx2c4.com/wireguard/windows/l18n"
	"golang.zx2c4.com/wireguard/windows/speedtest"
)

// SpeedTestPage runs a download / upload / ping / jitter test on Cloudflare's speed test network.
type SpeedTestPage struct {
	*walk.TabPage

	view   *walk.CustomWidget
	footer *walk.LinkLabel
	vpn    func() string // the office the VPN is connected to, or ""

	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	progress speedtest.Progress
	result   *speedtest.Result
	through  string
	err      string

	buttonRect walk.Rectangle
	fonts      map[string]*walk.Font
}

func NewSpeedTestPage(vpn func() string) (*SpeedTestPage, error) {
	var err error
	var disposables walk.Disposables
	defer disposables.Treat()

	sp := &SpeedTestPage{vpn: vpn, fonts: map[string]*walk.Font{}}
	if sp.TabPage, err = walk.NewTabPage(); err != nil {
		return nil, err
	}
	disposables.Add(sp)
	sp.SetTitle(l18n.Sprintf("Speed test"))
	layout := walk.NewVBoxLayout()
	layout.SetMargins(walk.Margins{})
	layout.SetSpacing(0)
	sp.SetLayout(layout)
	if bg, err := walk.NewSolidColorBrush(brandPage); err == nil {
		sp.SetBackground(bg)
	}
	for name, f := range map[string]struct {
		size  int
		style walk.FontStyle
	}{"title": {14, walk.FontBold}, "sub": {9, 0}, "big": {30, walk.FontBold}, "unit": {11, 0}, "label": {8, 0}, "value": {14, walk.FontBold}, "button": {11, walk.FontBold}, "small": {9, 0}} {
		if font, err := walk.NewFont("Segoe UI", f.size, f.style); err == nil {
			sp.fonts[name] = font
		}
	}

	if sp.view, err = walk.NewCustomWidgetPixels(sp, 0, sp.paint); err != nil {
		return nil, err
	}
	sp.view.SetPaintMode(walk.PaintBuffered)
	sp.view.SetInvalidatesOnResize(true)
	sp.view.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton && hit(sp.buttonRect, x, y) {
			sp.onButton()
		}
	})
	sp.view.MouseMove().Attach(func(x, y int, button walk.MouseButton) {
		if hit(sp.buttonRect, x, y) {
			sp.view.SetCursor(walk.CursorHand())
		} else {
			sp.view.SetCursor(walk.CursorArrow())
		}
	})

	if sp.footer, err = walk.NewLinkLabel(sp); err != nil {
		return nil, err
	}
	sp.footer.SetText(l18n.Sprintf(`Measured on Cloudflare's speed test network · <a id="ookla" href="https://www.speedtest.net/">Open speedtest.net</a>`))
	sp.footer.LinkActivated().Attach(func(link *walk.LinkLabelLink) {
		win.ShellExecute(sp.Form().Handle(), nil, windows.StringToUTF16Ptr(link.URL()), nil, nil, win.SW_SHOWNORMAL)
	})

	disposables.Spare()
	return sp, nil
}

func (sp *SpeedTestPage) Dispose() {
	sp.mu.Lock()
	if sp.cancel != nil {
		sp.cancel()
	}
	sp.mu.Unlock()
	sp.TabPage.Dispose()
}

func (sp *SpeedTestPage) onButton() {
	sp.mu.Lock()
	if sp.running {
		if sp.cancel != nil {
			sp.cancel()
		}
		sp.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	sp.running, sp.cancel, sp.err = true, cancel, ""
	sp.progress = speedtest.Progress{Phase: speedtest.PhaseLatency}
	sp.through = ""
	if sp.vpn != nil {
		sp.through = sp.vpn()
	}
	sp.mu.Unlock()
	sp.view.Invalidate()

	go func() {
		last := time.Time{}
		res, err := speedtest.New().Run(ctx, func(p speedtest.Progress) {
			sp.mu.Lock()
			sp.progress = p
			sp.mu.Unlock()
			if time.Since(last) > 100*time.Millisecond || p.Phase == speedtest.PhaseDone {
				last = time.Now()
				sp.Synchronize(func() { sp.view.Invalidate() })
			}
		})
		sp.mu.Lock()
		sp.running, sp.cancel = false, nil
		switch {
		case errors.Is(err, context.Canceled):
			sp.err = l18n.Sprintf("Test stopped.")
		case err != nil:
			sp.err = err.Error()
		default:
			sp.result = &res
		}
		sp.mu.Unlock()
		cancel()
		sp.Synchronize(func() { sp.view.Invalidate() })
	}()
}

func mbps(v float64) string {
	switch {
	case v <= 0:
		return "—"
	case v >= 100:
		return fmt.Sprintf("%.0f", v)
	case v >= 10:
		return fmt.Sprintf("%.1f", v)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}

func ms(v float64) string {
	if v <= 0 {
		return "—"
	}
	if v >= 10 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

func (sp *SpeedTestPage) paint(canvas *walk.Canvas, _ walk.Rectangle) error {
	b := sp.view.ClientBoundsPixels()
	s := func(v int) int { return sp.view.IntFrom96DPI(v) }
	cx, cw := column(b.Width, s)

	page, _ := walk.NewSolidColorBrush(brandPage)
	defer page.Dispose()
	canvas.FillRectanglePixels(page, b)
	navy, _ := walk.NewSolidColorBrush(brandNavy)
	defer navy.Dispose()
	canvas.FillRectanglePixels(navy, walk.Rectangle{Width: b.Width, Height: s(250)})
	tile, _ := walk.NewSolidColorBrush(brandNavyLight)
	defer tile.Dispose()

	sp.mu.Lock()
	running, p, through, errText := sp.running, sp.progress, sp.through, sp.err
	var res *speedtest.Result
	if sp.result != nil {
		r := *sp.result
		res = &r
	}
	sp.mu.Unlock()
	if !running && sp.vpn != nil && res == nil {
		through = sp.vpn()
	}

	canvas.DrawTextPixels(l18n.Sprintf("Speed test"), sp.fonts["title"], brandWhite, walk.Rectangle{X: cx, Y: s(16), Width: cw, Height: s(28)}, walk.TextLeft|walk.TextSingleLine)
	where := l18n.Sprintf("Your internet connection (VPN off)")
	if through != "" {
		where = l18n.Sprintf("Through the VPN: %s", displayName(through))
	}
	canvas.DrawTextPixels(where, sp.fonts["sub"], brandCyanText, walk.Rectangle{X: cx, Y: s(44), Width: cw, Height: s(20)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)

	// The big live number.
	phase, big, unit := "", "", ""
	switch {
	case running && p.Phase == speedtest.PhaseLatency:
		phase, big, unit = l18n.Sprintf("Measuring ping…"), ms(p.Result.PingMs), "ms"
	case running && p.Phase == speedtest.PhaseDownload:
		phase, big, unit = l18n.Sprintf("↓ Download"), mbps(p.Mbps), "Mbps"
	case running && p.Phase == speedtest.PhaseUpload:
		phase, big, unit = l18n.Sprintf("↑ Upload"), mbps(p.Mbps), "Mbps"
	case res != nil:
		phase, big, unit = l18n.Sprintf("↓ Download"), mbps(res.DownMbps), "Mbps"
	default:
		phase, big, unit = l18n.Sprintf("Press Start to test your speed"), "—", ""
	}
	canvas.DrawTextPixels(phase, sp.fonts["sub"], brandMuted, walk.Rectangle{X: cx, Y: s(80), Width: cw, Height: s(20)}, walk.TextCenter|walk.TextSingleLine)
	canvas.DrawTextPixels(big, sp.fonts["big"], brandWhite, walk.Rectangle{X: cx, Y: s(100), Width: cw, Height: s(56)}, walk.TextCenter|walk.TextSingleLine)
	canvas.DrawTextPixels(unit, sp.fonts["unit"], brandCyanText, walk.Rectangle{X: cx, Y: s(154), Width: cw, Height: s(20)}, walk.TextCenter|walk.TextSingleLine)

	// Progress bar.
	bar := walk.Rectangle{X: cx, Y: s(186), Width: cw, Height: s(6)}
	canvas.FillRoundedRectanglePixels(tile, bar, walk.Size{Width: s(6), Height: s(6)})
	if running && p.Fraction > 0 {
		cyan, _ := walk.NewSolidColorBrush(brandCyan)
		done := bar
		done.Width = max(s(6), int(float64(bar.Width)*p.Fraction))
		canvas.FillRoundedRectanglePixels(cyan, done, walk.Size{Width: s(6), Height: s(6)})
		cyan.Dispose()
	}

	// The button.
	label, fill, ink := l18n.Sprintf("Start speed test"), brandCyan, brandNavy
	if running {
		label, fill = l18n.Sprintf("Stop"), brandWhite
	} else if res != nil || errText != "" {
		label = l18n.Sprintf("Test again")
	}
	sp.buttonRect = walk.Rectangle{X: cx + cw/4, Y: s(204), Width: cw / 2, Height: s(36)}
	btn, _ := walk.NewSolidColorBrush(fill)
	canvas.FillRoundedRectanglePixels(btn, sp.buttonRect, walk.Size{Width: s(10), Height: s(10)})
	btn.Dispose()
	canvas.DrawTextPixels(label, sp.fonts["button"], ink, sp.buttonRect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)

	// Four result tiles.
	r := p.Result
	if !running && res != nil {
		r = *res
	}
	down, up := r.DownMbps, r.UpMbps
	if running && p.Phase == speedtest.PhaseDownload {
		down = p.Mbps
	}
	if running && p.Phase == speedtest.PhaseUpload {
		up = p.Mbps
	}
	tiles := [][3]string{
		{l18n.Sprintf("Download"), mbps(down), "Mbps"},
		{l18n.Sprintf("Upload"), mbps(up), "Mbps"},
		{l18n.Sprintf("Ping"), ms(r.PingMs), "ms"},
		{l18n.Sprintf("Jitter"), ms(r.JitterMs), "ms"},
	}
	white, _ := walk.NewSolidColorBrush(brandWhite)
	defer white.Dispose()
	border, _ := walk.NewCosmeticPen(walk.PenSolid, brandBorder)
	defer border.Dispose()
	gap := s(10)
	cols := 4
	if cw < s(400) {
		cols = 2
	}
	w := (cw - (cols-1)*gap) / cols
	for i, t := range tiles {
		rect := walk.Rectangle{X: cx + (i%cols)*(w+gap), Y: s(266) + (i/cols)*s(76), Width: w, Height: s(66)}
		canvas.FillRoundedRectanglePixels(white, rect, walk.Size{Width: s(10), Height: s(10)})
		canvas.DrawRoundedRectanglePixels(border, rect, walk.Size{Width: s(10), Height: s(10)})
		canvas.DrawTextPixels(t[0], sp.fonts["label"], brandInkMuted, walk.Rectangle{X: rect.X + s(12), Y: rect.Y + s(8), Width: rect.Width - s(16), Height: s(16)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)
		canvas.DrawTextPixels(t[1], sp.fonts["value"], brandInk, walk.Rectangle{X: rect.X + s(12), Y: rect.Y + s(26), Width: rect.Width - s(16), Height: s(26)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)
		canvas.DrawTextPixels(t[2], sp.fonts["label"], brandInkMuted, walk.Rectangle{X: rect.X + s(12), Y: rect.Y + s(48), Width: rect.Width - s(16), Height: s(14)}, walk.TextLeft|walk.TextSingleLine)
	}
	y := s(266) + ((len(tiles)+cols-1)/cols)*s(76) + s(4)

	// Details.
	var lines []string
	if r.Server != "" {
		lines = append(lines, l18n.Sprintf("Test server: Cloudflare %s", r.Server))
	}
	if r.ISP != "" || r.IP != "" {
		lines = append(lines, l18n.Sprintf("Internet provider: %s · Address: %s", r.ISP, r.IP))
	}
	if !running && res != nil {
		lines = append(lines, l18n.Sprintf("Tested %s", res.At.Format("Jan 2, 3:04 PM")))
	}
	if errText != "" {
		canvas.DrawTextPixels(errText, sp.fonts["small"], walk.RGB(0xB4, 0x23, 0x18), walk.Rectangle{X: cx, Y: y, Width: cw, Height: s(36)}, walk.TextLeft|walk.TextWordbreak|walk.TextEndEllipsis)
		y += s(38)
	}
	for _, line := range lines {
		canvas.DrawTextPixels(line, sp.fonts["small"], brandInkMuted, walk.Rectangle{X: cx, Y: y, Width: cw, Height: s(18)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)
		y += s(20)
	}
	if through != "" {
		canvas.DrawTextPixels(l18n.Sprintf("While connected, the test runs through your office's internet."), sp.fonts["small"], brandInkMuted, walk.Rectangle{X: cx, Y: y + s(4), Width: cw, Height: s(18)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)
	}
	return nil
}
