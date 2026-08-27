# iron-link

iron-link is a desktop proxy orchestrator for Linux, macOS, and Windows. It runs
as a single Go daemon that embeds the sing-box and xray cores as libraries, with
a Flutter desktop client on top; the two talk over a typed IPC.

> Status: pre-alpha. The daemon lives in `daemon-go/`; the client lives in
> `client-flutter/`.

## Overview

Existing desktop GUIs for sing-box and xray tend to expose only a preset-driven
subset of each core's capabilities. iron-link inverts that: the whole
configuration surface of both cores is reachable from one UI, and every
structured editor falls back to raw JSON where it runs out.

The basic flow is: add a subscription, choose a server, probe latency, connect.
Around that sit a routing editor, per-application traffic accounting, a
connection doctor, and TUN/proxy controls.

## Features

- **Home** — connection state; a node tree that mixes standalone nodes with
  subscription groups; single-click connect in either **TUN** (whole-device) or
  **proxy-only** (SOCKS) mode; switching nodes live; per-node latency probes.
- **Routing** — a schema-driven editor covering the complete sing-box 1.12
  condition set. Unknown or advanced keys and logical rules are preserved as-is,
  and a raw-JSON view is always reachable.
- **Logs** — the sing-box feed, live, filterable by text and level.
- **Traffic** — rate and cumulative totals per application, broken down by
  outcome (direct / proxy / blocked); Linux shows real application icons.
- **Doctor** — connectivity preflight, IPv4 and IPv6 address-family reachability
  included, each with a one-click remedy.
- **Settings** — DNS upstreams and strategy, IP version, LAN bypass, SOCKS port,
  TUN options, restore-on-start, subscription User-Agent, latency target.
- **System tray** — connect/disconnect, node switch, show/quit, close-to-tray.
  Linux is served by a pure-Dart StatusNotifierItem over DBus (no
  libappindicator); Windows is implemented; macOS is not yet done.

Supported protocols: **VLESS, Shadowsocks (including SS-2022), VMess, Trojan,
Hysteria2, TUIC, Hysteria v1, and AnyTLS** — the full set of share-link
protocols the embedded cores understand. Each lives in a single file behind a
protocol registry, and the core is chosen per node: sing-box natively where it
can, and the in-process xray bridge where only xray is able to dial.

## Architecture

- **A single process holding two cores.** `iron-link-daemon` (Go) links
  [sing-box](https://github.com/SagerNet/sing-box) and
  [xray-core](https://github.com/XTLS/Xray-core) as libraries. There are no
  child processes and no separately downloaded core binaries.
- **sing-box handles the tunnel** — TUN, routing, DNS — all compiled in-process.
- **xray covers what sing-box cannot** (`xhttp`). An `xray-reality` outbound
  joins the two cores through an in-memory `core.Dial`; there is no loopback hop
  between them.
- **Core choice is per node**, driven by a capability table. The client surfaces
  the eligible cores and lets you override among them.
- **There is no Clash API.** Traffic, logs, latency, and live node selection are
  all in-process instrumentation, reachable only through the daemon's owner-only
  IPC (framed JSON — the frames are defined in `contract/`).
- **The daemon decides what runs** and holds the store (profiles, nodes,
  subscriptions, routing). The client only sends intent — names and flags — and
  never executable paths or raw configs.

## Layout

```
daemon-go/       Engine (sing-box + xray + bridge), store, subscription
                 pipeline, routing compiler, IPC server, node diagnosis, doctor.
contract/        Canonical wire frames, asserted by both the Go daemon and the
                 Dart client suites (the cross-language contract).
client-flutter/  The Flutter desktop client — a thin front-end over the daemon
                 IPC: lib/src/{wire,ipc,app,model,ui,platform}; tests in test/.
packaging/       Release bundling (assemble.py + per-OS installers, incl. the
                 Windows Inno Setup installer).
```

## Build

You need **Go 1.26+** and the **Flutter SDK (Dart 3.12+)**. Both cores are
pinned — sing-box `v1.13.12` and xray-core `v1.260327.0` — and come in as
ordinary Go modules, so there is nothing else to install.

### Daemon (Go)

```bash
cd daemon-go
go build -tags "with_gvisor,with_utls,with_clash_api,with_quic" -o bin/iron-link-daemon ./cmd/iron-link-daemon
```

All four tags are **required**; dropping any one produces runtime errors:

| tag | purpose |
|---|---|
| `with_gvisor` | the gVisor TUN stack |
| `with_utls` | native REALITY (uTLS) |
| `with_clash_api` | sing-box couples its in-process log hook to this tag (nothing listens externally) |
| `with_quic` | the QUIC outbounds (hysteria2 / tuic / hysteria) |

```bash
sudo ./bin/iron-link-daemon    # TUN (full-device). Resolves socket/store to the
                               # invoking user, so the client needs no root.
./bin/iron-link-daemon         # proxy-only: SOCKS on 127.0.0.1:10808, no root.
```

### Client (Flutter)

```bash
cd client-flutter
flutter run   -d <linux|macos|windows>    # dev run (start the daemon first)
flutter build    <linux|macos|windows> --release
```

The client reaches the running daemon over the owner-only IPC — a unix socket,
or a named pipe on Windows — and reconnects by itself. The Windows release
artifact is a single installer: it carries both halves and registers the daemon
as an auto-start service (see `packaging/`).

> If you change the wire protocol or the routing/traffic shapes, rebuild the
> daemon. A bare restart keeps the stale binary, which then fails the contract
> against the new client.

## Platform status

| | Linux | macOS | Windows |
|---|---|---|---|
| Daemon end-to-end | verified | cross-build only, runtime pending | verified (TUN + xhttp) |
| Tray | done (SNI/DBus) | pending | implemented (runtime test pending) |
| Packaging | bundle pending | source-build only (unsigned) | one-click installer (unsigned) |

On GNOME the tray needs the AppIndicator extension; KDE, XFCE, Cinnamon, MATE,
Budgie, and waybar host it directly.

## Updates

The daemon checks a signed update manifest about once a day and caches the
verdict; the client reads it back and shows a banner when a newer version
exists. The check goes through the running session's tunnel when there is
one, otherwise over a TUN-exempt direct connection. An unreachable or blocked
manifest host is logged and otherwise ignored — nothing retries, nothing
prompts.

- **Windows** — the daemon downloads the installer into
  `%ProgramData%\iron-link\updates` (a directory only the service writes),
  verifies it, and the client launches it: one click, one UAC prompt. The
  installer stops the service, replaces both halves, starts the service again
  and relaunches the client.
- **Linux** — notify-only. The package is pacman-managed and is rebuilt from
  the repository (`makepkg -si`); the banner carries the release notes link.
- **macOS** — notify-only: the banner and the release notes link.

Three settings govern it, all on by default:

| setting | effect |
|---|---|
| `auto_update` | the daily background check; a manual "Check now" works regardless |
| `update_via_tunnel` | use the running session's outbound for the check and the download |
| `update_via_direct` | otherwise — or with no session — connect directly, exempt from the TUN |

With both transport settings off nothing is fetched at all, including manual
checks and downloads. The request carries no identifiers — no version, no
machine id, no query string — under a static browser User-Agent, on a ~24 h
cadence with ±10% jitter.

Integrity does not depend on the hosting origin. The manifest is
minisign-signed with an offline key; the daemon carries two public keys (a
routine `current` key and a `recovery` key for rotation). A monotonic `seq`,
mirrored in the signature's trusted comment, rejects replayed older manifests.
The installer is pinned by `sha256` and size, and re-hashed right before the
client launches it.

### Testing against a local manifest

`IRON_LINK_UPDATE_URL` (a comma-separated list) replaces the built-in manifest
URL; plain `http://` is accepted under the override only. Serve a directory
holding `update.json` and `update.json.minisig` (signed with
`scripts/sign-update.sh`) from any static HTTP server and point the daemon at
it:

- foreground: `IRON_LINK_UPDATE_URL=http://198.51.100.1:8000/update.json ./bin/iron-link-daemon`
- Windows service: set the variable in the shell that runs
  `iron-link-daemon.exe install`, then restart the service — `install` bakes
  it into the service environment next to `IRON_LINK_CONFIG_DIR`. Every
  `install` rebuilds that environment from its own shell, and the installer
  runs one on each (re)install, in-app updates included, so repeat the command
  afterwards or the override is gone
- Linux service: add the line to `/etc/iron-link/config.env` (the unit's
  `EnvironmentFile`) and restart the service

### Release procedure (maintainer)

1. Tag and push: `git tag vX.Y.Z && git push origin vX.Y.Z`. CI builds the
   installer and attaches `update.json.draft` with the artifact facts and the
   release notes link.
2. Rename the draft to `update.json`; fill in `seq` (last published + 1),
   `published_at`, and `expires_at` (about 180 days out).
3. `scripts/sign-update.sh update.json <current.key>` — signs the manifest and
   verifies it against the compiled-in keys.
4. Push `update.json` and `update.json.minisig` to the orphan `updates` branch
   (the script prints the commands).

## Running TUN — operational notes

1. **Run one daemon at a time.** Two of them contend over TUN addressing and
   routing and will quietly break the default route.
2. **Shut down cleanly** — `Ctrl-C` or SIGTERM. The daemon then unwinds the TUN,
   the routes, and its nftables. Avoid `kill -9`: SIGKILL bypasses teardown and
   leaves routes and rules behind that blackhole traffic.
3. Check the state once it has stopped:
   ```bash
   ip rule show                          # expect only 0 / 32766 / 32767
   ip -br link | grep -iE 'ilnk|tun777'  # expect nothing
   ```
4. Recovering after a stray `kill -9` or a crash:
   ```bash
   sudo ip link delete tun777 2>/dev/null
   sudo ip rule del priority 9000 2>/dev/null
   sudo ip rule del priority 9001 2>/dev/null
   sudo ip rule del priority 9002 2>/dev/null
   sudo ip rule del priority 32768 2>/dev/null
   sudo ip route flush table 2022 2>/dev/null
   sudo nft list ruleset | grep -i sing   # delete leftover sing-box tables
   ```
   (A reboot clears all of it too.)

## Verify

```bash
scripts/check.sh        # go vet + go test (incl. the Go wire contract) + 3-OS cross-build
cd client-flutter && flutter analyze && flutter test
```

`contract/fixtures/` pins the wire contract. Any protocol change has to update
those fixtures together with both language tables — Go in
`daemon-go/internal/api/contract_test.go`, Dart in
`client-flutter/test/contract_test.dart`. The live TUN and real-node tests are
gated behind root plus environment variables and skip themselves otherwise.

## Security

- **Intent-only IPC.** The wire carries names and flags — never executable paths
  or argv. Because the cores are embedded, nothing is ever spawned.
- **No localhost control plane.** All instrumentation stays in-process,
  reachable only over the owner-only socket (mode 0600, chowned to the invoking
  user; on Windows, a named pipe whose DACL admits the interactive user).
- **Atomic state writes** (write-temp-then-rename, 0600), since profiles hold
  credentials.

## Acknowledgements

iron-link is only a front-end. The circumvention itself belongs to the cores it
embeds:

- **sing-box** and the wider `sing` ecosystem
  ([SagerNet](https://github.com/SagerNet)) — the component that owns the tunnel
  here: TUN, routing, DNS.
- **Xray-core** ([Project X / XTLS](https://github.com/XTLS)) and the
  VLESS / REALITY / XTLS line of work — what dials the transports sing-box
  cannot.
- The broader [Project V / V2Ray / V2Fly](https://github.com/v2fly) lineage the
  cores descend from.

## License

**GPL-3.0-or-later** — see [`LICENSE`](LICENSE). The daemon links sing-box
(GPL-3.0) and xray-core (MPL-2.0) as libraries, so distributed daemon binaries
fall under GPL-3.0. The third-party license texts, generated from the actual
build, are collected in [`THIRD_PARTY_LICENSES`](THIRD_PARTY_LICENSES).
