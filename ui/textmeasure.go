/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2026 CFBTEL. All Rights Reserved.
 */

package ui

import (
	"sync"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

// Text measuring with plain GDI. walk's Canvas.MeasureTextPixels measures through a metafile, which gives wrong
// widths inside double-buffered painting (the "More details" pill came out wider than the card, and the office
// names and footer links were pushed out of sight). These ask Windows directly, on the same device context.

type measureFontKey struct {
	family string
	pt     int
	bold   bool
	dpi    int
}

var (
	measureFontsMu sync.Mutex
	measureFonts   = map[measureFontKey]win.HFONT{}
)

func measureFont(font *walk.Font, dpi int) win.HFONT {
	key := measureFontKey{font.Family(), font.PointSize(), font.Bold(), dpi}
	measureFontsMu.Lock()
	defer measureFontsMu.Unlock()
	if h, ok := measureFonts[key]; ok {
		return h
	}
	lf := win.LOGFONT{
		LfHeight:  -win.MulDiv(int32(key.pt), int32(dpi), 72),
		LfWeight:  win.FW_NORMAL,
		LfCharSet: win.DEFAULT_CHARSET,
		LfQuality: win.CLEARTYPE_QUALITY,
	}
	if key.bold {
		lf.LfWeight = win.FW_BOLD
	}
	name, _ := windows.UTF16FromString(key.family)
	copy(lf.LfFaceName[:len(lf.LfFaceName)-1], name)
	h := win.CreateFontIndirect(&lf)
	measureFonts[key] = h
	return h
}

// textExtent is the width of one line of text in pixels.
func textExtent(c *walk.Canvas, s string, font *walk.Font) int {
	if s == "" {
		return 0
	}
	hdc := c.HDC()
	old := win.SelectObject(hdc, win.HGDIOBJ(measureFont(font, c.DPI())))
	defer win.SelectObject(hdc, old)
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return 0
	}
	var size win.SIZE
	if !win.GetTextExtentPoint32(hdc, &u[0], int32(len(u)-1), &size) {
		return 0
	}
	return int(size.CX)
}

// wrappedHeight is the height in pixels of text word-wrapped to width.
func wrappedHeight(c *walk.Canvas, s string, font *walk.Font, width int) int {
	if s == "" || width <= 0 {
		return 0
	}
	hdc := c.HDC()
	old := win.SelectObject(hdc, win.HGDIOBJ(measureFont(font, c.DPI())))
	defer win.SelectObject(hdc, old)
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return 0
	}
	rc := win.RECT{Right: int32(width), Bottom: 10000}
	win.DrawTextEx(hdc, &u[0], int32(len(u)-1), &rc, win.DT_CALCRECT|win.DT_WORDBREAK|win.DT_NOPREFIX, nil)
	return int(rc.Bottom - rc.Top)
}
