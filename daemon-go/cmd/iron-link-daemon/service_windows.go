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

	"golang.org/x/sys/windows"
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
// IRON_LINK_CONFIG_DIR at the user's profile). Panics and anything else
// written to stderr go there too: os.Stderr covers Go code, but the runtime
// prints fatal errors through GetStdHandle(STD_ERROR_HANDLE), so the handle
// itself is redirected too. The file is never closed: os.Stderr
// points at it until the process exits, and main reports svc.Run's error
// through os.Stderr after this function returns.
func runService() error {
	var logw io.Writer = os.Stderr
	if f := serviceLog(); f != nil {
		logw = f
		os.Stderr = f
		_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
	}
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

// serviceLog opens <config-dir>\daemon.log for appending. It returns nil when
// the directory or the file cannot be opened; the caller then keeps stderr.
func serviceLog() *os.File {
	dir, err := store.DefaultDir()
	if err != nil {
		return nil
	}
	_ = os.MkdirAll(dir, 0o755)
	f, _ := os.OpenFile(filepath.Join(dir, "daemon.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	return f
}

// installService registers the auto-start service. configDir (default: the
// invoking user's config dir, since install runs in the elevated installer's
// user context) is baked into the service environment so the LocalSystem
// service shares profiles/state with the unprivileged client instead of using
// SYSTEM's %APPDATA%; see serviceEnv for the rest of that environment.
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
		// CreateService quotes the path itself; UpdateConfig writes it as
		// given, so quote it here the same way (the default install dir,
		// Program Files, contains a space).
		defer existing.Close()
		cfg, err := existing.Config()
		if err != nil {
			return fmt.Errorf("read service config: %w", err)
		}
		cfg.BinaryPathName = windows.EscapeArg(exe)
		cfg.StartType = mgr.StartAutomatic
		cfg.DelayedAutoStart = true
		if err := existing.UpdateConfig(cfg); err != nil {
			return fmt.Errorf("update service config: %w", err)
		}
		if err := hardenService(existing); err != nil {
			return err
		}
		return setServiceEnv(serviceName, serviceEnv(configDir))
	}
	// DelayedAutoStart: the SCM starts the service after the other auto-start
	// services, outside the boot-time start window, from the next boot on.
	srv, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName:      serviceDisplayName,
		Description:      serviceDescription,
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
		ErrorControl:     mgr.ErrorNormal,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	if err := setServiceEnv(serviceName, serviceEnv(configDir)); err != nil {
		_ = srv.Delete()
		return fmt.Errorf("set service environment: %w", err)
	}
	if err := hardenService(srv); err != nil {
		_ = srv.Delete()
		return err
	}
	return nil
}

// hardenService sets the SCM failure actions: restart the service after a
// crash, and (with the non-crash flag) after an exit with a non-zero code.
// The SCM repeats the last action for every failure past the list, so the
// last delay is the generous one; the reset period is in seconds and clears
// the failure count after a day without failures. The non-crash flag is
// read by the SCM at the next boot.
func hardenService(srv *mgr.Service) error {
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	if err := srv.SetRecoveryActions(actions, 86400); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}
	if err := srv.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("set recovery on non-crash failures: %w", err)
	}
	return nil
}

// serviceEnv is the per-service environment install bakes: the config dir,
// plus the update manifest URL override when the installing process
// carries one (pre-publish testing against a local manifest; see
// manager_update.go). A re-install without it clears a previously baked
// override.
func serviceEnv(configDir string) []string {
	env := []string{"IRON_LINK_CONFIG_DIR=" + configDir}
	if v := os.Getenv(updateURLEnv); v != "" {
		env = append(env, updateURLEnv+"="+v)
	}
	return env
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
	// Disable first (best-effort) so a recovery restart queued by the SCM
	// cannot bring the service back between Stop and Delete.
	if cfg, err := srv.Config(); err == nil {
		cfg.StartType = mgr.StartDisabled
		_ = srv.UpdateConfig(cfg)
	}
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
