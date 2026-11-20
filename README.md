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
  builds signed with CFBTEL's code-signing certificate. It checks about every hour and **installs updates by
  itself**: right away when no tunnel is connected, otherwise at the next idle moment and within 24 hours at most
  (tunnels reconnect after the upgrade). People can also click **Update now**. Turn automatic installs off with
  `HKLM\Software\CFBTEL\VPN\DisableAutoUpdate` = 1 (DWORD); updates are then only offered.
* **About**: CFBTEL copyright, help@cfbtel.com, and **Open-source licenses** (`ui/notices.txt`: WireGuard for
  Windows MIT, WireGuardNT Prebuilt Binaries License, Walk/win/Go BSD-3). Keep it current when dependencies change.
* **CFBTEL look**: the window opens on **Home** — CFBTEL navy/cyan status header, one big Connect / Disconnect
  button, traffic and handshake while connected, and "Your offices" (click an office's *Connect* to switch).
  The WireGuard tunnel editor and log are on the **Advanced** and **Log** tabs. The tray icon is faded when off.
  **Check for updates** is on Home and in the tray menu (anyone can install a newer signed release).
  The installer has a CFBTEL wizard (`installer/dialog.bmp`, `banner.bmp`, `license.rtf` from `ui/notices.txt`).
* English UI.

## Releasing

1. Raise `Number` in `version/version.go` (e.g. `1.0.1`) and push to `main`.
2. The **Build and release** workflow builds x86/amd64/arm64, signs programs and MSIs, writes `latest.sig` and
   publishes release `v1.0.1`. Installed copies install it automatically (see above).

Repository secrets (Settings → Secrets and variables → Actions):

| Secret | What |
|---|---|
| `CFBTEL_RELEASE_KEY` | private key from `tools/releasesign` (double-click the Windows build: it copies the private key to the clipboard and shows the public key for `updater/constants.go`) |
| `CFBTEL_PORTAL_URL` | `https://<staff VPN page host>`; kept out of the source |

Code signing uses **Microsoft Artifact Signing** (account `signalage-signing`, profile `signalage-public`,
certificate name `CFBTEL`) through the Entra app "CFBTEL VPN GitHub signing" (GitHub OpenID Connect, no secret).
Repository variables: `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`, `ARTIFACT_SIGNING_ENDPOINT`,
`ARTIFACT_SIGNING_ACCOUNT`, `ARTIFACT_SIGNING_PROFILE`, `CFBTEL_SIGNER`. (A `.pfx` in secrets
`CFBTEL_CODESIGN_PFX` / `CFBTEL_CODESIGN_PASSWORD` also works.) Without code signing the release is an unsigned
pre-release: it installs (SmartScreen warns) but doesn't update itself.

## Building locally

Linux: `make amd64/cfbtelvpn.exe` (needs mingw-w64, ImageMagick, bsdtar). Windows: `build.bat`, then
`installer\build.bat` for the MSIs. See the upstream instructions below for details.

## Rebasing on a new WireGuard for Windows

`git fetch https://git.zx2c4.com/wireguard-windows --tags && git rebase <new tag>` (or merge), keep the CFBTEL
changes, rebuild, bump `version/version.go`.

## License

MIT (see `COPYING`), like upstream; the installer definition is GPL-2.0 as upstream.
