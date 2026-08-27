#!/usr/bin/env bash
# Sign an update manifest with the offline minisign secret key, then verify
# the result against the public keys compiled into the daemon before it can
# be published.
#
#   scripts/sign-update.sh <update.json> <minisign-secret-key>
#
# Draft convention: CI (build.yml, tag builds) emits update.json.draft with
# the artifact facts filled in and the owner-only fields left as
# placeholders — "seq": 0 and published_at/expires_at set to
# "0001-01-01T00:00:00Z". Before signing, rename the draft to update.json
# and replace the placeholders: seq = last published seq + 1 (never reuse or
# go back — clients reject a seq they have already seen), published_at = now,
# expires_at = published_at + ~180 days. This script refuses placeholders.
#
# Signing is prehashed (-H) and the trusted comment mirrors the version and
# seq of the JSON; the daemon rejects a signature whose comment does not
# match the manifest body.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <update.json> <minisign-secret-key>" >&2
  exit 2
fi
manifest=$1
seckey=$2
[[ -f "$manifest" ]] || { echo "error: manifest not found: $manifest" >&2; exit 1; }
if [[ "$(basename "$manifest")" != "update.json" ]]; then
  echo "error: manifest must be named update.json (rename the draft first) — the published files are update.json + update.json.minisig" >&2
  exit 1
fi
[[ -f "$seckey" ]] || { echo "error: secret key not found: $seckey" >&2; exit 1; }
command -v minisign >/dev/null || { echo "error: minisign is not installed" >&2; exit 1; }
command -v jq >/dev/null || { echo "error: jq is not installed" >&2; exit 1; }

version=$(jq -er .version "$manifest")
seq=$(jq -er .seq "$manifest")
published_at=$(jq -er .published_at "$manifest")
expires_at=$(jq -er .expires_at "$manifest")

placeholder="0001-01-01T00:00:00Z"
if [[ "$seq" -eq 0 ]]; then
  echo "error: seq is still the draft placeholder 0 — set it to the last published seq + 1" >&2
  exit 1
fi
if [[ "$published_at" == "$placeholder" || "$expires_at" == "$placeholder" ]]; then
  echo "error: published_at/expires_at still hold the draft placeholder $placeholder" >&2
  exit 1
fi

sigfile="$manifest.minisig"
trusted="version=$version seq=$seq"
minisign -S -H -s "$seckey" -x "$sigfile" \
  -c "iron-link update manifest" -t "$trusted" -m "$manifest"
echo "signed: $sigfile (trusted comment: $trusted)"

# Verify with the daemon's own verifier and its compiled-in public keys so a
# key rotation gone wrong or an edited-after-signing manifest is caught here,
# not by every client after publish.
repo_root=$(cd "$(dirname "$0")/.." && pwd)
manifest_abs=$(cd "$(dirname "$manifest")" && pwd)/$(basename "$manifest")
(cd "$repo_root/daemon-go" &&
  IRON_LINK_UPDATE_MANIFEST="$manifest_abs" \
  IRON_LINK_UPDATE_SIG="$manifest_abs.minisig" \
  go test -count=1 -run '^TestVerifySignedFile$' -v ./internal/update)

cat <<EOF

Verified against the compiled-in keys. To publish (not executed):

  # first publish only — create the orphan updates branch:
  #   git worktree add --orphan -b updates ../iron-link-updates
  git worktree add ../iron-link-updates updates
  cp "$manifest_abs" "$manifest_abs.minisig" ../iron-link-updates/
  cd ../iron-link-updates
  git add update.json update.json.minisig
  git commit -m "update manifest $version seq $seq"
  git push origin updates
  cd - && git worktree remove ../iron-link-updates
EOF
