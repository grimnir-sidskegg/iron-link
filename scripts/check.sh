#!/usr/bin/env bash
# Local verification gate for the Go daemon (docs/maintaining.md).
# Run this before committing daemon-go or wire changes:
#   scripts/check.sh
#
# Covers: go vet, unit tests (live TUN/node tests are env-gated and skip
# themselves; the Go wire-contract fixture suite runs here too), and the 3-OS
# cross-build matrix (green Linux != green Windows — the sing-tun pin burned us
# once). The Dart half of the wire contract lives in client-flutter/ (run
# `flutter test` there).
set -euo pipefail
cd "$(dirname "$0")/.."

# The canonical shipping tag set. with_quic gates sing-box's QUIC outbounds
# (hysteria2/tuic/hysteria); building/testing UNtagged hides tag-gated breakage,
# so the gate must carry the same tags the shipped binary does.
TAGS="with_gvisor,with_utls,with_clash_api,with_quic"

echo "== go vet (-tags ${TAGS})"
(cd daemon-go && go vet -tags "$TAGS" ./...)

echo "== go test (unit; live tests skip without IRON_LINK_RUNTIME_TEST etc.)"
(cd daemon-go && go test -tags "$TAGS" ./...)

echo "== cross-build matrix (CGO_ENABLED=0)"
for goos in linux windows darwin; do
  echo "   -- GOOS=${goos}"
  (cd daemon-go && CGO_ENABLED=0 GOOS="${goos}" go build -tags "$TAGS" ./...)
done

echo "OK — all checks green"
