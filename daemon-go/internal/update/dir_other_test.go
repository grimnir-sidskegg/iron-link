//go:build !windows

package update

import "testing"

// Non-Windows platforms are notify-only: UpdatesDir must refuse rather than
// resolve a staging directory (in particular, never one under the
// user-writable config root — the daemon runs as root).
func TestUpdatesDirRefuses(t *testing.T) {
	if dir, err := UpdatesDir(t.TempDir()); err == nil {
		t.Fatalf("UpdatesDir = %q, want an error on a notify-only platform", dir)
	}
	if dir, err := UpdatesDir(""); err == nil {
		t.Fatalf("UpdatesDir(\"\") = %q, want an error", dir)
	}
}
