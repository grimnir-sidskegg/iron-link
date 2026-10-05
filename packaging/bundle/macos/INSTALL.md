# iron-link — install (macOS)

This bundle contains:

- `iron-link.app` — the desktop app.
- `iron-link-daemon` — the background service that runs the tunnel (Go;
  `sing-box` and `xray` are embedded in it, there are no separate cores).
- `launchd/` — the LaunchDaemon template `install.sh` fills in.

## Install

```sh
./install.sh
```

It asks for your password once (sudo) and:

- installs the daemon to `/usr/local/bin/iron-link-daemon`;
- installs the app to `/Applications/iron-link.app`;
- registers the daemon as a launchd **LaunchDaemon**: it runs as root (a TUN
  needs it), starts at boot and is restarted if it exits. The control socket
  is handed to the user who ran `install.sh`, so the app needs no privileges.

When it prints `done`, the Terminal window can be closed — the daemon keeps
running in the background. Launch iron-link from `/Applications` (or
`open -a iron-link`). macOS lists the daemon under System Settings → General
→ Login Items & Extensions.

The daemon serves the user who installed it. Its config, state and log live
in `~/Library/Application Support/iron-link` (log: `daemon.log`).

## Gatekeeper

The bundle is signed ad hoc, not with an Apple Developer ID, and not
notarized. Gatekeeper treats anything a browser downloaded (the `quarantine`
flag) as unverified: opening the app shows "Apple could not verify…" with no
way to proceed, and the daemon binary stalls on first launch behind the same
dialog. Two ways around it:

1. Download with the browser, unpack, and run `./install.sh` from Terminal:
   it removes the quarantine flag from the whole bundle before installing.
2. Fetch and unpack in Terminal — `curl -L … | tar xz` sets no quarantine
   flag. Then `./install.sh`.

Either way, verify the archive's `.sha256` against the release
page before installing.

## Update

Unpack the new bundle and run its `./install.sh` again; it replaces the
daemon, the app and the launchd unit. Config is kept.

## Uninstall

```sh
./install.sh --uninstall
```

Removes the daemon, its launchd unit and the app. Config is kept; delete
`~/Library/Application Support/iron-link` for a clean slate.

## Troubleshooting

- The app says the daemon is not running: check
  `launchctl print system/org.iron-link.daemon` and
  `~/Library/Application Support/iron-link/daemon.log`; running
  `./install.sh` again re-registers the unit.
- For debugging only, the daemon can run in the foreground without the
  install: `sudo ./iron-link-daemon`. It stops when that Terminal window
  closes, and it must not run next to the installed one — unload that first
  (`sudo launchctl bootout system/org.iron-link.daemon`).
