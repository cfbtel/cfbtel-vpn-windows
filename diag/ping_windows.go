/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2026 CFBTEL. All Rights Reserved.
 */

package diag

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi            = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreateFile  = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpCloseHandle = iphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho    = iphlpapi.NewProc("IcmpSendEcho")
)

// Ping sends one ICMP echo (IPv4) without needing administrator rights.
func Ping(ctx context.Context, ip netip.Addr) (time.Duration, error) {
	if !ip.Is4() {
		return 0, errors.New("only IPv4 can be pinged")
	}
	wait := 3 * time.Second
	if d, ok := ctx.Deadline(); ok {
		wait = time.Until(d)
	}
	if wait < 200*time.Millisecond {
		wait = 200 * time.Millisecond
	}
	h, _, err := procIcmpCreateFile.Call()
	if windows.Handle(h) == windows.InvalidHandle {
		return 0, err
	}
	defer procIcmpCloseHandle.Call(h)
	a := ip.As4()
	dest := *(*uint32)(unsafe.Pointer(&a[0])) // network byte order, as stored
	data := []byte("CFBTEL-VPN-diag")
	reply := make([]byte, 256)
	n, _, err := procIcmpSendEcho.Call(h, uintptr(dest), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 0,
		uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), uintptr(wait.Milliseconds()))
	if n == 0 {
		if errno, ok := err.(windows.Errno); ok && errno == 11010 { // IP_REQ_TIMED_OUT
			return 0, errors.New("timed out")
		}
		return 0, fmt.Errorf("no answer (%v)", err)
	}
	status := *(*uint32)(unsafe.Pointer(&reply[4]))
	if status != 0 {
		return 0, fmt.Errorf("no answer (ICMP status %d)", status)
	}
	return time.Duration(*(*uint32)(unsafe.Pointer(&reply[8]))) * time.Millisecond, nil
}
