# packaging

Release tooling driven by `.github/workflows/build.yml` (manual dispatch is
the normal path; the release step fires on `v*` tags). Each run
builds the Go daemon on Linux/macOS/Windows and produces a self-contained
per-OS bundle. On Windows it additionally builds the Flutter client and wraps
it together with the daemon into a single-file Inno Setup installer
(`iron-link-*-setup.exe`).

There is no core fetching: `sing-box` and `xray` are **embedded as libraries**
inside the Go daemon (`daemon-go/`), pinned in its `go.mod` (never float
them). The old
`cores.lock`/`fetch_cores.py` supply-chain machinery went with the spawning
Rust daemon (removed earlier; the Rust front-ends followed 2026-06-13).

## Pieces

- **`assemble.py`** — copies the Go daemon binary (via `--daemon-bin`) and
  `bundle/` files into the staging dir, then writes
  `iron-link-<version>-<target>.{tar.gz,zip}` + a `.sha256`.
- **`bundle/`** — what ships inside every archive: `INSTALL.md`, `install.sh`
  (Linux/macOS), `install.ps1` (Windows).
- **`arch/`** — native Arch Linux package (`PKGBUILD`, `.install`, `.desktop`).
  Builds both halves from GitHub `main` (Go daemon + `flutter build linux`),
  installs the daemon as a **root systemd service** (`systemd/iron-link.service`)
  and the Flutter GUI into the app menu, then wires them over
  `/run/iron-link/iron-link.sock` (socket group `iron-link`; the post-install
  adds the installing user and writes `/etc/iron-link/config.env`). Install/
  update: `makepkg -si` (in `packaging/arch/`); remove: `pacman -R iron-link`.
  Needs `flutter` (stable) on PATH; no CI — it compiles locally.
- **`windows/iron-link.iss`** — the Inno Setup script (Windows only). Bundles
  `iron-link-daemon.exe` + the Flutter client into one installer. The daemon is
  intentionally NOT a service: it must run elevated but as the logged-in user so
  its `%APPDATA%` matches the client's, so the script sets an AppCompat
  `RUNASADMIN` layer instead of registering an SCM unit. Compiled by the
  workflow with `ISCC`; the version + source paths come in as `/D` defines.

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

macOS service (launchd) + bundle/installer (Linux now has the Arch package +
systemd unit above; macOS still starts by hand and builds from source), macOS
code-signing/notarization, Windows Authenticode signing (the installer is
unsigned — SmartScreen will warn), more native packages (`.deb`/`.rpm`/`.dmg`),
and more arches (linux-arm64, darwin-amd64).
