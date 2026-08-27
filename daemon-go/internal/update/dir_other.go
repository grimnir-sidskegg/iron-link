//go:build !windows

package update

import "errors"

// UpdatesDir exists for signature parity with the Windows implementation,
// which resolves the protected download directory for installer artifacts.
// Linux and macOS are notify-only channels (kind=none): nothing is ever
// downloaded, so there is no staging directory to hand out. Refusing beats
// defaulting to the config root: the daemon runs as root while the config
// root belongs to the invoking user, and staging root-downloaded files in a
// user-writable tree would reopen the download/launch TOCTOU the Windows
// protected DACL exists to close. A future downloadable kind here needs a
// root-owned system path (e.g. /var/cache/iron-link) with the same
// pre-existing-directory ownership checks as the Windows side.
func UpdatesDir(string) (string, error) {
	return "", errors.New("update: no downloadable update channel on this platform")
}
