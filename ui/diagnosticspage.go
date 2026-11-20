/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2026 CFBTEL. All Rights Reserved.
 */

package ui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"golang.zx2c4.com/wireguard/windows/diag"
	"golang.zx2c4.com/wireguard/windows/l18n"
	"golang.zx2c4.com/wireguard/windows/manager"
	"golang.zx2c4.com/wireguard/windows/ringlogger"
	"golang.zx2c4.com/wireguard/windows/version"
)

const supportEmail = "help@cfbtel.com"

// DiagnosticsPage checks the connection end to end and sends a support ticket with the details and the log.
type DiagnosticsPage struct {
	*walk.TabPage

	home    *HomePage
	scroll  *walk.ScrollView
	results *walk.CustomWidget

	name, email, subject *walk.LineEdit
	message              *walk.TextEdit
	send, copyBtn, save  *walk.PushButton
	status               *walk.TextLabel

	mu      sync.Mutex
	running bool
	checks  []diag.Check
	ranAt   time.Time
	sys     diag.System
	offices []diag.Office
	report  string
	sending bool
	height  int

	buttonRect walk.Rectangle
	fonts      map[string]*walk.Font
}

func NewDiagnosticsPage(home *HomePage) (*DiagnosticsPage, error) {
	var err error
	var disposables walk.Disposables
	defer disposables.Treat()

	dp := &DiagnosticsPage{home: home, fonts: map[string]*walk.Font{}}
	if dp.TabPage, err = walk.NewTabPage(); err != nil {
		return nil, err
	}
	disposables.Add(dp)
	dp.SetTitle(l18n.Sprintf("Diagnostics"))
	layout := walk.NewVBoxLayout()
	layout.SetMargins(walk.Margins{})
	layout.SetSpacing(0)
	dp.SetLayout(layout)
	pageBrush, _ := walk.NewSolidColorBrush(brandPage)
	dp.SetBackground(pageBrush)
	for name, f := range map[string]struct {
		size  int
		style walk.FontStyle
	}{"title": {14, walk.FontBold}, "sub": {9, 0}, "group": {10, walk.FontBold}, "name": {9, walk.FontBold}, "detail": {9, 0}, "button": {10, walk.FontBold}, "heading": {11, walk.FontBold}} {
		if font, err := walk.NewFont("Segoe UI", f.size, f.style); err == nil {
			dp.fonts[name] = font
		}
	}

	if dp.scroll, err = walk.NewScrollView(dp); err != nil {
		return nil, err
	}
	// Both on, or walk squeezes the ScrollView to its narrowest width (see HomePage).
	dp.scroll.SetScrollbars(true, true)
	inner := walk.NewVBoxLayout()
	inner.SetMargins(walk.Margins{})
	inner.SetSpacing(0)
	dp.scroll.SetLayout(inner)
	dp.scroll.SetBackground(pageBrush)

	if dp.results, err = walk.NewCustomWidgetPixels(dp.scroll, 0, dp.paint); err != nil {
		return nil, err
	}
	dp.results.SetPaintMode(walk.PaintBuffered)
	dp.results.SetInvalidatesOnResize(true)
	dp.results.SetMinMaxSize(walk.Size{Height: 176}, walk.Size{Height: 176})
	dp.results.SizeChanged().Attach(func() {
		// Narrower = more wrapped lines: fit the height again (after this layout pass).
		dp.Synchronize(func() {
			if h := dp.measure(); h != dp.height {
				dp.resize()
			}
		})
	})
	dp.results.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton && hit(dp.buttonRect, x, y) {
			dp.Run(nil)
		}
	})
	dp.results.MouseMove().Attach(func(x, y int, button walk.MouseButton) {
		if hit(dp.buttonRect, x, y) {
			dp.results.SetCursor(walk.CursorHand())
		} else {
			dp.results.SetCursor(walk.CursorArrow())
		}
	})

	if err = dp.buildSupportForm(); err != nil {
		return nil, err
	}

	disposables.Spare()
	return dp, nil
}

func (dp *DiagnosticsPage) buildSupportForm() error {
	form, err := walk.NewComposite(dp.scroll)
	if err != nil {
		return err
	}
	grid := walk.NewGridLayout()
	grid.SetMargins(walk.Margins{HNear: 22, VNear: 14, HFar: 22, VFar: 18})
	grid.SetSpacing(6)
	form.SetLayout(grid)
	if bg, err := walk.NewSolidColorBrush(brandPage); err == nil {
		form.SetBackground(bg)
	}
	// Same column as the checks above: at most 560 px of content (+ margins), centred on wide windows.
	form.SetMinMaxSize(walk.Size{}, walk.Size{Width: 604})
	form.SetAlignment(walk.AlignHCenterVNear)

	row := 0
	label := func(text string, font *walk.Font, span int) *walk.TextLabel {
		l, _ := walk.NewTextLabel(form)
		l.SetText(text)
		if font != nil {
			l.SetFont(font)
		}
		grid.SetRange(l, walk.Rectangle{X: 0, Y: row, Width: span, Height: 1})
		row++
		return l
	}
	label(l18n.Sprintf("Contact CFBTEL support"), dp.fonts["heading"], 2)
	label(l18n.Sprintf("Sends your message, the checks above, this computer's connection details and the VPN log to %s. No passwords or private keys are sent.", supportEmail), nil, 2)

	field := func(caption, cue, setting string) *walk.LineEdit {
		l, _ := walk.NewTextLabel(form)
		l.SetText(caption)
		grid.SetRange(l, walk.Rectangle{X: 0, Y: row, Width: 1, Height: 1})
		e, _ := walk.NewLineEdit(form)
		e.SetCueBanner(cue)
		if setting != "" {
			e.SetText(walkSetting(setting))
		}
		grid.SetRange(e, walk.Rectangle{X: 1, Y: row, Width: 1, Height: 1})
		row++
		return e
	}
	dp.name = field(l18n.Sprintf("Your name"), l18n.Sprintf("First and last name"), "SupportName")
	dp.email = field(l18n.Sprintf("Your email"), l18n.Sprintf("So we can answer you"), "SupportEmail")
	dp.subject = field(l18n.Sprintf("Subject"), "", "")
	host, _ := os.Hostname()
	dp.subject.SetText(l18n.Sprintf("CFBTEL VPN problem on %s", host))

	l, _ := walk.NewTextLabel(form)
	l.SetText(l18n.Sprintf("What's happening?"))
	grid.SetRange(l, walk.Rectangle{X: 0, Y: row, Width: 1, Height: 1})
	dp.message, _ = walk.NewTextEditWithStyle(form, win.WS_VSCROLL)
	dp.message.SetMinMaxSize(walk.Size{Height: 90}, walk.Size{Height: 140})
	grid.SetRange(dp.message, walk.Rectangle{X: 1, Y: row, Width: 1, Height: 1})
	row++

	buttons, _ := walk.NewComposite(form)
	hb := walk.NewHBoxLayout()
	hb.SetMargins(walk.Margins{})
	buttons.SetLayout(hb)
	grid.SetRange(buttons, walk.Rectangle{X: 0, Y: row, Width: 2, Height: 1})
	row++
	dp.send, _ = walk.NewPushButton(buttons)
	dp.send.SetText(l18n.Sprintf("&Send to support"))
	dp.send.Clicked().Attach(dp.onSend)
	dp.copyBtn, _ = walk.NewPushButton(buttons)
	dp.copyBtn.SetText(l18n.Sprintf("&Copy report"))
	dp.copyBtn.Clicked().Attach(func() {
		dp.withReport(func(report string) {
			walk.Clipboard().SetText(report)
			dp.setStatus(l18n.Sprintf("The report is copied. Paste it into an email to %s.", supportEmail))
		})
	})
	dp.save, _ = walk.NewPushButton(buttons)
	dp.save.SetText(l18n.Sprintf("Sa&ve…"))
	dp.save.Clicked().Attach(dp.onSave)
	walk.NewHSpacer(buttons)

	dp.status, _ = walk.NewTextLabel(form)
	grid.SetRange(dp.status, walk.Rectangle{X: 0, Y: row, Width: 2, Height: 1})
	return nil
}

func (dp *DiagnosticsPage) setStatus(text string) {
	dp.status.SetText(text)
	dp.scroll.RequestLayout()
}

// gather reads the offices and this computer's details.
func (dp *DiagnosticsPage) gather() (diag.System, []diag.Office) {
	host, _ := os.Hostname()
	sys := diag.System{App: "CFBTEL VPN " + version.Number, Computer: host, User: os.Getenv("USERDOMAIN") + `\` + os.Getenv("USERNAME"), DeviceID: deviceID()}
	if v := windows.RtlGetVersion(); v != nil {
		sys.Windows = fmt.Sprintf("Windows %d.%d build %d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	}
	if ifaces, err := net.Interfaces(); err == nil {
		for _, i := range ifaces {
			if i.Flags&net.FlagLoopback != 0 {
				continue
			}
			it := diag.Interface{Name: i.Name, Up: i.Flags&net.FlagUp != 0}
			if addrs, err := i.Addrs(); err == nil {
				for _, a := range addrs {
					if p, err := netip.ParsePrefix(a.String()); err == nil {
						it.Addrs = append(it.Addrs, p)
					}
				}
			}
			sys.Interfaces = append(sys.Interfaces, it)
		}
	}

	var offices []diag.Office
	tunnels, _ := manager.IPCClientTunnels()
	for i := range tunnels {
		t := tunnels[i]
		o := diag.Office{Name: t.Name, State: "off", Handshake: -1}
		if st, err := t.State(); err == nil {
			switch st {
			case manager.TunnelStarted:
				o.State, o.Connected = "connected", true
			case manager.TunnelStarting:
				o.State, o.Connected = "connecting", true
			case manager.TunnelStopping:
				o.State = "disconnecting"
			}
		}
		if c, err := t.StoredConfig(); err == nil {
			o.PublicKey = c.Interface.PrivateKey.Public().String()
			if len(c.Interface.Addresses) > 0 {
				o.Address = c.Interface.Addresses[0].String()
			}
			for _, d := range c.Interface.DNS {
				o.DNS = append(o.DNS, d.String())
			}
			if len(c.Peers) > 0 {
				p := c.Peers[0]
				o.ServerKey = p.PublicKey.String()
				o.Endpoint = p.Endpoint.String()
				for _, a := range p.AllowedIPs {
					o.AllowedIPs = append(o.AllowedIPs, a.String())
				}
			}
		}
		if o.Connected {
			if c, err := t.RuntimeConfig(); err == nil && len(c.Peers) > 0 {
				p := c.Peers[0]
				o.Rx, o.Tx = p.RxBytes.String(), p.TxBytes.String()
				if !p.LastHandshakeTime.IsEmpty() {
					o.Handshake = time.Since(time.Unix(0, 0).Add(time.Duration(p.LastHandshakeTime)))
				}
			}
		}
		offices = append(offices, o)
	}
	return sys, offices
}

// deviceID is a stable ID for this computer (from Windows' machine ID, hashed).
func deviceID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "unknown"
	}
	defer k.Close()
	guid, _, err := k.GetStringValue("MachineGuid")
	if err != nil || guid == "" {
		return "unknown"
	}
	sum := sha256.Sum256([]byte("cfbtel-vpn:" + strings.ToLower(guid)))
	return "CFB-" + strings.ToUpper(hex.EncodeToString(sum[:6]))
}

func logLines(max int) []string {
	items, _ := ringlogger.Global.FollowFromCursor(ringlogger.CursorAll)
	if len(items) > max {
		items = items[len(items)-max:]
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Stamp.Format("2006-01-02 15:04:05.000")+": "+it.Line)
	}
	return out
}

// Run does the checks; done (may be nil) runs on the UI thread afterwards.
func (dp *DiagnosticsPage) Run(done func()) {
	dp.mu.Lock()
	if dp.running {
		dp.mu.Unlock()
		return
	}
	dp.running, dp.checks = true, nil
	dp.mu.Unlock()
	dp.resize()
	go func() {
		sys, offices := dp.gather()
		base, _ := portal()
		env := diag.DefaultEnv(base, diag.Ping)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		checks := diag.Run(ctx, env, sys, offices, func(c diag.Check) {
			dp.mu.Lock()
			dp.checks = append(dp.checks, c)
			dp.mu.Unlock()
			dp.Synchronize(dp.resize)
		})
		now := time.Now()
		report := diag.Report(sys, offices, checks, logLines(400), now)
		dp.mu.Lock()
		dp.running, dp.checks, dp.sys, dp.offices, dp.report, dp.ranAt = false, checks, sys, offices, report, now
		dp.mu.Unlock()
		dp.Synchronize(func() {
			dp.resize()
			if done != nil {
				done()
			}
		})
	}()
}

// withReport runs the checks first if they haven't run in the last 10 minutes.
func (dp *DiagnosticsPage) withReport(f func(report string)) {
	dp.mu.Lock()
	report, fresh := dp.report, time.Since(dp.ranAt) < 10*time.Minute
	dp.mu.Unlock()
	if report != "" && fresh {
		f(report)
		return
	}
	dp.setStatus(l18n.Sprintf("Running the checks first…"))
	dp.Run(func() {
		dp.mu.Lock()
		report := dp.report
		dp.mu.Unlock()
		f(report)
	})
}

type supportRequest struct {
	PublicKeys []string `json:"public_keys"`
	DeviceID   string   `json:"device_id"`
	Computer   string   `json:"computer"`
	App        string   `json:"app"`
	Name       string   `json:"contact_name"`
	Email      string   `json:"contact_email"`
	Subject    string   `json:"subject"`
	Message    string   `json:"message"`
	Problems   int      `json:"problems"`
	Warnings   int      `json:"warnings"`
	Report     string   `json:"report"`
}

func (dp *DiagnosticsPage) onSend() {
	name, email := strings.TrimSpace(dp.name.Text()), strings.TrimSpace(dp.email.Text())
	subject, message := strings.TrimSpace(dp.subject.Text()), strings.TrimSpace(dp.message.Text())
	if !strings.Contains(email, "@") || strings.ContainsAny(email, " <>") {
		dp.setStatus(l18n.Sprintf("Enter your email address so CFBTEL can answer you."))
		dp.email.SetFocus()
		return
	}
	if message == "" {
		dp.setStatus(l18n.Sprintf("Please write a few words about what's happening."))
		dp.message.SetFocus()
		return
	}
	if subject == "" {
		subject = l18n.Sprintf("CFBTEL VPN problem")
	}
	saveWalkSetting("SupportName", name)
	saveWalkSetting("SupportEmail", email)
	dp.mu.Lock()
	if dp.sending {
		dp.mu.Unlock()
		return
	}
	dp.sending = true
	dp.mu.Unlock()
	dp.send.SetEnabled(false)

	dp.withReport(func(report string) {
		dp.setStatus(l18n.Sprintf("Sending…"))
		dp.mu.Lock()
		sys, offices, checks := dp.sys, dp.offices, dp.checks
		dp.mu.Unlock()
		req := supportRequest{DeviceID: sys.DeviceID, Computer: sys.Computer, App: sys.App, Name: name, Email: email, Subject: subject, Message: message, Report: report}
		req.Problems, req.Warnings = diag.Summary(checks)
		for _, o := range offices {
			if o.PublicKey != "" {
				req.PublicKeys = append(req.PublicKeys, o.PublicKey)
			}
		}
		go func() {
			ref, emailed, err := postSupport(req)
			dp.Synchronize(func() {
				dp.mu.Lock()
				dp.sending = false
				dp.mu.Unlock()
				dp.send.SetEnabled(true)
				switch {
				case err == nil && emailed:
					dp.message.SetText("")
					dp.setStatus(l18n.Sprintf("Sent to CFBTEL support. Your reference is %s — we'll answer at %s.", ref, email))
				case err == nil:
					// Saved at CFBTEL, but its email didn't go out: send it by email too.
					req.Subject = "[" + ref + "] " + req.Subject
					dp.emailFallback(req, errors.New(l18n.Sprintf("saved as %s, but CFBTEL couldn't email it", ref)))
				default:
					dp.emailFallback(req, err)
				}
			})
		}()
	})
}

func postSupport(req supportRequest) (ref string, emailed bool, err error) {
	base, err := portal()
	if err != nil {
		return "", false, err
	}
	if len(req.PublicKeys) == 0 {
		return "", false, errors.New(l18n.Sprintf("this computer has no office set up yet"))
	}
	body, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/vpn/app/support", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}).Do(r)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	var out struct {
		Reference string `json:"reference"`
		Emailed   bool   `json:"emailed"`
		Error     string `json:"error"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out)
	if resp.StatusCode != http.StatusOK || out.Reference == "" {
		if out.Error != "" {
			return "", false, errors.New(out.Error)
		}
		return "", false, fmt.Errorf("CFBTEL answered %s", resp.Status)
	}
	return out.Reference, out.Emailed, nil
}

// emailFallback: copy the report, save it as a file and open an email to support.
func (dp *DiagnosticsPage) emailFallback(req supportRequest, cause error) {
	walk.Clipboard().SetText(req.Report)
	path := saveReportFile(req.Report)
	body := req.Message + "\n\n" + l18n.Sprintf("Name: %s\nEmail: %s\nComputer: %s\nDevice ID: %s\n", req.Name, req.Email, req.Computer, req.DeviceID)
	body += "\n" + l18n.Sprintf("The full diagnostic report is on the clipboard: click here and press Ctrl+V.")
	if path != "" {
		body += " " + l18n.Sprintf("It is also saved at %s if you'd rather attach it.", path)
	}
	mailto := "mailto:" + supportEmail + "?subject=" + url.PathEscape(req.Subject) + "&body=" + url.PathEscape(body)
	win.ShellExecute(dp.Form().Handle(), nil, windows.StringToUTF16Ptr(mailto), nil, nil, win.SW_SHOWNORMAL)
	dp.setStatus(l18n.Sprintf("Couldn't send it directly (%v). An email to %s is open: paste the report (Ctrl+V) and press Send.", cause, supportEmail))
}

func saveReportFile(report string) string {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_Documents, 0)
	if err != nil {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, "CFBTEL VPN report "+time.Now().Format("2006-01-02 1504")+".txt")
	if os.WriteFile(path, []byte(strings.ReplaceAll(report, "\n", "\r\n")), 0o644) != nil {
		return ""
	}
	return path
}

func (dp *DiagnosticsPage) onSave() {
	dp.withReport(func(report string) {
		fd := walk.FileDialog{
			Filter:   l18n.Sprintf("Text files (*.txt)|*.txt|All files (*.*)|*.*"),
			FilePath: "CFBTEL VPN report " + time.Now().Format("2006-01-02 1504") + ".txt",
			Title:    l18n.Sprintf("Save the diagnostic report"),
		}
		if ok, _ := fd.ShowSave(dp.Form()); !ok {
			return
		}
		path := fd.FilePath
		if !strings.HasSuffix(strings.ToLower(path), ".txt") {
			path += ".txt"
		}
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(report, "\n", "\r\n")), 0o644); err != nil {
			dp.setStatus(l18n.Sprintf("Couldn't save: %v", err))
			return
		}
		dp.setStatus(l18n.Sprintf("Saved to %s", path))
	})
}

// resize fits the results widget to its content and repaints it.
func (dp *DiagnosticsPage) resize() {
	h := dp.measure()
	if h != dp.height {
		dp.height = h
		dp.results.SetMinMaxSize(walk.Size{Height: h}, walk.Size{Height: h})
		dp.scroll.RequestLayout()
	}
	dp.results.Invalidate()
}

func statusColor(s diag.Status) walk.Color {
	switch s {
	case diag.OK:
		return brandGreen
	case diag.Warn:
		return brandAmber
	case diag.Fail:
		return walk.RGB(0xD9, 0x3B, 0x2F)
	}
	return brandGrey
}

// checkLine is where one group heading or check goes, in pixels.
type checkLine struct {
	group     string
	check     *diag.Check
	y, height int
}

func (dp *DiagnosticsPage) measure() int {
	canvas, err := dp.results.CreateCanvas()
	if err != nil {
		return dp.results.IntFrom96DPI(176)
	}
	defer canvas.Dispose()
	_, h := dp.layout(canvas, dp.results.ClientBoundsPixels().Width)
	return dp.results.IntTo96DPI(h)
}

// layout places the checks under the header, measuring how many lines each detail wraps to.
func (dp *DiagnosticsPage) layout(canvas *walk.Canvas, width int) ([]checkLine, int) {
	s := func(v int) int { return dp.results.IntFrom96DPI(v) }
	if width < s(200) {
		width = s(200)
	}
	_, cw := column(width, s)
	dp.mu.Lock()
	checks := append([]diag.Check(nil), dp.checks...)
	running := dp.running
	dp.mu.Unlock()

	var lines []checkLine
	y := s(176)
	group := ""
	for i := range checks {
		c := &checks[i]
		if c.Group != group {
			group = c.Group
			y += s(8)
			lines = append(lines, checkLine{group: displayName(group), y: y, height: s(22)})
			y += s(24)
		}
		dh := s(16)
		if r, _, err := canvas.MeasureTextPixels(c.Detail, dp.fonts["detail"], walk.Rectangle{Width: cw - s(22), Height: s(400)}, walk.TextLeft|walk.TextWordbreak); err == nil && r.Height > 0 {
			dh = r.Height
		}
		h := s(20) + dh + s(8)
		lines = append(lines, checkLine{check: c, y: y, height: h})
		y += h
	}
	if running {
		y += s(26)
	}
	return lines, y + s(8)
}

func (dp *DiagnosticsPage) paint(canvas *walk.Canvas, _ walk.Rectangle) error {
	b := dp.results.ClientBoundsPixels()
	s := func(v int) int { return dp.results.IntFrom96DPI(v) }
	cx, cw := column(b.Width, s)

	page, _ := walk.NewSolidColorBrush(brandPage)
	defer page.Dispose()
	canvas.FillRectanglePixels(page, b)
	navy, _ := walk.NewSolidColorBrush(brandNavy)
	defer navy.Dispose()
	canvas.FillRectanglePixels(navy, walk.Rectangle{Width: b.Width, Height: s(160)})

	dp.mu.Lock()
	running, checks, ranAt := dp.running, len(dp.checks), dp.ranAt
	dp.mu.Unlock()
	problems, warnings := 0, 0
	dp.mu.Lock()
	problems, warnings = diag.Summary(dp.checks)
	dp.mu.Unlock()

	canvas.DrawTextPixels(l18n.Sprintf("Diagnostics"), dp.fonts["title"], brandWhite, walk.Rectangle{X: cx, Y: s(16), Width: cw, Height: s(28)}, walk.TextLeft|walk.TextSingleLine)
	sub := l18n.Sprintf("Checks this computer's internet, the office firewall and the VPN connection.")
	switch {
	case running:
		sub = l18n.Sprintf("Checking… (%d done)", checks)
	case !ranAt.IsZero() && problems == 0 && warnings == 0:
		sub = l18n.Sprintf("Everything looks fine (checked %s).", ranAt.Format("3:04 PM"))
	case !ranAt.IsZero():
		sub = l18n.Sprintf("%d problem(s), %d warning(s) — checked %s. Send the report to support below.", problems, warnings, ranAt.Format("3:04 PM"))
	}
	canvas.DrawTextPixels(sub, dp.fonts["sub"], brandCyanText, walk.Rectangle{X: cx, Y: s(46), Width: cw, Height: s(38)}, walk.TextLeft|walk.TextWordbreak|walk.TextEndEllipsis)

	label, fill, ink := l18n.Sprintf("Run checks"), brandCyan, brandNavy
	if running {
		label, fill, ink = l18n.Sprintf("Checking…"), brandNavyLight, brandMuted
	} else if !ranAt.IsZero() {
		label = l18n.Sprintf("Run checks again")
	}
	dp.buttonRect = walk.Rectangle{X: cx, Y: s(98), Width: min(cw, s(220)), Height: s(40)}
	btn, _ := walk.NewSolidColorBrush(fill)
	canvas.FillRoundedRectanglePixels(btn, dp.buttonRect, walk.Size{Width: s(10), Height: s(10)})
	btn.Dispose()
	canvas.DrawTextPixels(label, dp.fonts["button"], ink, dp.buttonRect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)

	lines, _ := dp.layout(canvas, b.Width)
	for _, l := range lines {
		y := l.y
		if l.check == nil {
			canvas.DrawTextPixels(l.group, dp.fonts["group"], brandInk, walk.Rectangle{X: cx, Y: y, Width: cw, Height: s(22)}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
			continue
		}
		c := l.check
		dot, _ := walk.NewSolidColorBrush(statusColor(c.Status))
		canvas.FillEllipsePixels(dot, walk.Rectangle{X: cx + s(2), Y: y + s(5), Width: s(10), Height: s(10)})
		dot.Dispose()
		canvas.DrawTextPixels(c.Name, dp.fonts["name"], brandInk, walk.Rectangle{X: cx + s(22), Y: y, Width: cw - s(22), Height: s(20)}, walk.TextLeft|walk.TextSingleLine|walk.TextEndEllipsis)
		canvas.DrawTextPixels(c.Detail, dp.fonts["detail"], brandInkMuted, walk.Rectangle{X: cx + s(22), Y: y + s(20), Width: cw - s(22), Height: l.height - s(20)}, walk.TextLeft|walk.TextWordbreak|walk.TextEndEllipsis)
	}
	if running {
		last := s(176)
		if len(lines) > 0 {
			ll := lines[len(lines)-1]
			last = ll.y + ll.height
		}
		canvas.DrawTextPixels(l18n.Sprintf("Checking…"), dp.fonts["detail"], brandInkMuted, walk.Rectangle{X: cx, Y: last + s(4), Width: cw, Height: s(20)}, walk.TextLeft|walk.TextSingleLine)
	}
	return nil
}
