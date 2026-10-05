# iron-link — install (Windows)

For the desktop app use the installer from the release page,
`iron-link-*-setup.exe`. It installs the app together with the daemon and
registers the daemon as a service; nothing else is needed.

This archive contains only the daemon:

- `iron-link-daemon.exe` — the background service that runs the tunnel (Go;
  `sing-box`, `xray` and wintun are embedded in it, there are no separate
  cores or drivers).
- `install.ps1` — registers it as a service.

## Install the daemon on its own

In PowerShell, as Administrator:

```powershell
.\install.ps1
```

Copies the daemon to `%ProgramFiles%\iron-link` and registers it as an
auto-start **service** (LocalSystem, no console window, no per-launch UAC,
survives reboot). Manage it with `services.msc` or
`iron-link-daemon.exe stop|start|uninstall`.

## Uninstall

`iron-link-daemon.exe uninstall` deregisters the service. Delete
`%APPDATA%\iron-link` for a clean slate.
