package main

import (
	"encoding/json"
	"testing"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/store"
)

func settingsManager(t *testing.T) *manager {
	t.Helper()
	return newManager(store.OpenAt(t.TempDir()))
}

func TestGetSettingsFreshStoreReturnsDefaults(t *testing.T) {
	m := settingsManager(t)
	resp := m.Handle(api.Request{Command: api.CmdGetSettings})
	if resp.Status != api.StatusSettings {
		t.Fatalf("status = %s (%s)", resp.Status, resp.Message)
	}
	want := store.DefaultSettings()
	if resp.Settings == nil || resp.Settings.SocksPort != want.SocksPort ||
		resp.Settings.LogLevel != want.LogLevel {
		t.Fatalf("expected defaults, got %+v", resp.Settings)
	}
	if resp.NeedsReactivation {
		t.Fatal("get must never flag reactivation")
	}
}

func TestSetSettingsPersistsAndReadsBack(t *testing.T) {
	m := settingsManager(t)
	doc := store.DefaultSettings()
	doc.LogLevel = "debug"
	doc.DNS.Servers = []api.DNSServer{{Type: "udp", Address: "9.9.9.9"}}

	resp := m.Handle(api.Request{Command: api.CmdSetSettings, Settings: &doc})
	if resp.Status != api.StatusSettings {
		t.Fatalf("set failed: %s (%s)", resp.Status, resp.Message)
	}
	if resp.NeedsReactivation {
		t.Fatal("no session is running — reactivation flag must be off")
	}

	got := m.Handle(api.Request{Command: api.CmdGetSettings})
	if got.Settings.LogLevel != "debug" || got.Settings.DNS.Servers[0].Address != "9.9.9.9" {
		t.Fatalf("set did not persist: %+v", got.Settings)
	}
}

// TestSetSettingsWithoutUpdateKeysKeepsDefaults: a set_settings from a
// client that predates the update-check flags carries a settings document
// WITHOUT those keys. The wire decode lands on a zero Request (not on the
// stored file), so without the UnmarshalJSON preset one such save would
// silently persist all three flags as false. Absent keys must keep the
// default ON; an explicitly false key must still stick.
func TestSetSettingsWithoutUpdateKeysKeepsDefaults(t *testing.T) {
	m := settingsManager(t)
	data, err := json.Marshal(store.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "auto_update")
	delete(raw, "update_via_tunnel")
	delete(raw, "update_via_direct")
	send := func(doc map[string]json.RawMessage) api.Settings {
		t.Helper()
		body, err := json.Marshal(map[string]any{"command": api.CmdSetSettings, "settings": doc})
		if err != nil {
			t.Fatal(err)
		}
		var req api.Request
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if resp := m.Handle(req); resp.Status != api.StatusSettings {
			t.Fatalf("set_settings: %s (%s)", resp.Status, resp.Message)
		}
		got, err := m.store.LoadSettings()
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	got := send(raw)
	if !got.AutoUpdate || !got.UpdateViaTunnel || !got.UpdateViaDirect {
		t.Fatalf("absent update keys must keep the default ON, got %+v", got)
	}

	raw["auto_update"] = json.RawMessage("false")
	if got := send(raw); got.AutoUpdate || !got.UpdateViaTunnel || !got.UpdateViaDirect {
		t.Fatalf("an explicit false must override the preset, got %+v", got)
	}
}

func TestSetSettingsRejectsInvalidDocument(t *testing.T) {
	m := settingsManager(t)
	doc := store.DefaultSettings()
	doc.SocksPort = -1
	resp := m.Handle(api.Request{Command: api.CmdSetSettings, Settings: &doc})
	if resp.Status != api.StatusError {
		t.Fatalf("invalid document accepted: %s", resp.Status)
	}
	if resp := m.Handle(api.Request{Command: api.CmdSetSettings}); resp.Status != api.StatusError {
		t.Fatalf("missing document accepted: %s", resp.Status)
	}
}

func TestEngineRelevantChanged(t *testing.T) {
	base := store.DefaultSettings()

	live := base
	live.RestoreOnStart = false
	live.SubscriptionUserAgent = "other/1.0"
	live.LatencyProbe.BudgetSecs = 10
	live.AutoUpdate = false
	live.UpdateViaTunnel = false
	live.UpdateViaDirect = false
	if engineRelevantChanged(base, live) {
		t.Fatal("live-applied fields must not flag reactivation")
	}

	engine := base
	engine.DNS.ViaTunnel = true
	if !engineRelevantChanged(base, engine) {
		t.Fatal("a DNS change must flag reactivation")
	}
	engine = base
	engine.Tun.MTU = 9000
	if !engineRelevantChanged(base, engine) {
		t.Fatal("a TUN change must flag reactivation")
	}
}

func TestTunablesFromSettingsLanBypass(t *testing.T) {
	s := store.DefaultSettings()
	s.LanBypass.Enabled = false
	tun := tunablesFromSettings(s)
	if tun.LanBypass == nil || len(tun.LanBypass) != 0 {
		t.Fatalf("disabled bypass must map to an explicit empty list, got %#v", tun.LanBypass)
	}

	s.LanBypass = api.LanBypass{Enabled: true, CIDRs: nil}
	if tun := tunablesFromSettings(s); tun.LanBypass == nil {
		t.Fatalf("enabled bypass with an emptied list must stay non-nil")
	}
}
