# iron-link — install

This bundle contains the self-contained **daemon** — there are no external
cores to manage (`sing-box` and `xray` are embedded inside it as libraries):

- `iron-link-daemon` — the privileged background service (Go; embeds the cores).

The desktop **GUI is the Flutter client**, shipped separately: on Windows as
the Inno Setup installer (`iron-link-*-setup.exe`, which bundles this daemon +
the app); a Flutter bundle for Linux/macOS is a packaging follow-up — build it
from `client-flutter/` for now (`flutter build <linux|macos> --release`).

## Quick try (no install)

```sh
# Linux / macOS
sudo ./iron-link-daemon        # sudo for a TUN session; proxy-only needs none
```
```powershell
# Windows (PowerShell, as Administrator for TUN)
.\iron-link-daemon.exe
```

The daemon resolves its socket and config dir to the *invoking* user (under
`sudo` it uses `SUDO_UID`'s home), so an unprivileged client talks to it
directly. Run exactly ONE daemon at a time.

## Permanent install

```sh
# Linux / macOS
./install.sh
```
```powershell
# Windows — run as Administrator
.\install.ps1
```

- **Windows:** registers the daemon as an auto-start **service** (LocalSystem,
  background, no console window, no per-launch UAC, survives reboot). Manage it
  with `services.msc` or `iron-link-daemon.exe stop|start|uninstall`.
- **Linux / macOS:** copies the daemon onto your PATH; an OS service unit
  (systemd / launchd) is not wired up yet — start it manually for now.

## Privileges (TUN only — proxy-only mode needs none)

- **Linux:** `install.sh` runs `setcap cap_net_admin,cap_net_raw=+ep` on the
  daemon (needs `sudo` once, during install). Note: in this no-root mode,
  DNS configuration through systemd-resolved may still require
  authentication on activation — running the daemon under `sudo` avoids
  that until a service unit lands.
- **macOS:** run the daemon with `sudo` for TUN sessions.
- **Windows:** the service runs as LocalSystem, which has the rights for TUN;
  wintun is embedded in the daemon — no separate driver file to install.

## Uninstall

On Windows, `iron-link-daemon.exe uninstall` (deregisters the service). Then
delete the daemon binary and the config dir (`~/.config/iron-link`,
`~/Library/Application Support/iron-link`, or `%APPDATA%\iron-link`).
