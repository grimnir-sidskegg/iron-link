# Maintaining

Working notes for building, verifying, and releasing iron-link. User-facing
documentation stays in the [README](../README.md); the update system has its
own page, [`updates.md`](updates.md).

## Verification gates

`scripts/check.sh` runs `go vet` + `go test` (including the Go side of the
wire contract) and a 3-OS cross-build; `cd client-flutter && flutter analyze
&& flutter test` covers the client. Run both before pushing daemon or wire
changes.

`contract/fixtures/` pins the wire protocol. Any protocol change updates the
fixtures together with both language tables — Go in
`daemon-go/internal/api/contract_test.go`, Dart in
`client-flutter/test/contract_test.dart` — and needs a daemon rebuild for
live runs: a bare restart keeps the stale binary, which then fails the
contract against the new client.

The live TUN and real-node tests are gated behind root plus environment
variables and skip themselves otherwise; anything byte-exact under a
signature or hash (`updates/`, the update testdata) is pinned `-text` in
`.gitattributes` so a CRLF checkout cannot break verification.

## CI

`.github/workflows/build.yml` runs on manual dispatch (the normal path —
commits accumulate and are built when wanted) and on `v*` tags. A dispatch
run may stamp a real test version through the `version` input; otherwise the
build carries the `0.0.0-<sha>` sentinel, which the update check treats as
"unstamped, never compare". A tag build additionally refuses to ship dev
update keys, generates the release notes from the commit log since the
previous stable tag, creates the GitHub release with the bundles and the
Windows installer, and attaches `update.json.draft`.

`.github/workflows/publish-update.yml` fires when a signed manifest pair
lands in `updates/` on `main`: it re-verifies the signature against the
compiled-in public keys, refuses a non-increasing `seq`, uploads the pair to
the matching release's assets, and mirrors it to the legacy `updates` branch
for older builds. All workflow actions are pinned by commit SHA.

## Versioning

The version is stamped at build time — `-X main.version` into the daemon,
`--dart-define=IRON_LINK_VERSION` into the client, `/DAppVersion` into the
Windows installer — and `vX.Y.Z` tags are the source of truth. A tag with a
pre-release suffix (`v1.2.0-alpha.1`) is marked as a GitHub pre-release and
is excluded from stable release-notes ranges.

## Releases

The release and update-publication procedure — tag push, offline signing,
committing the manifest pair to `updates/` on `main` — is in
[`updates.md`](updates.md).

## Packaging

- **Windows** — the Inno Setup installer
  (`packaging/windows/iron-link.iss`) is built by CI; it carries both halves
  and registers the daemon as an auto-start service.
- **Arch Linux** — `packaging/arch/PKGBUILD` builds from `main` (VCS-style,
  `sha256sums=('SKIP')`). Run `makepkg -si` from a copy of `packaging/arch/`
  outside the working tree: makepkg rewrites the `pkgver=` line in place and
  dirties the checkout otherwise.
- **macOS** — source build only.
