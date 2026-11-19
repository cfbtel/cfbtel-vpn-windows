# CFBTEL VPN for Windows

CFBTEL's branded VPN client for Windows, built on the official
[WireGuard® for Windows](https://git.zx2c4.com/wireguard-windows) 1.1.1 (WireGuardNT kernel driver, same tunnel
engine and security). "WireGuard" is a registered trademark of Jason A. Donenfeld; this is an independent build by
CFBTEL and is not endorsed by the WireGuard project.

What's different from upstream:

* **CFBTEL VPN** name, icon and installer, installed side by side with WireGuard if present
  (own services `CFBTELVPNManager` / `CFBTELVPNTunnel$<name>`, folder `Program Files\CFBTEL VPN`,
  registry `HKLM\Software\CFBTEL\VPN`, MSI upgrade codes).
* **Sign in to CFBTEL** (toolbar, tray menu, empty window): the app makes its own key pair, opens the staff VPN page
  in the browser, the person signs in (Microsoft 365 or email and password) and picks the office, and the tunnel is
  added and turned on. The private key never leaves the computer. Server side: AdmixCentral `/vpn/app`.
* **Updates from CFBTEL**: the built-in updater reads `latest.sig` from the latest release of this repository,
  verifies it with CFBTEL's release key (Ed25519, signify format) and the MSI's BLAKE2b hash, and only runs on
  builds signed with CFBTEL's code-signing certificate.
* English UI.

## Releasing

1. Raise `Number` in `version/version.go` (e.g. `1.0.1`) and push to `main`.
2. The **Build and release** workflow builds x86/amd64/arm64, signs programs and MSIs, writes `latest.sig` and
   publishes release `v1.0.1`. Installed copies offer the update (tray notification, Update tab).

Repository secrets (Settings → Secrets and variables → Actions):

| Secret | What |
|---|---|
| `CFBTEL_RELEASE_KEY` | private key from `tools/releasesign` (double-click the Windows build: it copies the private key to the clipboard and shows the public key for `updater/constants.go`) |
| `CFBTEL_PORTAL_URL` | `https://<staff VPN page host>`; kept out of the source |
| `CFBTEL_CODESIGN_PFX` | base64 of the code-signing `.pfx` |
| `CFBTEL_CODESIGN_PASSWORD` | its password |

Without the code-signing secrets the release is an unsigned pre-release: it installs (SmartScreen warns) but
doesn't update itself.

## Building locally

Linux: `make amd64/cfbtelvpn.exe` (needs mingw-w64, ImageMagick, bsdtar). Windows: `build.bat`, then
`installer\build.bat` for the MSIs. See the upstream instructions below for details.

## Rebasing on a new WireGuard for Windows

`git fetch https://git.zx2c4.com/wireguard-windows --tags && git rebase <new tag>` (or merge), keep the CFBTEL
changes, rebuild, bump `version/version.go`.

## License

MIT (see `COPYING`), like upstream; the installer definition is GPL-2.0 as upstream.
