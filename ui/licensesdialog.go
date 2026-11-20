/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2026 CFBTEL. All Rights Reserved.
 */

package ui

import (
	_ "embed"
	"strings"

	"github.com/lxn/walk"
	"github.com/lxn/win"

	"golang.zx2c4.com/wireguard/windows/l18n"
)

// notices.txt: CFBTEL copyright and the licenses of the open-source parts shipped in the app.
//
//go:embed notices.txt
var noticesText string

func runLicensesDialog(owner walk.Form) error {
	dlg, err := walk.NewDialog(owner)
	if err != nil {
		return err
	}
	defer dlg.Dispose()
	dlg.SetTitle(l18n.Sprintf("CFBTEL VPN – Open-source licenses"))
	vbl := walk.NewVBoxLayout()
	vbl.SetMargins(walk.Margins{10, 10, 10, 10})
	dlg.SetLayout(vbl)
	if icon, err := loadLogoIcon(32); err == nil {
		dlg.SetIcon(icon)
	}
	dlg.SetMinMaxSize(walk.Size{Width: 560, Height: 420}, walk.Size{})
	dlg.SetSize(walk.Size{Width: 700, Height: 560})

	te, err := walk.NewTextEditWithStyle(dlg, win.WS_VSCROLL)
	if err != nil {
		return err
	}
	if font, err := walk.NewFont("Consolas", 9, 0); err == nil {
		te.SetFont(font)
	}
	te.SetReadOnly(true)
	te.SetText(strings.ReplaceAll(noticesText, "\n", "\r\n"))

	buttons, err := walk.NewComposite(dlg)
	if err != nil {
		return err
	}
	hbl := walk.NewHBoxLayout()
	hbl.SetMargins(walk.Margins{})
	buttons.SetLayout(hbl)
	walk.NewHSpacer(buttons)
	closePB, err := walk.NewPushButton(buttons)
	if err != nil {
		return err
	}
	closePB.SetText(l18n.Sprintf("Close"))
	closePB.Clicked().Attach(dlg.Accept)
	dlg.SetDefaultButton(closePB)
	dlg.SetCancelButton(closePB)

	dlg.Run()
	return nil
}
