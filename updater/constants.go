/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2019-2026 WireGuard LLC. All Rights Reserved.
 */

package updater

const (
	// CFBTEL's release signing key (signify / Ed25519). The private key is a GitHub Actions secret.
	releasePublicKeyBase64 = "RWQRDpzpNh/WzVM7eN1I0qLilEkCJSwZAys/y774eR4AvHZp+0cjn8Su"
	// Updates come from the latest GitHub release of cfbtel/cfbtel-vpn-windows
	// (latest.sig lists each MSI's BLAKE2b-256 hash and is signed with the key above).
	updateServerHost     = "github.com"
	updateServerPort     = 443
	updateServerUseHttps = true
	latestVersionPath    = "/cfbtel/cfbtel-vpn-windows/releases/latest/download/latest.sig"
	msiPath              = "/cfbtel/cfbtel-vpn-windows/releases/latest/download/%s"
	msiArchPrefix        = "cfbtelvpn-%s-"
	msiSuffix              = ".msi"
)
