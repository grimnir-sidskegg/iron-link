//go:build !windows

package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpdatesDir(t *testing.T) {
	root := t.TempDir()
	dir, err := UpdatesDir(root)
	if err != nil {
		t.Fatalf("UpdatesDir: %v", err)
	}
	if dir != filepath.Join(root, "updates") {
		t.Fatalf("dir = %q, want <root>/updates", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want a 0700 directory", info.Mode())
	}
	// Idempotent on a second call.
	if again, err := UpdatesDir(root); err != nil || again != dir {
		t.Fatalf("second call: %q, %v", again, err)
	}
	if _, err := UpdatesDir(""); err == nil {
		t.Fatal("empty config root must be rejected")
	}
}
