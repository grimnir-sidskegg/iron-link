# iron-link — install

This bundle contains the self-contained **daemon** — there are no external
cores to manage (`sing-box` and `xray` are embedded inside it as libraries):

- `iron-link-daemon` — the privileged background service (Go; embeds the cores).

The desktop **GUI is the Flutter client**: on Windows it ships as the Inno
Setup installer (`iron-link-*-setup.exe`, which bundles this daemon + the
app); the Linux bundle carries it as `gui/` (run `gui/iron_link_flutter` in
place, or install the whole thing with the binary Arch recipe —
`packaging/arch/PKGBUILD-bin` in the repository); the macOS bundle carries it
as `iron-link.app`.

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
- **Linux:** copies the daemon onto your PATH; an OS service unit is not wired
  up here — start it manually, or use the Arch recipe.
- **macOS:** installs the daemon to `/usr/local/bin`, the app to
  `/Applications/iron-link.app`, and registers the daemon as a launchd
  **LaunchDaemon** (root, starts at boot, survives reboot). The control socket
  is handed to the installing user, so the app needs no privileges. macOS
  lists the daemon under System Settings → General → Login Items & Extensions.
  `./install.sh --uninstall` removes all three.

### macOS and Gatekeeper

The macOS bundle is signed ad hoc, not with an Apple Developer ID, and not
notarized. Gatekeeper treats anything a browser downloaded (the `quarantine`
flag) as unverified: opening the app shows "Apple could not verify…" with no
way to proceed, and the daemon binary stalls on first launch behind the same
dialog. Two ways around it:

1. Fetch and unpack in Terminal — `curl -L … | tar xz` sets no quarantine flag,
   so nothing is blocked. Then `./install.sh`.
2. Download with the browser, unpack, and run `./install.sh` from Terminal: it
   removes the quarantine flag from the whole bundle before installing.

Alternatively, after the blocked launch, System Settings → Privacy & Security
offers "Open Anyway" for the app for a short while. Either way: verify the
archive's `.sha256` against the release page before installing.

## Privileges (TUN only — proxy-only mode needs none)

- **Linux:** `install.sh` runs `setcap cap_net_admin,cap_net_raw=+ep` on the
  daemon (needs `sudo` once, during install). Note: in this no-root mode,
  DNS configuration through systemd-resolved may still require
  authentication on activation — running the daemon under `sudo` avoids
  that until a service unit lands.
- **macOS:** the launchd daemon runs as root (a `utun` needs it). Without the
  install, run `sudo ./iron-link-daemon` for TUN sessions.
- **Windows:** the service runs as LocalSystem, which has the rights for TUN;
  wintun is embedded in the daemon — no separate driver file to install.

## Uninstall

- **Windows:** `iron-link-daemon.exe uninstall` (deregisters the service).
- **Linux / macOS:** `./install.sh --uninstall`.

Then delete the config dir if you want a clean slate (`~/.config/iron-link`,
`~/Library/Application Support/iron-link`, or `%APPDATA%\iron-link`).
