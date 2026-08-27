package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	"ironlink/daemon/internal/api"
)

// Daemon-global settings (`settings.json` at the config root). The
// defaults reproduce the values that were hardcoded before settings
// existed, so an absent or partial file is behavior-neutral.

// defaultRestoreOnStart reports the per-OS default for the "restore the last
// session on daemon start" knob. It is OFF on Windows and ON everywhere else:
// on Windows the daemon runs as an auto-start LocalSystem service, so a
// session that resurrects itself at boot surprises users who expect the VPN
// active only while the app/tray is on screen. Only the DEFAULT-when-unset
// differs by OS — an explicit persisted value always wins, because
// LoadSettings decodes settings.json over this document. goos is passed in
// (runtime.GOOS at the call site) so both branches are testable from any host.
func defaultRestoreOnStart(goos string) bool {
	return goos != "windows"
}

// DefaultSettings returns the document every field falls back to.
func DefaultSettings() api.Settings {
	return api.Settings{
		LogLevel:  "info",
		IPVersion: "both",
		DNS: api.DNSSettings{
			Strategy: "prefer_ipv4",
			Servers:  []api.DNSServer{{Type: "tls", Address: "8.8.8.8"}},
		},
		LanBypass: api.LanBypass{
			Enabled: true,
			// Link-local / ULA only. RFC1918 IPv4 ranges are handled by the
			// immutable private-direct route rule, not this editable exclude
			// list, so DNS to a LAN resolver is hijacked rather than leaked.
			CIDRs: []string{
				"169.254.0.0/16", "fc00::/7", "fe80::/10",
			},
		},
		RestoreOnStart:        defaultRestoreOnStart(runtime.GOOS),
		SubscriptionUserAgent: "v2rayN/6.23",
		SocksPort:             10808,
		Tun:                   api.TunSettings{MTU: 1500, Stack: "mixed", StrictRoute: true},
		LatencyProbe: api.ProbeSettings{
			URL:        "https://www.gstatic.com/generate_204",
			BudgetSecs: 45,
		},
		// The update check defaults ON, tunnel-first with a direct fallback.
		// A document that omits the keys — an older settings.json OR an older
		// client's set_settings — keeps these defaults; the preset lives in
		// api.Settings.UnmarshalJSON so both decode paths are covered.
		AutoUpdate:      true,
		UpdateViaTunnel: true,
		UpdateViaDirect: true,
	}
}

// ValidateSettings rejects a document the engine could not compile or
// that would brick the daemon's own plumbing.
func ValidateSettings(s api.Settings) error {
	if !oneOf(s.LogLevel, "error", "warn", "info", "debug") {
		return fmt.Errorf("settings: unknown log_level %q", s.LogLevel)
	}
	if !oneOf(s.IPVersion, "v4", "v6", "both") {
		return fmt.Errorf("settings: unknown ip_version %q", s.IPVersion)
	}
	// Empty = the default (prefer_ipv4): a pre-strategy client / partial file
	// omits the key and decodes over the default document.
	if !oneOf(s.DNS.Strategy, "", "prefer_ipv4", "prefer_ipv6", "ipv4_only", "ipv6_only") {
		return fmt.Errorf("settings: unknown dns strategy %q", s.DNS.Strategy)
	}
	if len(s.DNS.Servers) == 0 {
		return errors.New("settings: dns needs at least one server")
	}
	for _, srv := range s.DNS.Servers {
		if !oneOf(srv.Type, "udp", "tls", "https") {
			return fmt.Errorf("settings: unknown dns server type %q", srv.Type)
		}
		if _, err := netip.ParseAddr(srv.Address); err != nil {
			return fmt.Errorf("settings: dns server address %q must be a "+
				"literal IP (hostname upstreams are not supported yet)", srv.Address)
		}
	}
	for _, cidr := range s.LanBypass.CIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("settings: lan_bypass cidr %q: %v", cidr, err)
		}
	}
	if s.SubscriptionUserAgent == "" {
		return errors.New("settings: subscription_user_agent must not be empty")
	}
	if s.SocksPort < 1 || s.SocksPort > 65535 {
		return fmt.Errorf("settings: socks_port %d out of range", s.SocksPort)
	}
	if s.Tun.MTU < 576 || s.Tun.MTU > 65535 {
		return fmt.Errorf("settings: tun mtu %d out of range (576..65535)", s.Tun.MTU)
	}
	if !oneOf(s.Tun.Stack, "system", "gvisor", "mixed") {
		return fmt.Errorf("settings: unknown tun stack %q", s.Tun.Stack)
	}
	if u, err := url.Parse(s.LatencyProbe.URL); err != nil ||
		(u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("settings: latency_probe url %q must be http(s)", s.LatencyProbe.URL)
	}
	if s.LatencyProbe.BudgetSecs < 5 || s.LatencyProbe.BudgetSecs > 300 {
		return fmt.Errorf("settings: latency_probe budget_secs %d out of range (5..300)",
			s.LatencyProbe.BudgetSecs)
	}
	return nil
}

func oneOf(v string, set ...string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

func (s *Store) settingsPath() string { return filepath.Join(s.baseDir, "settings.json") }

// LoadSettings reads settings.json; a missing file means defaults, and a
// PARTIAL file keeps defaults for the fields it omits (decode over the
// default document).
func (s *Store) LoadSettings() (api.Settings, error) {
	data, err := os.ReadFile(s.settingsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return api.Settings{}, fmt.Errorf("read settings: %w", err)
	}
	settings := DefaultSettings()
	if err := json.Unmarshal(data, &settings); err != nil {
		return api.Settings{}, fmt.Errorf("decode settings: %w", err)
	}
	if err := ValidateSettings(settings); err != nil {
		return api.Settings{}, err
	}
	return settings, nil
}

// SaveSettings validates and atomically persists the document.
func (s *Store) SaveSettings(settings api.Settings) error {
	if err := ValidateSettings(settings); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	return atomicWrite(s.settingsPath(), data)
}
