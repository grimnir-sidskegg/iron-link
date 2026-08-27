//go:build !windows

package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// UpdatesDir resolves (and creates on first use) the artifact download
// directory: <configRoot>/updates, private to the daemon (0700). Linux and
// macOS are notify-only channels (kind=none), so in practice nothing lands
// here — the helper exists so call sites stay platform-agnostic.
func UpdatesDir(configRoot string) (string, error) {
	if configRoot == "" {
		return "", errors.New("update: empty config root")
	}
	dir := filepath.Join(configRoot, "updates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("update: create %s: %w", dir, err)
	}
	return dir, nil
}
