// The settings verbs: the daemon-global settings.json document, owned by
// the daemon (clients never touch the file). set_settings takes the FULL
// document; engine-relevant changes apply at the NEXT activation and are
// flagged with needs_reactivation while a session runs.
package main

import (
	"reflect"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/engine"
)

func (m *manager) getSettings() api.Response {
	m.mu.Lock()
	defer m.mu.Unlock()
	settings, err := m.store.LoadSettings()
	if err != nil {
		return errResp(err.Error())
	}
	return api.Response{Status: api.StatusSettings, Settings: &settings}
}

func (m *manager) setSettings(req api.Request) api.Response {
	if req.Settings == nil {
		return errResp("set_settings requires a settings document")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old, err := m.store.LoadSettings()
	if err != nil {
		return errResp(err.Error())
	}
	if err := m.store.SaveSettings(*req.Settings); err != nil {
		return errResp(err.Error())
	}
	resp := api.Response{Status: api.StatusSettings, Settings: req.Settings}
	if m.sess != nil && engineRelevantChanged(old, *req.Settings) {
		resp.NeedsReactivation = true
	}
	return resp
}

// engineRelevantChanged reports whether a field that is compiled into the
// running session differs. RestoreOnStart, the subscription UA, and the
// probe settings apply live — they are neutralized before comparing.
func engineRelevantChanged(a, b api.Settings) bool {
	a.RestoreOnStart, b.RestoreOnStart = false, false
	a.SubscriptionUserAgent, b.SubscriptionUserAgent = "", ""
	a.LatencyProbe, b.LatencyProbe = api.ProbeSettings{}, api.ProbeSettings{}
	return !reflect.DeepEqual(a, b)
}

// subscriptionUA resolves the fetch User-Agent from settings (applies
// live — no re-activation involved). An unreadable settings file falls
// back to the default UA rather than failing the refresh.
func (m *manager) subscriptionUA() string {
	if s, err := m.store.LoadSettings(); err == nil {
		return s.SubscriptionUserAgent
	}
	return ""
}

// tunablesFromSettings maps the stored document to the engine's view.
func tunablesFromSettings(s api.Settings) engine.Tunables {
	dns := make([]engine.DNSUpstream, len(s.DNS.Servers))
	for i, srv := range s.DNS.Servers {
		dns[i] = engine.DNSUpstream{Type: srv.Type, Address: srv.Address}
	}
	// Always a non-nil slice: nil tells the engine "use defaults", which
	// must not happen for an explicit document (disabled = empty, enabled
	// with an emptied list = empty too).
	lanBypass := []string{}
	if s.LanBypass.Enabled {
		lanBypass = append(lanBypass, s.LanBypass.CIDRs...)
	}
	return engine.Tunables{
		LogLevel:     s.LogLevel,
		IPVersion:    s.IPVersion,
		DNSStrategy:  s.DNS.Strategy,
		DNS:          dns,
		DNSViaTunnel: s.DNS.ViaTunnel,
		LanBypass:    lanBypass,
		TunMTU:       s.Tun.MTU,
		TunStack:     s.Tun.Stack,
		StrictRoute:  s.Tun.StrictRoute,
	}
}
