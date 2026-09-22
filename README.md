# iron-link

iron-link is a desktop proxy orchestrator for Linux, macOS, and Windows. It runs
as a single Go daemon that embeds the sing-box and xray cores as libraries, with
a Flutter desktop client on top; the two talk over a typed IPC.

> Status: pre-alpha. Verified end-to-end on Linux, Windows and macOS (Apple
> Silicon); the macOS bundle is not notarized, see `packaging/bundle/INSTALL.md`.
> The daemon lives in `daemon-go/`; the client lives in `client-flutter/`.

## Overview

Existing desktop GUIs for sing-box and xray tend to expose only a preset-driven
subset of each core's capabilities. iron-link inverts that: the whole
configuration surface of both cores is reachable from one UI, and every
structured editor falls back to raw JSON where it runs out.

The basic flow is: add a subscription, choose a server, probe latency, connect.
Around that sit a routing editor, per-application traffic accounting, a
connection doctor, and TUN/proxy controls.

## Features

- **The full configuration surface of both cores.** Structured editors cover
  what they can and fall back to raw JSON where they run out — nothing the
  cores accept is out of reach.
- **Two connection modes** — TUN (whole-device) and proxy-only (local SOCKS) —
  with live node switching and per-node latency probes.
- **Routing** over the complete sing-box condition set; unknown and advanced
  keys survive editing untouched.
- **Built-in observability** — live core logs, per-application traffic broken
  down by outcome (direct / proxy / blocked), and a connectivity doctor with
  one-click remedies.

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

## Updates

The daemon checks a minisign-signed update manifest about once a day: the
signing key lives offline, a monotonic counter rejects rollbacks, and the
request carries no identifiers. Windows updates install in one click;
Linux and macOS are notify-only. The full design — behavior, integrity
model, local testing, release procedure — is in
[`docs/updates.md`](docs/updates.md).

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

`contract/fixtures/` pins the wire contract, asserted by both language suites.
The live TUN and real-node tests are gated behind root plus environment
variables and skip themselves otherwise. Build, CI, and release process notes
live in [`docs/maintaining.md`](docs/maintaining.md).

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
