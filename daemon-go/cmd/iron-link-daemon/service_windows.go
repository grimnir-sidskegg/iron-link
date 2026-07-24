//go:build windows

// Windows service support: the daemon registers as an auto-start service that
// runs as LocalSystem (TUN needs Administrator) with no console window. The
// installer invokes the install/start subcommands; the unprivileged Flutter
// client talks to the same named pipe as always.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"ironlink/daemon/internal/store"
)

const (
	serviceName        = "iron-link"
	serviceDisplayName = "iron-link daemon"
	serviceDescription = "iron-link proxy/VPN daemon (embedded sing-box + xray)."
)

// maybeRunWindows dispatches the Windows entry points: an install/uninstall/
// start/stop subcommand, or — when the SCM launched us — the service run loop.
// It returns handled=false for an ordinary interactive run (e.g. a developer
// double-clicking the exe, or the live-smoke spawning it), so main falls
// through to the foreground path.
func maybeRunWindows() (bool, error) {
	if len(os.Args) > 1 {
		return true, handleServiceCommand(os.Args[1:])
	}
	inService, err := svc.IsWindowsService()
	if err != nil {
		return true, fmt.Errorf("detect service mode: %w", err)
	}
	if !inService {
		return false, nil
	}
	return true, runService()
}

func handleServiceCommand(args []string) error {
	switch args[0] {
	case "install":
		return installService(args[1:])
	case "uninstall":
		return uninstallService()
	case "start":
		return startService()
	case "stop":
		return stopService()
	default:
		return fmt.Errorf("unknown command %q (want install|uninstall|start|stop)", args[0])
	}
}

// runService runs under the SCM. A service has no console, so the daemon's
// status/log output goes to <config-dir>\daemon.log (install pointed
// IRON_LINK_CONFIG_DIR at the user's profile).
func runService() error {
	logw, closeLog := serviceLog()
	defer closeLog()
	return svc.Run(serviceName, &ironLinkService{logw: logw})
}

type ironLinkService struct{ logw io.Writer }

// Execute is the SCM handler: start the daemon core, report Running, and run it
// until the SCM sends Stop/Shutdown (which cancels the context and lets run()
// tear the session down).
func (s *ironLinkService) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- run(ctx, s.logw) }()

	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				<-errc // let run() finish its graceful shutdown
				return false, 0
			}
		case err := <-errc:
			// run() exited on its own (e.g. the listener failed).
			if err != nil {
				fmt.Fprintln(s.logw, "iron-link-daemon: service exited:", err)
				return false, 1
			}
			return false, 0
		}
	}
}

func serviceLog() (io.Writer, func()) {
	dir, err := store.DefaultDir()
	if err != nil {
		return os.Stderr, func() {}
	}
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "daemon.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return os.Stderr, func() {}
	}
	return f, func() { _ = f.Close() }
}

// installService registers the auto-start service. configDir (default: the
// invoking user's config dir, since install runs in the elevated installer's
// user context) is baked into the service environment so the LocalSystem
// service shares profiles/state with the unprivileged client instead of using
// SYSTEM's %APPDATA%.
func installService(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	configDir := flagValue(args, "--config-dir")
	if configDir == "" {
		if configDir, err = store.DefaultDir(); err != nil {
			return err
		}
	}

	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	if existing, err := m.OpenService(serviceName); err == nil {
		// Already installed (re-install / upgrade): refresh the binary path +
		// environment instead of failing, so the installer is idempotent.
		defer existing.Close()
		if cfg, cerr := existing.Config(); cerr == nil {
			cfg.BinaryPathName = exe
			cfg.StartType = mgr.StartAutomatic
			_ = existing.UpdateConfig(cfg)
		}
		return setServiceEnv(serviceName, []string{"IRON_LINK_CONFIG_DIR=" + configDir})
	}
	srv, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName:  serviceDisplayName,
		Description:  serviceDescription,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	if err := setServiceEnv(serviceName, []string{"IRON_LINK_CONFIG_DIR=" + configDir}); err != nil {
		_ = srv.Delete()
		return fmt.Errorf("set service environment: %w", err)
	}
	return nil
}

func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	srv, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", serviceName)
	}
	defer srv.Close()
	_, _ = srv.Control(svc.Stop) // best-effort; ignore "not running"
	return srv.Delete()
}

func startService() error {
	m, srv, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer srv.Close()
	return srv.Start()
}

func stopService() error {
	m, srv, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer srv.Close()

	status, err := srv.Control(svc.Stop)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for status.State != svc.Stopped {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %q to stop", serviceName)
		}
		time.Sleep(300 * time.Millisecond)
		if status, err = srv.Query(); err != nil {
			return err
		}
	}
	return nil
}

func openService() (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, err
	}
	srv, err := m.OpenService(serviceName)
	if err != nil {
		m.Disconnect()
		return nil, nil, fmt.Errorf("service %q is not installed", serviceName)
	}
	return m, srv, nil
}

// setServiceEnv writes the service's per-service Environment (REG_MULTI_SZ of
// NAME=VALUE entries) — the SCM has no API for it, so it goes to the registry.
func setServiceEnv(name string, env []string) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\`+name, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringsValue("Environment", env)
}

func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}
