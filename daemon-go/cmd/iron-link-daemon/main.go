// Command iron-link-daemon is the privileged Go service that embeds sing-box +
// xray and serves the Flutter client over a peer-authenticated local IPC.
//
// It runs three ways: in the FOREGROUND (interactive / Unix, until SIGINT or
// SIGTERM); as a WINDOWS SERVICE (launched by the SCM — see service_windows.go);
// or as a one-shot install/uninstall/start/stop subcommand (Windows only).
// Build with `-tags "with_gvisor,with_utls,with_clash_api,with_quic"` (TUN
// stack + native REALITY + QUIC outbounds).
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"ironlink/daemon/internal/ipc"
	"ironlink/daemon/internal/store"
)

// version is the build-time stamp, set with
// `-ldflags "-X main.version=vX.Y.Z"` by CI and the packaging scripts. A plain
// `go build` leaves "dev" — Go's automatic VCS stamping is no substitute here
// (the repo root has no go.mod, so the module version reads "(devel)").
var version = "dev"

func main() {
	// The version flag must short-circuit BEFORE the Windows service dispatch:
	// maybeRunWindows treats ANY argument as a service subcommand and errors on
	// unknown ones, so this check runs first on all OSes.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-version", "version":
			fmt.Println(versionString())
			return
		}
	}

	// On Windows this runs the SCM service loop or an install/uninstall/start/
	// stop subcommand; elsewhere (and for an interactive Windows run) it returns
	// handled=false and we fall through to the foreground path below.
	if handled, err := maybeRunWindows(); handled || err != nil {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iron-link-daemon:", err)
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "iron-link-daemon:", err)
		os.Exit(1)
	}
}

// versionString renders the one-line `--version` output: the stamped version,
// plus the VCS revision when the build was unstamped (a dev build still
// identifies the commit it came from, when the toolchain recorded one).
func versionString() string {
	v := version
	if v == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" && s.Value != "" {
					rev := s.Value
					if len(rev) > 12 {
						rev = rev[:12]
					}
					v = "dev (" + rev + ")"
					break
				}
			}
		}
	}
	return "iron-link-daemon " + v
}

// run is the daemon core: open the store, listen on the control endpoint, and
// serve until ctx is cancelled (a signal interactively, or the SCM Stop as a
// service), then shut the session down. Status lines go to logw (stderr in the
// foreground, a log file under the config dir as a service — a service has no
// console).
func run(ctx context.Context, logw io.Writer) error {
	st, err := store.Open()
	if err != nil {
		return err
	}
	addr := ipc.DefaultAddr()
	l, err := ipc.Listen(addr)
	if err != nil {
		return err
	}
	defer l.Close()

	m := newManager(st)
	m.applyUpdateURLOverride(os.Getenv, logw)
	hub := ipc.NewHub(m.Snapshot)
	m.hub = hub
	srv := ipc.NewServer(ipc.HandlerFunc(m.Handle), hub)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(l) }()
	fmt.Fprintf(logw, "%s listening at %s (%s)\n",
		versionString(), addr, time.Now().UTC().Format(time.RFC3339))

	// Restore the previous session (best-effort, async — activation can take
	// seconds and must not block the control plane).
	go m.restoreLastSession()
	// The background update check (jittered daily, fail-soft, gated by the
	// auto_update / transport settings at every tick).
	go m.updateLoop(ctx)

	select {
	case <-ctx.Done():
		// Graceful shutdown: deferred l.Close() unblocks Serve; a running
		// session must not outlive the daemon (it holds the TUN/auto_route)
		// but the last-session record SURVIVES so the next start restores it.
		fmt.Fprintf(logw, "iron-link-daemon stopping (%s)\n", time.Now().UTC().Format(time.RFC3339))
		m.shutdown()
		return nil
	case err := <-errc:
		return err
	}
}
