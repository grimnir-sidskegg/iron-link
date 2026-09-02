//go:build !windows

package ipc

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestResolveAddr(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}

	// IRON_LINK_SOCKET overrides everything.
	if got := resolveAddr(0, env(map[string]string{"IRON_LINK_SOCKET": "/x/y.sock"})); got != "/x/y.sock" {
		t.Fatalf("override: got %q", got)
	}

	// Unprivileged: $XDG_RUNTIME_DIR/iron-link.sock.
	if got := resolveAddr(1000, env(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"})); got != "/run/user/1000/iron-link.sock" {
		t.Fatalf("xdg: got %q", got)
	}

	// Root via sudo: bind in the INVOKING user's runtime dir, NOT root's XDG —
	// the branch fires only if that dir exists, so use the test user's own.
	uid := os.Getuid()
	rd := filepath.Join("/run/user", strconv.Itoa(uid))
	if fi, err := os.Stat(rd); err == nil && fi.IsDir() {
		got := resolveAddr(0, env(map[string]string{
			"SUDO_UID":        strconv.Itoa(uid),
			"XDG_RUNTIME_DIR": "/run/user/0", // root's — must be IGNORED
		}))
		if want := filepath.Join(rd, "iron-link.sock"); got != want {
			t.Fatalf("sudo-root: got %q want %q", got, want)
		}
	}
}

func TestSocketMode(t *testing.T) {
	cases := []struct {
		name       string
		euid, egid int
		underSudo  bool
		want       os.FileMode
	}{
		// Unprivileged daemon: owner-only.
		{"unprivileged", 1000, 1000, false, 0o600},
		// Root via sudo: chowned to the invoking user, perms stay owner-only.
		{"sudo-root", 0, 0, true, 0o600},
		// System service: bare root under a dedicated group → group-accessible.
		{"system-service", 0, 970, false, 0o660},
		// Interactive root (no service group): group is root, no widening.
		{"bare-root", 0, 0, false, 0o600},
	}
	for _, c := range cases {
		if got := socketMode(c.euid, c.egid, c.underSudo); got != c.want {
			t.Errorf("%s: socketMode(%d,%d,%v) = %o, want %o", c.name, c.euid, c.egid, c.underSudo, got, c.want)
		}
	}
}

func TestListenUnixPermsAndStale(t *testing.T) {
	addr := filepath.Join(t.TempDir(), "d.sock")

	l, err := Listen(addr)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	fi, err := os.Stat(addr)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket perm = %o, want 600", perm)
	}
	c, err := net.Dial("unix", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.Close()
	l.Close()

	// A stale socket file from the previous bind must not block re-listening.
	l2, err := Listen(addr)
	if err != nil {
		t.Fatalf("re-listen over stale socket: %v", err)
	}
	l2.Close()
}
