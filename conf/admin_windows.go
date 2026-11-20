/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2019-2026 WireGuard LLC. All Rights Reserved.
 */

package conf

import (
	"sync"

	"golang.org/x/sys/windows/registry"
)

const adminRegKey = `Software\CFBTEL\VPN`

var (
	adminKey     registry.Key
	adminKeyOnce sync.Once
	adminKeyErr  error
)

func openAdminKey() (registry.Key, error) {
	adminKeyOnce.Do(func() {
		adminKey, adminKeyErr = registry.OpenKey(registry.LOCAL_MACHINE, adminRegKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	})
	return adminKey, adminKeyErr
}

func AdminBool(name string) bool {
	key, err := openAdminKey()
	if err != nil {
		return false
	}
	val, _, err := key.GetIntegerValue(name)
	if err != nil {
		return false
	}
	return val != 0
}

// AdminBoolDefault is AdminBool for settings that are on unless an administrator sets them to 0.
func AdminBoolDefault(name string, def bool) bool {
	key, err := openAdminKey()
	if err != nil {
		return def
	}
	val, _, err := key.GetIntegerValue(name)
	if err != nil {
		return def
	}
	return val != 0
}

// StandardUsersAllowed says whether people who aren't administrators on this computer may use CFBTEL VPN:
// sign in with Microsoft 365, connect and disconnect. On by default; an administrator turns it off with
// HKLM\Software\CFBTEL\VPN StandardUsers = 0 (REG_DWORD).
func StandardUsersAllowed() bool {
	return AdminBoolDefault("StandardUsers", true)
}
