# Updates

How iron-link ships and applies its own updates.

## Behavior

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

## Integrity

Integrity does not depend on the hosting origin. The manifest is
minisign-signed with an offline key; the daemon carries two public keys (a
routine `current` key and a `recovery` key for rotation). A recovery-signed
manifest retires the `current` key on every daemon that verifies it: from
then on only recovery-signed manifests are accepted until a build carrying
new keys is installed. A monotonic `seq`, mirrored in the signature's
trusted comment, rejects replayed older manifests.
The installer is pinned by `sha256` and size, and re-hashed right before the
client launches it.

## Testing against a local manifest

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

## Release procedure (maintainer)

1. `git tag vX.Y.Z && git push origin vX.Y.Z` — CI builds, generates the
   release notes from the commit log, and attaches `update.json.draft`.
2. Review the draft, rename it to `update.json`, and sign:
   `scripts/sign-update.sh update.json <current.key>`. The script header
   documents what to review and the checks it enforces.
3. Commit the signed pair to `updates/` on `main` and push (the script
   prints the commands); the publish-update workflow verifies it and
   mirrors it to the release assets and the legacy `updates` branch.

A bad release is rolled forward — a new, higher version with the next `seq`
— never rolled back: daemons reject a manifest whose `seq` or version goes
backwards.
