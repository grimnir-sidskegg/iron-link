//go:build !windows

package ipc

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// DefaultAddr is the control socket path. Override with IRON_LINK_SOCKET (used by
// tests and side-by-side runs).
//
// Otherwise it follows the established per-user socket path convention so the
// client — which computes the path the same way — finds this daemon unchanged:
// prefer $XDG_RUNTIME_DIR/iron-link.sock (per-user, tmpfs, cleared on logout),
// else <config-root>/iron-link.sock.
//
// SPECIAL CASE — elevated for TUN via sudo: TUN needs CAP_NET_ADMIN, so the
// daemon is typically started with `sudo`. Under sudo $XDG_RUNTIME_DIR is
// unreliable (often cleared, or root's), so when running as root we bind in the
// INVOKING user's runtime dir `/run/user/$SUDO_UID/` — exactly where that user's
// unprivileged client looks — and Listen() chowns the socket back to them. This
// is what lets the client stay unprivileged while the daemon is root.
func DefaultAddr() string {
	return resolveAddr(os.Geteuid(), os.Getenv)
}

// resolveAddr is DefaultAddr's testable core (euid + env injected).
func resolveAddr(euid int, getenv func(string) string) string {
	if p := getenv("IRON_LINK_SOCKET"); p != "" {
		return p
	}
	// Elevated via sudo: target the invoking user's runtime dir explicitly
	// rather than trust sudo's env handling of XDG_RUNTIME_DIR.
	if euid == 0 {
		if uid := getenv("SUDO_UID"); uid != "" {
			rd := filepath.Join("/run/user", uid)
			if fi, err := os.Stat(rd); err == nil && fi.IsDir() {
				return filepath.Join(rd, "iron-link.sock")
			}
		}
	}
	if dir := getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "iron-link.sock")
	}
	return filepath.Join(configRoot(), "iron-link.sock")
}

// configRoot is the per-user config root: $IRON_LINK_CONFIG_DIR if
// set (used as-is), else the platform config dir joined with "iron-link" —
// Linux $XDG_CONFIG_HOME or ~/.config, macOS ~/Library/Application Support.
func configRoot() string {
	if d := os.Getenv("IRON_LINK_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "iron-link")
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "iron-link")
	}
	return filepath.Join(home, ".config", "iron-link")
}

// Listen binds the control UDS at addr. It creates the parent dir (0700),
// removes a stale socket from a previous run, and tightens the socket perms
// (see socketMode).
//
// When running elevated for TUN (euid 0 under sudo), it ALSO chowns the socket
// to the invoking user (SUDO_UID/SUDO_GID): otherwise the socket is root:root
// 0600 and the user's unprivileged client cannot connect — forcing it to run
// as root too, which is exactly what we avoid. 0600 + this chown means
// only that user (and root) can drive the privileged daemon — the auth IS the
// filesystem ownership.
//
// As a system service (bare root, no SUDO_UID) the unit runs us under a
// dedicated group (systemd Group=iron-link), so the socket is created owned by
// that group; socketMode then widens it to 0660 and the auth is group
// membership — the unprivileged client, a member of that group, connects while
// the socket stays unreachable to everyone else.
func Listen(addr string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(addr), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	l, err := net.Listen("unix", addr)
	if err != nil {
		return nil, err
	}
	uid, gid, underSudo := sudoIDs(os.Getenv)
	if err := os.Chmod(addr, socketMode(os.Geteuid(), os.Getegid(), underSudo)); err != nil {
		l.Close()
		return nil, err
	}
	if os.Geteuid() == 0 && underSudo {
		if err := os.Chown(addr, uid, gid); err != nil {
			l.Close()
			return nil, fmt.Errorf("chown control socket to invoking user %d:%d: %w", uid, gid, err)
		}
	}
	return l, nil
}

// socketMode picks the control-socket permission bits. Default 0600 (owner
// only). When running as a bare-root system service under a dedicated non-root
// group (systemd Group=iron-link → egid is that group), it widens to 0660 so a
// member of the group — the unprivileged client — can reach the root daemon.
// Interactive root (egid 0 → group is root, so 0660 grants nothing extra) and
// the sudo path (we chown the socket to the invoking user instead) keep 0600.
func socketMode(euid, egid int, underSudo bool) os.FileMode {
	if euid == 0 && !underSudo && egid != 0 {
		return 0o660
	}
	return 0o600
}

// sudoIDs returns the numeric uid/gid of the user that invoked sudo, from
// SUDO_UID/SUDO_GID. ok is false when not running under sudo (a bare-root
// system service has no invoking user — its socket ownership is the unit's
// concern, e.g. a User= directive).
func sudoIDs(getenv func(string) string) (uid, gid int, ok bool) {
	us, gs := getenv("SUDO_UID"), getenv("SUDO_GID")
	if us == "" || gs == "" {
		return 0, 0, false
	}
	u, err1 := strconv.Atoi(us)
	g, err2 := strconv.Atoi(gs)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return u, g, true
}
