package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ironlink/daemon/internal/api"
)

func settingsStore(t *testing.T) *Store {
	t.Helper()
	return OpenAt(t.TempDir())
}

func TestLoadSettingsMissingFileYieldsDefaults(t *testing.T) {
	s := settingsStore(t)
	got, err := s.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	want := DefaultSettings()
	if got.LogLevel != want.LogLevel || got.SocksPort != want.SocksPort ||
		len(got.LanBypass.CIDRs) != len(want.LanBypass.CIDRs) {
		t.Fatalf("missing file should yield defaults, got %+v", got)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s := settingsStore(t)
	in := DefaultSettings()
	in.LogLevel = "debug"
	in.IPVersion = "v4"
	in.DNS = api.DNSSettings{
		Servers:   []api.DNSServer{{Type: "udp", Address: "1.1.1.1"}},
		ViaTunnel: true,
	}
	in.SocksPort = 1080
	in.Tun.MTU = 9000
	if err := s.SaveSettings(in); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	out, err := s.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if out.LogLevel != "debug" || out.IPVersion != "v4" || !out.DNS.ViaTunnel ||
		out.DNS.Servers[0].Address != "1.1.1.1" || out.SocksPort != 1080 ||
		out.Tun.MTU != 9000 {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestLoadSettingsPartialFileKeepsDefaults(t *testing.T) {
	s := settingsStore(t)
	partial := `{"log_level": "warn", "dns": {"servers": [{"type":"udp","address":"9.9.9.9"}]}}`
	if err := os.WriteFile(filepath.Join(s.Dir(), "settings.json"), []byte(partial), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if got.LogLevel != "warn" {
		t.Fatalf("explicit field lost: %+v", got)
	}
	if got.DNS.Servers[0].Address != "9.9.9.9" {
		t.Fatalf("server list not replaced: %+v", got.DNS)
	}
	want := DefaultSettings()
	if got.SocksPort != want.SocksPort || got.Tun.MTU != want.Tun.MTU ||
		got.SubscriptionUserAgent != want.SubscriptionUserAgent {
		t.Fatalf("omitted fields should keep defaults: %+v", got)
	}
}

func TestSaveSettingsFilePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no POSIX mode bits; atomicWrite skips Chmod there and
		// Stat reports 0666 for any writable file. Nothing to assert.
		t.Skip("file mode bits are not honored on Windows")
	}
	s := settingsStore(t)
	if err := s.SaveSettings(DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(s.Dir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("settings.json perms = %o, want 600", perm)
	}
}

func TestValidateSettingsRejections(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*api.Settings)
		wantSub string
	}{
		{"log level", func(s *api.Settings) { s.LogLevel = "verbose" }, "log_level"},
		{"ip version", func(s *api.Settings) { s.IPVersion = "ipv4" }, "ip_version"},
		{"no dns servers", func(s *api.Settings) { s.DNS.Servers = nil }, "at least one"},
		{"dns type", func(s *api.Settings) { s.DNS.Servers[0].Type = "doq" }, "server type"},
		{"dns hostname", func(s *api.Settings) { s.DNS.Servers[0].Address = "dns.google" }, "literal IP"},
		{"bad cidr", func(s *api.Settings) { s.LanBypass.CIDRs = []string{"10.0.0.0/33"} }, "cidr"},
		{"empty ua", func(s *api.Settings) { s.SubscriptionUserAgent = "" }, "user_agent"},
		{"port range", func(s *api.Settings) { s.SocksPort = 0 }, "socks_port"},
		{"mtu range", func(s *api.Settings) { s.Tun.MTU = 100 }, "mtu"},
		{"tun stack", func(s *api.Settings) { s.Tun.Stack = "netstack" }, "stack"},
		{"probe url", func(s *api.Settings) { s.LatencyProbe.URL = "ftp://x" }, "url"},
		{"probe budget", func(s *api.Settings) { s.LatencyProbe.BudgetSecs = 1 }, "budget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := DefaultSettings()
			tc.mutate(&s)
			err := ValidateSettings(s)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("want error containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestDefaultSettingsAreValid(t *testing.T) {
	if err := ValidateSettings(DefaultSettings()); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

// (a) A fresh install's restore-on-start default is per-OS: OFF on Windows
// (auto-start LocalSystem service), ON everywhere else. The decision is a
// pure function, so both branches are asserted from any host.
func TestDefaultRestoreOnStartIsOffOnWindows(t *testing.T) {
	if defaultRestoreOnStart("windows") {
		t.Fatal("restore-on-start default must be OFF on windows")
	}
	for _, goos := range []string{"linux", "darwin"} {
		if !defaultRestoreOnStart(goos) {
			t.Fatalf("restore-on-start default must be ON on %s", goos)
		}
	}
	// The wired-in default follows the build target (runtime.GOOS).
	if got, want := DefaultSettings().RestoreOnStart, runtime.GOOS != "windows"; got != want {
		t.Fatalf("DefaultSettings().RestoreOnStart = %v on %s, want %v", got, runtime.GOOS, want)
	}
}

// (b) An explicit persisted choice must survive a load regardless of the
// per-OS default: LoadSettings decodes the file over DefaultSettings, and
// SaveSettings always writes the key (no omitempty), so a present value wins.
func TestLoadSettingsRestoreOnStartExplicitValueSurvives(t *testing.T) {
	for _, explicit := range []bool{true, false} {
		s := settingsStore(t)
		doc := DefaultSettings()
		doc.RestoreOnStart = explicit
		if err := s.SaveSettings(doc); err != nil {
			t.Fatalf("SaveSettings: %v", err)
		}
		got, err := s.LoadSettings()
		if err != nil {
			t.Fatalf("LoadSettings: %v", err)
		}
		if got.RestoreOnStart != explicit {
			t.Fatalf("persisted restore_on_start=%v not preserved, got %v", explicit, got.RestoreOnStart)
		}
	}
}
