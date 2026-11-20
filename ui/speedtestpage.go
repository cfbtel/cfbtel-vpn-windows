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

	view *walk.CustomWidget
	vpn  func() string // the office the VPN is connected to, or ""

	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	progress speedtest.Progress
	result   *speedtest.Result
	through  string
	err      string

	buttonRect walk.Rectangle
	linkRect   walk.Rectangle
	hover      string
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
	if bg, err := walk.NewSolidColorBrush(darkBg); err == nil {
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
		if button != walk.LeftButton {
			return
		}
		switch {
		case hit(sp.buttonRect, x, y):
			sp.onButton()
		case hit(sp.linkRect, x, y):
			win.ShellExecute(sp.Form().Handle(), nil, windows.StringToUTF16Ptr("https://www.speedtest.net/"), nil, nil, win.SW_SHOWNORMAL)
		}
	})
	sp.view.MouseMove().Attach(func(x, y int, button walk.MouseButton) {
		h := ""
		switch {
		case hit(sp.buttonRect, x, y):
			h = "button"
		case hit(sp.linkRect, x, y):
			h = "link"
		}
		if h != "" {
			sp.view.SetCursor(walk.CursorHand())
		} else {
			sp.view.SetCursor(walk.CursorArrow())
		}
		if h != sp.hover {
			sp.hover = h
			sp.view.Invalidate()
		}
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
		case errors.Is(err, speedtest.ErrBusy):
			sp.err = err.Error()
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
	p := painter{canvas, s}
	cx, cw := column(b.Width, s)
	p.fill(darkBg, b, 0)

	sp.mu.Lock()
	running, prog, through, errText := sp.running, sp.progress, sp.through, sp.err
	var res *speedtest.Result
	if sp.result != nil {
		r := *sp.result
		res = &r
	}
	sp.mu.Unlock()
	if !running && sp.vpn != nil && res == nil {
		through = sp.vpn()
	}

	p.text(l18n.Sprintf("Speed test"), sp.fonts["title"], darkText, walk.Rectangle{X: cx, Y: s(18), Width: cw, Height: s(28)}, walk.TextLeft)
	where := l18n.Sprintf("Your own internet connection (VPN off)")
	if through != "" {
		where = l18n.Sprintf("Through the VPN: %s", displayName(through))
	}
	p.text(where, sp.fonts["sub"], darkText2, walk.Rectangle{X: cx, Y: s(46), Width: cw, Height: s(20)}, walk.TextLeft)

	// The live reading, in a card.
	card := walk.Rectangle{X: cx, Y: s(80), Width: cw, Height: s(212)}
	p.fill(darkCard, card, 18)
	border := darkLine
	if running {
		border = brandCyan
	}
	p.outline(border, 1, card, 18)
	phase, big, unit, bigColor := "", "", "", darkText
	switch {
	case running && prog.Phase == speedtest.PhaseLatency:
		phase, big, unit = l18n.Sprintf("Measuring ping"), ms(prog.Result.PingMs), "ms"
	case running && prog.Phase == speedtest.PhaseDownload:
		phase, big, unit, bigColor = l18n.Sprintf("Download"), mbps(prog.Mbps), "Mbps", darkDown
	case running && prog.Phase == speedtest.PhaseUpload:
		phase, big, unit, bigColor = l18n.Sprintf("Upload"), mbps(prog.Mbps), "Mbps", darkUp
	case res != nil:
		phase, big, unit, bigColor = l18n.Sprintf("Download"), mbps(res.DownMbps), "Mbps", darkDown
	default:
		phase, big, unit = l18n.Sprintf("Press Start to test your speed"), "—", ""
	}
	p.text(phase, sp.fonts["sub"], darkText3, walk.Rectangle{X: card.X, Y: card.Y + s(18), Width: card.Width, Height: s(20)}, walk.TextCenter)
	p.text(big, sp.fonts["big"], bigColor, walk.Rectangle{X: card.X, Y: card.Y + s(38), Width: card.Width, Height: s(56)}, walk.TextCenter|walk.TextVCenter)
	p.text(unit, sp.fonts["unit"], darkText2, walk.Rectangle{X: card.X, Y: card.Y + s(94), Width: card.Width, Height: s(20)}, walk.TextCenter)

	bar := walk.Rectangle{X: card.X + s(18), Y: card.Y + s(126), Width: card.Width - s(36), Height: s(6)}
	p.fill(darkTile, bar, 6)
	if running && prog.Fraction > 0 {
		done := bar
		done.Width = max(s(6), int(float64(bar.Width)*prog.Fraction))
		p.fill(brandCyan, done, 6)
	}

	label, fill, ink := l18n.Sprintf("Start speed test"), brandCyan, darkBg
	if sp.hover == "button" {
		fill = darkCyanHi
	}
	if running {
		label, fill, ink = l18n.Sprintf("Stop"), darkSlate, darkText
		if sp.hover == "button" {
			fill = darkSlateHi
		}
	} else if res != nil || errText != "" {
		label = l18n.Sprintf("Test again")
	}
	sp.buttonRect = walk.Rectangle{X: card.X + s(18), Y: card.Y + s(148), Width: card.Width - s(36), Height: s(46)}
	p.fill(fill, sp.buttonRect, 10)
	p.text(label, sp.fonts["button"], ink, sp.buttonRect, walk.TextCenter|walk.TextVCenter)

	// Results: download, upload, ping, jitter.
	r := prog.Result
	if !running && res != nil {
		r = *res
	}
	down, up := r.DownMbps, r.UpMbps
	if running && prog.Phase == speedtest.PhaseDownload {
		down = prog.Mbps
	}
	if running && prog.Phase == speedtest.PhaseUpload {
		up = prog.Mbps
	}
	tiles := []struct {
		label, value, unit string
		color              walk.Color
	}{
		{l18n.Sprintf("Download"), mbps(down), "Mbps", darkDown},
		{l18n.Sprintf("Upload"), mbps(up), "Mbps", darkUp},
		{l18n.Sprintf("Ping"), ms(r.PingMs), "ms", darkText},
		{l18n.Sprintf("Jitter"), ms(r.JitterMs), "ms", darkText},
	}
	gap := s(10)
	cols := 4
	if cw < s(400) {
		cols = 2
	}
	w := (cw - (cols-1)*gap) / cols
	for i, t := range tiles {
		rect := walk.Rectangle{X: cx + (i%cols)*(w+gap), Y: s(306) + (i/cols)*s(74), Width: w, Height: s(64)}
		p.fill(darkCard, rect, 12)
		p.text(t.label, sp.fonts["label"], darkText3, walk.Rectangle{X: rect.X + s(14), Y: rect.Y + s(9), Width: rect.Width - s(20), Height: s(16)}, walk.TextLeft)
		vw := p.textWidth(t.value, sp.fonts["value"])
		p.text(t.value, sp.fonts["value"], t.color, walk.Rectangle{X: rect.X + s(14), Y: rect.Y + s(28), Width: rect.Width - s(20), Height: s(26)}, walk.TextLeft)
		p.text(t.unit, sp.fonts["label"], darkText3, walk.Rectangle{X: rect.X + s(14) + vw + s(5), Y: rect.Y + s(34), Width: max(0, rect.Width-s(24)-vw), Height: s(18)}, walk.TextLeft)
	}
	y := s(306) + ((len(tiles)+cols-1)/cols)*s(74) + s(6)

	if errText != "" {
		canvas.DrawTextPixels(errText, sp.fonts["small"], walk.RGB(0xFF, 0x8A, 0x80), walk.Rectangle{X: cx, Y: y, Width: cw, Height: s(38)}, walk.TextLeft|walk.TextWordbreak|walk.TextEndEllipsis|walk.TextNoPrefix)
		y += s(40)
	}
	var lines []string
	if r.Server != "" {
		lines = append(lines, l18n.Sprintf("Test server: Cloudflare %s", r.Server))
	}
	if r.ISP != "" || r.IP != "" {
		lines = append(lines, l18n.Sprintf("Provider: %s · %s", r.ISP, r.IP))
	}
	if !running && res != nil {
		lines = append(lines, l18n.Sprintf("Tested %s", res.At.Format("Jan 2, 3:04 PM")))
	}
	for _, line := range lines {
		p.text(line, sp.fonts["small"], darkText2, walk.Rectangle{X: cx, Y: y, Width: cw, Height: s(18)}, walk.TextLeft)
		y += s(20)
	}

	// Footer: where it's measured, and speedtest.net.
	fy := b.Height - s(30)
	p.line(darkLine, 1, walk.PenSolid, walk.Point{X: 0, Y: fy - s(4)}, walk.Point{X: b.Width, Y: fy - s(4)})
	link := l18n.Sprintf("Open speedtest.net")
	lw := p.textWidth(link, sp.fonts["small"])
	sp.linkRect = walk.Rectangle{X: cx + cw - lw, Y: fy, Width: lw, Height: s(26)}
	lc := darkLinkText
	if sp.hover == "link" {
		lc = darkText
	}
	p.text(link, sp.fonts["small"], lc, sp.linkRect, walk.TextLeft|walk.TextVCenter)
	p.text(l18n.Sprintf("Measured on Cloudflare's speed test network"), sp.fonts["small"], darkText3, walk.Rectangle{X: cx, Y: fy, Width: cw - lw - s(10), Height: s(26)}, walk.TextLeft|walk.TextVCenter)
	return nil
}
