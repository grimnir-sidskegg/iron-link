//go:build with_utls

// Manager-level update-check wiring, offline: the auto_update gate, the
// per-attempt transport chooser, the cached result under mu, and the
// settings round trip. The manifest is the update package's SIGNED fixture
// (its signature verifies against the compiled-in dev current key) served
// through a stubbed transport — nothing is dialed. Tagged: the chooser
// tests start the REAL session over the fixture's native REALITY member.
package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/update"
)

// fixtureUpdateTransport serves internal/update's signed manifest fixture
// for any URL, counting requests and recording the URLs asked for — no
// sockets involved.
type fixtureUpdateTransport struct {
	t     *testing.T
	calls atomic.Int32
	mu    sync.Mutex
	urls  []string
}

// requested returns the URLs fetched so far, in order.
func (f *fixtureUpdateTransport) requested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.urls...)
}

func (f *fixtureUpdateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.urls = append(f.urls, req.URL.String())
	f.mu.Unlock()
	name := "../../internal/update/testdata/update.json"
	if strings.HasSuffix(req.URL.Path, ".minisig") {
		name += ".minisig"
	}
	data, err := os.ReadFile(name)
	if err != nil {
		f.t.Errorf("read update fixture: %v", err)
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(data)),
		Request:    req,
	}, nil
}

// TestUpdateCheckAutoUpdateGate: auto_update=false means NO fetch at all;
// switched on, the check runs, caches the verified result under mu, and
// persists the accepted seq to the store root.
func TestUpdateCheckAutoUpdateGate(t *testing.T) {
	m := fixtureManager(t)
	m.version = "v1.0.0" // comparable current (the fixture manifest is v1.2.3)
	ft := &fixtureUpdateTransport{t: t}
	m.updateTransport = ft
	m.updateURLs = []string{"https://update.example.com/update.json"}
	// A fixed clock inside the fixture manifest's validity window keeps the
	// Available assertion below deterministic past its expires_at.
	fixedNow := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	m.updateNow = func() time.Time { return fixedNow }

	s, err := m.store.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.AutoUpdate = false
	if err := m.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if m.tryUpdateCheck(context.Background()) {
		t.Error("auto_update=false must not attempt a check")
	}
	if n := ft.calls.Load(); n != 0 {
		t.Fatalf("auto_update=false must not fetch, saw %d requests", n)
	}

	s.AutoUpdate = true
	if err := m.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if !m.tryUpdateCheck(context.Background()) {
		t.Fatal("auto_update=true with a transport must attempt the check")
	}
	if ft.calls.Load() == 0 {
		t.Fatal("no fetch went through the injected transport")
	}
	m.mu.Lock()
	res := m.updateResult
	m.mu.Unlock()
	if res == nil {
		t.Fatal("a verified check must cache its result")
	}
	if res.LatestVersion != "v1.2.3" || res.VerifiedKey != update.KeyCurrent || !res.CheckedAt.Equal(fixedNow) {
		t.Fatalf("cached result: %+v", res)
	}
	// Available proves the manager fed its OWN version into the compare
	// (a broken CurrentVersion would decay to the dev semantics: never
	// available, with every other field above still populated).
	if !res.Available {
		t.Fatal("v1.2.3 must be available over the v1.0.0 current")
	}
	if seq, err := update.NewFileSeqStore(m.store.Dir()).LastSeenSeq(); err != nil || seq != 7 {
		t.Fatalf("persisted seq = %d, %v; want 7 (the fixture's)", seq, err)
	}
}

// TestUpdateAttemptClockPersists: the loop's attempt clock survives a
// restart — noteUpdateAttempt lands in update_state.json and a fresh
// manager over the same config root seeds itself from it, so a rebooted
// service keeps the daily cadence instead of checking right after boot. A
// stamp from the future is discarded.
func TestUpdateAttemptClockPersists(t *testing.T) {
	m := fixtureManager(t)
	m.noteUpdateAttempt()
	m.mu.Lock()
	stamped := m.updateLastAttempt
	m.mu.Unlock()
	if stamped.IsZero() {
		t.Fatal("noteUpdateAttempt must stamp the in-memory clock")
	}
	persisted, err := update.NewFileSeqStore(m.store.Dir()).LastAttempt()
	if err != nil || persisted.IsZero() {
		t.Fatalf("persisted attempt clock = %v, %v", persisted, err)
	}
	if d := stamped.Sub(persisted); d < 0 || d >= time.Second {
		t.Fatalf("persisted %v drifts from stamped %v", persisted, stamped)
	}

	restarted := newManager(m.store)
	restarted.seedUpdateAttemptClock()
	if !restarted.updateLastAttempt.Equal(persisted) {
		t.Fatalf("seeded clock = %v, want the persisted %v", restarted.updateLastAttempt, persisted)
	}

	if err := update.NewFileSeqStore(m.store.Dir()).SetLastAttempt(time.Now().Add(48 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	ahead := newManager(m.store)
	ahead.seedUpdateAttemptClock()
	if !ahead.updateLastAttempt.IsZero() {
		t.Fatalf("a future stamp must be discarded, got %v", ahead.updateLastAttempt)
	}
}

// TestUpdateURLOverrideReachesCheck: the IRON_LINK_UPDATE_URL override
// installed at startup is what a check fetches from — the manifest and its
// signature are requested from the override host, never the production
// mirror — and the verified result is served as usual.
func TestUpdateURLOverrideReachesCheck(t *testing.T) {
	m := fixtureManager(t)
	m.version = "v1.0.0"
	ft := &fixtureUpdateTransport{t: t}
	m.updateTransport = ft
	fixedNow := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	m.updateNow = func() time.Time { return fixedNow }

	const override = "http://198.51.100.1:8000/update.json"
	var logw bytes.Buffer
	m.applyUpdateURLOverride(func(name string) string {
		if name == updateURLEnv {
			return override
		}
		return ""
	}, &logw)
	if !strings.Contains(logw.String(), "overridden by "+updateURLEnv) {
		t.Fatalf("startup log = %q, want the override notice", logw.String())
	}

	resp := m.Handle(api.Request{Command: api.CmdCheckUpdate, Force: true})
	if resp.Status != api.StatusUpdateStatus || resp.UpdateStatus == nil {
		t.Fatalf("check_update: %+v", resp)
	}
	if st := resp.UpdateStatus; !st.Available || st.LatestVersion != "v1.2.3" {
		t.Fatalf("forced status over the override: %+v", st)
	}
	want := []string{override, override + ".minisig"}
	if got := ft.requested(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requested URLs = %q, want %q", got, want)
	}
}

// TestUpdateTransportChooser: tunnel with a live session and via_tunnel on;
// direct otherwise while via_direct is on; nil (skip) when the applicable
// flags are off. tryUpdateCheck recomputes this at every attempt.
func TestUpdateTransportChooser(t *testing.T) {
	m := fixtureManager(t)
	settings, err := m.store.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !settings.AutoUpdate || !settings.UpdateViaTunnel || !settings.UpdateViaDirect {
		t.Fatalf("the update flags must default ON: %+v", settings)
	}

	if tr, mode := chooseUpdateTransport(settings, nil); tr == nil || mode != "direct" {
		t.Fatalf("no session must fall back to direct, got %q", mode)
	}
	noDirect := settings
	noDirect.UpdateViaDirect = false
	if tr, mode := chooseUpdateTransport(noDirect, nil); tr != nil || mode != "" {
		t.Fatalf("no session + via_direct off must skip, got %q", mode)
	}

	// A REAL session (SOCKS mode, nothing dialed) makes tunnel-first win.
	if resp := m.Handle(api.Request{Command: api.CmdActivate}); resp.Status != api.StatusActivated {
		t.Fatalf("activate: %+v", resp)
	}
	defer m.Handle(api.Request{Command: api.CmdStop})
	m.mu.Lock()
	sess := m.sess
	m.mu.Unlock()
	if sess == nil {
		t.Fatal("no session after activate")
	}
	if tr, mode := chooseUpdateTransport(settings, sess); tr == nil || mode != "tunnel" {
		t.Fatalf("session + via_tunnel must pick tunnel, got %q", mode)
	}
	noTunnel := settings
	noTunnel.UpdateViaTunnel = false
	if _, mode := chooseUpdateTransport(noTunnel, sess); mode != "direct" {
		t.Fatalf("via_tunnel off must fall back to direct, got %q", mode)
	}
	noTunnel.UpdateViaDirect = false
	if tr, mode := chooseUpdateTransport(noTunnel, sess); tr != nil || mode != "" {
		t.Fatalf("both flags off must disable the transport, got %q", mode)
	}
}

// TestUpdateSettingsRoundTrip: the three flags persist through the
// set_settings/get_settings verbs, and toggling ONLY them while a session
// runs must not demand a re-activation (they apply live).
func TestUpdateSettingsRoundTrip(t *testing.T) {
	m := fixtureManager(t)
	if resp := m.Handle(api.Request{Command: api.CmdActivate}); resp.Status != api.StatusActivated {
		t.Fatalf("activate: %+v", resp)
	}
	defer m.Handle(api.Request{Command: api.CmdStop})

	got := m.Handle(api.Request{Command: api.CmdGetSettings})
	if got.Status != api.StatusSettings || got.Settings == nil {
		t.Fatalf("get_settings: %+v", got)
	}
	if !got.Settings.AutoUpdate || !got.Settings.UpdateViaTunnel || !got.Settings.UpdateViaDirect {
		t.Fatalf("update flags must default ON: %+v", got.Settings)
	}

	doc := *got.Settings
	doc.AutoUpdate = false
	doc.UpdateViaTunnel = false
	doc.UpdateViaDirect = false
	set := m.Handle(api.Request{Command: api.CmdSetSettings, Settings: &doc})
	if set.Status != api.StatusSettings {
		t.Fatalf("set_settings: %+v", set)
	}
	if set.NeedsReactivation {
		t.Error("toggling the update flags must not require re-activation")
	}

	got = m.Handle(api.Request{Command: api.CmdGetSettings})
	if got.Settings.AutoUpdate || got.Settings.UpdateViaTunnel || got.Settings.UpdateViaDirect {
		t.Fatalf("flags did not round-trip: %+v", got.Settings)
	}
}
