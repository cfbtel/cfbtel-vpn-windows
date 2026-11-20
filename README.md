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
* **Fixed-size window** (460×760, no resize/maximize). **Home** is dark (navy), in the style of OpenVPN Connect:
  status and connected timer (from the tunnel service's start time), one card with the office, one big
  Connect/Disconnect button, live traffic graph (download green, upload orange), speeds now and last handshake,
  *More details* (totals, connected since, VPN and office address), "Your offices", and a footer with Check for
  updates · Diagnostics · Help · About. All drawn with GDI (`ui/homepage.go`); icons are drawn, not text glyphs.
* **Updates with a warning**: *Check for updates* (Home footer, tray, update balloon) asks first: "the VPN will be
  disconnected and CFBTEL VPN will close; it opens again in about a minute". On Yes it disconnects, the window
  closes when the installer starts, and after the update the app reconnects the offices that were on.
* UI settings live in `HKCU\Software\CFBTEL\VPN\UI` (chosen office, support name/email, reconnect list).
* **Speed test** tab (`speedtest/`): ping, jitter, download and upload measured on Cloudflare's speed test network
  (speed.cloudflare.com, ~25 s, 4 staggered streams with growing pieces; pauses and retries when Cloudflare
  answers 429, and says it's busy only if nothing could be measured), live gauge, server/ISP/IP, notes whether it ran through the VPN;
  link to speedtest.net (Ookla's engine can't be built in without their license).
* **Diagnostics** tab (`diag/`): internet, DNS, outbound UDP, CFBTEL service, other VPN adapters, and per office:
  address lookup, ping, the firewall side as AdmixCentral sees it (`POST /vpn/app/status`: firewall reachable,
  tunnel and port, this device's peer present, device removed), address clashes with the local network, handshake
  age, office DNS/gateway through the tunnel. **Send to CFBTEL support** posts the message, checks, device ID
  (hashed MachineGuid), device public keys and the last 400 log lines to AdmixCentral (`POST /vpn/app/support`),
  which stores the ticket and emails it to help@cfbtel.com with the report attached. If that fails, the app copies
  the report, saves it to Documents and opens an email to help@cfbtel.com. No private or preshared keys are sent.
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
