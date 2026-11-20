/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2019-2026 WireGuard LLC. All Rights Reserved.
 */

package ui

import (
	"sync"
	"time"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"

	"golang.zx2c4.com/wireguard/windows/l18n"
	"golang.zx2c4.com/wireguard/windows/manager"
)

type ManageTunnelsWindow struct {
	walk.FormBase

	tabs        *walk.TabWidget
	homePage    *HomePage
	speedPage   *SpeedTestPage
	diagPage    *DiagnosticsPage
	tunnelsPage *TunnelsPage
	logPage     *LogPage
	updatePage  *UpdatePage
	tray        *Tray

	tunnelChangedCB *manager.TunnelChangeCallback
}

const (
	manageWindowWindowClass = "CFBTEL VPN UI - Manage Tunnels"
	raiseMsg                = win.WM_USER + 0x3510
	aboutWireGuardCmd       = 0x37
)

var taskbarButtonCreatedMsg uint32

// The window's one size (96 DPI); it can't be resized.
var manageWindowSize = walk.Size{Width: 460, Height: 760}

var initedManageTunnels sync.Once

func NewManageTunnelsWindow() (*ManageTunnelsWindow, error) {
	initedManageTunnels.Do(func() {
		walk.AppendToWalkInit(func() {
			walk.MustRegisterWindowClass(manageWindowWindowClass)
			taskbarButtonCreatedMsg = win.RegisterWindowMessage(windows.StringToUTF16Ptr("TaskbarButtonCreated"))
		})
	})

	var err error
	var disposables walk.Disposables
	defer disposables.Treat()

	font, err := walk.NewFont("Segoe UI", 9, 0)
	if err != nil {
		return nil, err
	}

	mtw := new(ManageTunnelsWindow)
	mtw.SetName("CFBTEL VPN")

	// Fixed size, like a phone-style VPN client: no resizing, no maximize.
	err = walk.InitWindow(mtw, nil, manageWindowWindowClass, win.WS_OVERLAPPEDWINDOW&^(win.WS_THICKFRAME|win.WS_MAXIMIZEBOX), win.WS_EX_CONTROLPARENT)
	if err != nil {
		return nil, err
	}
	disposables.Add(mtw)
	win.ChangeWindowMessageFilterEx(mtw.Handle(), raiseMsg, win.MSGFLT_ALLOW, nil)
	mtw.SetPersistent(true)

	if icon, err := loadLogoIcon(32); err == nil {
		mtw.SetIcon(icon)
	}
	mtw.SetTitle("CFBTEL VPN")
	mtw.SetFont(font)
	mtw.SetMinMaxSize(manageWindowSize, manageWindowSize)
	mtw.SetSize(manageWindowSize)
	vlayout := walk.NewVBoxLayout()
	vlayout.SetMargins(walk.Margins{5, 5, 5, 5})
	mtw.SetLayout(vlayout)
	mtw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		// "Close to tray" instead of exiting application
		*canceled = true
		if !noTrayAvailable {
			mtw.Hide()
		} else {
			win.ShowWindow(mtw.Handle(), win.SW_MINIMIZE)
		}
	})

	if mtw.tabs, err = walk.NewTabWidget(mtw); err != nil {
		return nil, err
	}

	if mtw.homePage, err = NewHomePage(); err != nil {
		return nil, err
	}
	mtw.tabs.Pages().Add(mtw.homePage.TabPage)

	if mtw.speedPage, err = NewSpeedTestPage(mtw.homePage.connected); err != nil {
		return nil, err
	}
	mtw.tabs.Pages().Add(mtw.speedPage.TabPage)

	if mtw.diagPage, err = NewDiagnosticsPage(mtw.homePage); err != nil {
		return nil, err
	}
	mtw.tabs.Pages().Add(mtw.diagPage.TabPage)
	mtw.homePage.OnInstalling = func() {
		// The installer is about to replace the app: close the window (the tray says what's happening).
		mtw.Hide()
		if mtw.tray != nil {
			if icon, err := loadLogoIcon(128); err == nil {
				mtw.tray.ShowCustom(l18n.Sprintf("Updating CFBTEL VPN"), l18n.Sprintf("CFBTEL VPN is installing an update and opens again in about a minute."), icon)
			}
		}
	}
	// After an update the app starts again: reconnect the offices that were on.
	time.AfterFunc(3*time.Second, func() { mtw.Synchronize(mtw.homePage.reconnectAfterUpdate) })
	mtw.homePage.OpenDiagnostics = func() {
		mtw.tabs.SetCurrentIndex(mtw.tabs.Pages().Index(mtw.diagPage.TabPage))
		mtw.diagPage.Run(nil)
	}

	if mtw.tunnelsPage, err = NewTunnelsPage(); err != nil {
		return nil, err
	}
	mtw.tabs.Pages().Add(mtw.tunnelsPage.TabPage)
	mtw.tunnelsPage.CreateToolbar()

	if mtw.logPage, err = NewLogPage(); err != nil {
		return nil, err
	}
	mtw.tabs.Pages().Add(mtw.logPage.TabPage)

	mtw.VisibleChanged().Attach(func() {
		if mtw.Visible() {
			mtw.tunnelsPage.updateConfView()
			win.SetForegroundWindow(mtw.Handle())
			win.BringWindowToTop(mtw.Handle())
			mtw.logPage.scrollToBottom()
		}
	})

	mtw.tunnelChangedCB = manager.IPCClientRegisterTunnelChange(mtw.onTunnelChange)
	globalState, _ := manager.IPCClientGlobalState()
	mtw.onTunnelChange(nil, manager.TunnelUnknown, globalState, nil)

	systemMenu := win.GetSystemMenu(mtw.Handle(), false)
	if systemMenu != 0 {
		win.InsertMenuItem(systemMenu, 0, true, &win.MENUITEMINFO{
			CbSize:     uint32(unsafe.Sizeof(win.MENUITEMINFO{})),
			FMask:      win.MIIM_ID | win.MIIM_STRING | win.MIIM_FTYPE,
			FType:      win.MIIM_STRING,
			DwTypeData: windows.StringToUTF16Ptr(l18n.Sprintf("&About CFBTEL VPN…")),
			WID:        uint32(aboutWireGuardCmd),
		})
		win.InsertMenuItem(systemMenu, 1, true, &win.MENUITEMINFO{
			CbSize: uint32(unsafe.Sizeof(win.MENUITEMINFO{})),
			FMask:  win.MIIM_TYPE,
			FType:  win.MFT_SEPARATOR,
		})
	}

	disposables.Spare()

	return mtw, nil
}

func (mtw *ManageTunnelsWindow) Dispose() {
	if mtw.tunnelChangedCB != nil {
		mtw.tunnelChangedCB.Unregister()
		mtw.tunnelChangedCB = nil
	}
	mtw.FormBase.Dispose()
}

func (mtw *ManageTunnelsWindow) updateProgressIndicator(globalState manager.TunnelState) {
	pi := mtw.ProgressIndicator()
	if pi == nil {
		return
	}
	switch globalState {
	case manager.TunnelStopping, manager.TunnelStarting:
		pi.SetState(walk.PIIndeterminate)
	default:
		pi.SetState(walk.PINoProgress)
	}
	if icon, err := iconForState(globalState, 16); err == nil {
		if globalState == manager.TunnelStopped {
			icon = nil
		}
		pi.SetOverlayIcon(icon, textForState(globalState, false))
	}
}

func (mtw *ManageTunnelsWindow) onTunnelChange(tunnel *manager.Tunnel, state, globalState manager.TunnelState, err error) {
	mtw.Synchronize(func() {
		mtw.updateProgressIndicator(globalState)

		if err != nil && mtw.Visible() {
			errMsg := err.Error()
			if len(errMsg) > 0 && errMsg[len(errMsg)-1] != '.' {
				errMsg += "."
			}
			showWarningCustom(mtw, l18n.Sprintf("Tunnel Error"), l18n.Sprintf("%s\n\nPlease consult the log for more information.", errMsg))
		}
	})
}

func (mtw *ManageTunnelsWindow) UpdateFound() {
	if mtw.homePage != nil {
		mtw.homePage.UpdateFound()
	}
	if mtw.updatePage != nil {
		return
	}
	// No separate "Update" tab: Home's footer and the tray offer it, always with the warning first.
}

func (mtw *ManageTunnelsWindow) WndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case win.WM_QUERYENDSESSION:
		if lParam == win.ENDSESSION_CLOSEAPP {
			return win.TRUE
		}
	case win.WM_ENDSESSION:
		if lParam == win.ENDSESSION_CLOSEAPP && wParam == 1 {
			walk.App().Exit(198)
		}
	case win.WM_SYSCOMMAND:
		if wParam == aboutWireGuardCmd {
			onAbout(mtw)
			return 0
		}
	case raiseMsg:
		if mtw.tunnelsPage == nil || mtw.tabs == nil {
			mtw.Synchronize(func() {
				mtw.SendMessage(msg, wParam, lParam)
			})
			return 0
		}
		if !mtw.Visible() {
			mtw.tunnelsPage.listView.SelectFirstActiveTunnel()
			mtw.tabs.SetCurrentIndex(0) // Home
		}
		raise(mtw.Handle())
		return 0
	case taskbarButtonCreatedMsg:
		ret := mtw.FormBase.WndProc(hwnd, msg, wParam, lParam)
		go func() {
			globalState, err := manager.IPCClientGlobalState()
			if err == nil {
				mtw.Synchronize(func() {
					mtw.updateProgressIndicator(globalState)
				})
			}
		}()
		return ret
	}

	return mtw.FormBase.WndProc(hwnd, msg, wParam, lParam)
}
