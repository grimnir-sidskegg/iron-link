#!/usr/bin/env bash
# Sign an update manifest with the offline minisign secret key, then verify
# the result against the public keys compiled into the daemon before it can
# be published.
#
#   scripts/sign-update.sh <update.json> <minisign-secret-key>
#
# Draft convention: CI (build.yml, tag builds) emits update.json.draft with
# the artifact facts, timestamps, and a seq HINT (published manifest + 1)
# filled in. The hint can be wrong — a stale CDN read, a 404 on a fresh
# mirror — so the authority on seq is the counter file kept NEXT TO THE
# SECRET KEY: this script refuses to sign any seq other than counter + 1
# (never reuse or go back — clients reject a lower seq, and an equal seq
# would make two different manifests both valid). Review the draft before
# signing — version matches the tag you pushed, the artifact URL points at
# this repository's release, and the sha256 matches a locally downloaded
# installer — then rename it to update.json and sign.
#
# Signing is prehashed (-H) and the trusted comment mirrors the version and
# seq of the JSON; the daemon rejects a signature whose comment does not
# match the manifest body.
#
# Sign with the CURRENT key. The recovery key is not for routine use: a
# manifest signed with it makes every daemon that verifies it refuse the
# compiled-in current key from then on (a rotation after loss/compromise),
# and it must carry a seq above anything the old key may have published.
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

# The seq authority: a counter file beside the secret key records the last
# seq actually signed. CI's draft seq is only a hint (it reads the published
# manifest over a CDN and can be stale); the counter cannot be. First run
# bootstraps the file from the manifest being signed.
counter="$(dirname "$seckey")/iron-link-update.seq"
if [[ -f "$counter" ]]; then
  last_signed=$(<"$counter")
  expected=$((last_signed + 1))
  if [[ "$seq" -ne "$expected" ]]; then
    echo "error: manifest seq $seq, but the signing counter ($counter) says the next seq is $expected" >&2
    echo "fix the manifest — or, if the counter itself is wrong, edit the file deliberately" >&2
    exit 1
  fi
else
  echo "note: no signing counter at $counter — bootstrapping it from this manifest (seq $seq)"
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

echo "$seq" > "$counter"

cat <<EOF

Verified against the compiled-in keys; signing counter now $seq. To publish
(not executed):

  cp "$manifest_abs" "$manifest_abs.minisig" "$repo_root/updates/"
  cd "$repo_root"
  git add updates/update.json updates/update.json.minisig
  git commit -m "update manifest $version seq $seq"
  git push origin main

The publish-update workflow then verifies the pair, uploads it to the
$version release assets, and mirrors it to the legacy updates branch.
EOF
