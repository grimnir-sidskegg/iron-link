# iron-link — install (Linux)

This bundle contains:

- `iron-link-daemon` — the privileged background service that runs the
  tunnel (Go; `sing-box` and `xray` are embedded in it, there are no separate
  cores).
- `gui/` — the desktop app (`gui/iron_link_flutter`), runnable in place.
- `systemd/iron-link.service`, `iron-link.desktop`, `iron-link.png` — the
  service unit, menu entry and icon a package wraps.

## Full install (Arch Linux)

The binary recipe `packaging/arch/PKGBUILD-bin` in the repository repacks
this tarball into a package: the daemon as a root systemd service that starts
at boot, the app in the menu. Install it with `makepkg -si -p PKGBUILD-bin`.
This is the only path that sets the daemon up as a service.

## Run from this bundle

The daemon runs in the foreground in its own terminal; closing that terminal
stops it. Start it, then the app:

```sh
sudo ./iron-link-daemon        # sudo for a TUN session; proxy-only needs none
./gui/iron_link_flutter
```

The daemon resolves its socket and config dir to the *invoking* user (under
`sudo` it uses `SUDO_UID`'s home), so the unprivileged app talks to it
directly. Run exactly ONE daemon at a time.

## install.sh

```sh
./install.sh
```

Copies the daemon to `~/.local/bin` (override with `BIN_DIR=…`) and grants it
`cap_net_admin,cap_net_raw` with `setcap` (sudo once). It does not register a
service: the daemon is still started by hand as above. In this no-root mode
DNS configuration through systemd-resolved may ask for authentication on
activation — running the daemon under `sudo` avoids that.

## Uninstall

`./install.sh --uninstall` removes the daemon from `BIN_DIR`. Delete
`~/.config/iron-link` for a clean slate.
