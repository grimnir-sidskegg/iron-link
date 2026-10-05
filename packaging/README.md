# packaging

Release tooling driven by `.github/workflows/build.yml` (manual dispatch is
the normal path; the release step fires on `v*` tags). Each run
builds the Go daemon and the Flutter client on Linux/macOS/Windows. On Linux
and macOS both go into the per-OS bundle (`gui/` and `iron-link.app`); on
Windows the client and the daemon are wrapped into a single-file Inno Setup
installer (`iron-link-*-setup.exe`), next to a daemon-only bundle.

There is no core fetching: `sing-box` and `xray` are **embedded as libraries**
inside the Go daemon (`daemon-go/`), pinned in its `go.mod` (never float
them).

## Pieces

- **`assemble.py`** — copies the Go daemon binary (via `--daemon-bin`) and
  `bundle/` files into the staging dir, then writes
  `iron-link-<version>-<target>.{tar.gz,zip}` + a `.sha256`.
- **`bundle/`** — what ships inside the archives, one directory per OS:
  `linux/`, `macos/`, `windows/`, each with its own `INSTALL.md` and
  installer (`install.sh` or `install.ps1`). Each archive carries only its
  own OS's files.
- **`arch/`** — native Arch Linux package (`PKGBUILD`, `.install`, `.desktop`).
  Builds both halves from GitHub `main` (Go daemon + `flutter build linux`),
  installs the daemon as a **root systemd service** (`systemd/iron-link.service`)
  and the Flutter GUI into the app menu, then wires them over
  `/run/iron-link/iron-link.sock` (socket group `iron-link`; the post-install
  adds the installing user and writes `/etc/iron-link/config.env`). Install/
  update: `makepkg -si` (in `packaging/arch/`); remove:
  `pacman -R iron-link-git`. Needs `flutter` (stable) on PATH; no CI — it
  compiles locally. `PKGBUILD-bin` produces the same layout from the release
  tarball instead (`iron-link-bin`, needs only base-devel).
- **`windows/iron-link.iss`** — the Inno Setup script (Windows only). Bundles
  `iron-link-daemon.exe` + the Flutter client into one installer and registers
  the daemon as an auto-start Windows service (LocalSystem); `install` bakes
  the installing user's config dir into the service environment so it shares
  `%APPDATA%\iron-link` with the client. Compiled by the workflow with `ISCC`;
  the version + source paths come in as `/D` defines.

## Targets

`linux-amd64` (ubuntu), `darwin-arm64` (macOS), `windows-amd64` (windows) — the
three default GitHub runners, one arch each. Add more by extending the matrix
in the workflow.

## Run locally

```sh
(cd daemon-go && CGO_ENABLED=0 go build -o bin/iron-link-daemon ./cmd/iron-link-daemon)
python3 packaging/assemble.py --target linux-amd64 --version 0.0.0 \
    --daemon-bin daemon-go/bin/iron-link-daemon \
    --staging dist --archive tar.gz
```

## Not yet (follow-ups)

A service install from the Linux bundle (its `install.sh` only copies the
daemon; the systemd unit is wired up by the Arch recipes alone), macOS
code-signing/notarization, Windows Authenticode signing (the installer is
unsigned — SmartScreen will warn), more native packages (`.deb`/`.rpm`/`.dmg`),
and more arches (linux-arm64, darwin-amd64).
