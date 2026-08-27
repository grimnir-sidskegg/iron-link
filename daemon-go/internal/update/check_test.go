package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"aead.dev/minisign"
)

// testNow is the fixed check clock: well inside the fixture manifest's
// published_at..expires_at window.
var testNow = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

type memSeqStore struct {
	seq     uint64
	sets    []uint64
	revoked []uint64
	loadErr error
	saveErr error
}

func (s *memSeqStore) LastSeenSeq() (uint64, error) { return s.seq, s.loadErr }

func (s *memSeqStore) SetLastSeenSeq(seq uint64) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.seq = seq
	s.sets = append(s.sets, seq)
	return nil
}

func (s *memSeqStore) RevokedKeyIDs() ([]uint64, error) { return s.revoked, s.loadErr }

func (s *memSeqStore) RevokeKeyID(id uint64) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.revoked = append(s.revoked, id)
	return nil
}

// runtimeInstallerArtifact is a downloadable entry matching the platform
// the test runs on.
func runtimeInstallerArtifact() map[string]any {
	return map[string]any{
		"os":     runtime.GOOS,
		"arch":   runtime.GOARCH,
		"kind":   "installer",
		"name":   "iron-link-1.2.3-setup.exe",
		"size":   12345678,
		"sha256": strings.Repeat("ab", 32),
		"urls":   []any{"https://example.com/iron-link-1.2.3-setup.exe"},
	}
}

// checkManifestDoc is validManifestDoc with the artifact list keyed to the
// running platform, so the selection policy is exercised for real.
func checkManifestDoc() map[string]any {
	doc := validManifestDoc()
	doc["artifacts"] = []any{runtimeInstallerArtifact()}
	return doc
}

// signDoc signs the doc the way the publish path does, with the trusted
// comment mirroring the doc's own version/seq.
func signDoc(t *testing.T, key minisign.PrivateKey, doc map[string]any) (data, sig []byte) {
	t.Helper()
	data = marshalDoc(t, doc)
	comment := fmt.Sprintf("version=%v seq=%v", doc["version"], doc["seq"])
	return data, signPrehashed(t, key, data, comment)
}

// serveManifest serves the pair at <base>/update.json(.minisig).
func serveManifest(t *testing.T, manifest, sig []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/update.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(manifest)
	})
	mux.HandleFunc("/update.json.minisig", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(sig)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func checkConfig(srv *httptest.Server, store *memSeqStore, keys []trustedKey, current string) Config {
	return Config{
		CurrentVersion: current,
		ManifestURLs:   []string{srv.URL + "/update.json"},
		Transport:      http.DefaultTransport,
		SeqStore:       store,
		Now:            func() time.Time { return testNow },
		keys:           keys,
	}
}

func TestCheckPolicy(t *testing.T) {
	cases := []struct {
		name          string
		current       string
		lastSeen      uint64
		mutate        func(doc map[string]any)
		wantRollback  bool
		wantAvailable bool
		wantStale     bool
		wantArtifact  bool
		wantSets      []uint64
	}{
		{
			name:          "newer version with platform installer",
			current:       "v1.0.0",
			wantAvailable: true,
			wantArtifact:  true,
			wantSets:      []uint64{7},
		},
		{
			name:     "equal version not available",
			current:  "v1.2.3",
			wantSets: []uint64{7}, // seq still advances: the manifest was seen
		},
		{
			name:     "older manifest than current",
			current:  "v2.0.0",
			wantSets: []uint64{7},
		},
		{
			name:     "dev build checked but never prompted",
			current:  "dev",
			wantSets: []uint64{7},
		},
		{
			// CI stamps untagged (tester) builds v0.0.0-<sha>; comparing
			// that would offer every such build a downgrade to the release.
			name:     "untagged CI build checked but never prompted",
			current:  "v0.0.0-abc1234",
			wantSets: []uint64{7},
		},
		{
			name:          "git-describe tail stripped before compare",
			current:       "v1.0.0.r12.gabc1234",
			wantAvailable: true,
			wantArtifact:  true,
			wantSets:      []uint64{7},
		},
		{
			name:     "git-describe tail of the same version",
			current:  "v1.2.3.r4.gdeadbee",
			wantSets: []uint64{7},
		},
		{
			name:    "non-stable channel is not an update",
			current: "v1.0.0",
			mutate:  func(doc map[string]any) { doc["channel"] = "beta" },
			// early exit: seq not persisted for a foreign channel
		},
		{
			name:         "downgraded seq hard-rejected",
			current:      "v1.0.0",
			lastSeen:     10,
			wantRollback: true,
		},
		{
			name:          "equal seq accepted without a rewrite",
			current:       "v1.0.0",
			lastSeen:      7,
			wantAvailable: true,
			wantArtifact:  true,
		},
		{
			name:      "expired manifest is stale, not available",
			current:   "v1.0.0",
			mutate:    func(doc map[string]any) { doc["expires_at"] = "2026-01-01T00:00:00Z" },
			wantStale: true,
			wantSets:  []uint64{7},
		},
		{
			name:    "missing platform entry means notify-only",
			current: "v1.0.0",
			mutate: func(doc map[string]any) {
				doc["artifacts"] = []any{map[string]any{"os": "plan9", "arch": "mips", "kind": "none"}}
			},
			wantAvailable: true,
			wantSets:      []uint64{7},
		},
		{
			name:    "explicit kind none means notify-only",
			current: "v1.0.0",
			mutate: func(doc map[string]any) {
				doc["artifacts"] = []any{map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "kind": "none"}}
			},
			wantAvailable: true,
			wantSets:      []uint64{7},
		},
		{
			// Download refuses everything but kind=installer, so Check must
			// not attach a pkg artifact either: non-nil Artifact promises a
			// working download.
			name:    "pkg kind collapses to notify-only",
			current: "v1.0.0",
			mutate: func(doc map[string]any) {
				a := runtimeInstallerArtifact()
				a["kind"] = "pkg"
				a["name"] = "iron-link-1.2.3.pkg"
				doc["artifacts"] = []any{a}
			},
			wantAvailable: true,
			wantSets:      []uint64{7},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current, _, keys := genTestKeys(t)
			doc := checkManifestDoc()
			if tc.mutate != nil {
				tc.mutate(doc)
			}
			manifest, sig := signDoc(t, current, doc)
			srv := serveManifest(t, manifest, sig)
			store := &memSeqStore{seq: tc.lastSeen}

			res, err := Check(context.Background(), checkConfig(srv, store, keys, tc.current))
			if tc.wantRollback {
				if !errors.Is(err, ErrRollback) {
					t.Fatalf("want ErrRollback, got %v", err)
				}
				if len(store.sets) != 0 {
					t.Fatalf("rollback must not persist seq, got sets %v", store.sets)
				}
				return
			}
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if res.Available != tc.wantAvailable {
				t.Fatalf("Available = %v, want %v (%+v)", res.Available, tc.wantAvailable, res)
			}
			if res.Stale != tc.wantStale {
				t.Fatalf("Stale = %v, want %v", res.Stale, tc.wantStale)
			}
			if gotArtifact := res.Artifact != nil; gotArtifact != tc.wantArtifact {
				t.Fatalf("Artifact = %v, want present=%v", res.Artifact, tc.wantArtifact)
			}
			if !res.CheckedAt.Equal(testNow) {
				t.Fatalf("CheckedAt = %v, want the injected clock %v", res.CheckedAt, testNow)
			}
			if res.VerifiedKey != KeyCurrent {
				t.Fatalf("VerifiedKey = %q, want %q", res.VerifiedKey, KeyCurrent)
			}
			if fmt.Sprint(store.sets) != fmt.Sprint(tc.wantSets) {
				t.Fatalf("seq sets = %v, want %v", store.sets, tc.wantSets)
			}
			if len(store.revoked) != 0 {
				t.Fatalf("a current-signed manifest must not revoke anything, got %v", store.revoked)
			}
		})
	}
}

// TestCheckRecoveryKeyRevokesCurrent pins the revocation half of the
// two-key design: once a recovery-signed manifest is accepted, the current
// key's id is persisted as revoked and every later current-signed manifest
// is refused (with the floor untouched), while the recovery key keeps
// working. A recovery-signed manifest below the floor is a rollback like
// any other and revokes nothing.
func TestCheckRecoveryKeyRevokesCurrent(t *testing.T) {
	current, recovery, keys := genTestKeys(t)
	currentID, recoveryID := keys[0].pub.ID(), keys[1].pub.ID()
	signed := func(key minisign.PrivateKey, seq int) *httptest.Server {
		doc := checkManifestDoc()
		doc["seq"] = seq
		manifest, sig := signDoc(t, key, doc)
		return serveManifest(t, manifest, sig)
	}
	store := &memSeqStore{seq: 7}

	// Replayed recovery-signed manifest below the floor: rollback, no revocation.
	_, err := Check(context.Background(), checkConfig(signed(recovery, 6), store, keys, "v1.0.0"))
	if !errors.Is(err, ErrRollback) {
		t.Fatalf("want ErrRollback for a recovery-signed manifest below the floor, got %v", err)
	}
	if len(store.revoked) != 0 {
		t.Fatalf("a rollback must not revoke, got %v", store.revoked)
	}

	res, err := Check(context.Background(), checkConfig(signed(recovery, 8), store, keys, "v1.0.0"))
	if err != nil {
		t.Fatalf("recovery-signed check: %v", err)
	}
	if res.VerifiedKey != KeyRecovery || !res.Available {
		t.Fatalf("recovery-signed result: %+v", res)
	}
	if fmt.Sprint(store.revoked) != fmt.Sprint([]uint64{currentID}) {
		t.Fatalf("revoked = %X, want the current key %X", store.revoked, currentID)
	}
	if store.seq != 8 {
		t.Fatalf("floor = %d, want 8", store.seq)
	}

	// The old current key cannot outrun the owner with a higher seq.
	res, err = Check(context.Background(), checkConfig(signed(current, 9), store, keys, "v1.0.0"))
	if !errors.Is(err, ErrRevokedKey) {
		t.Fatalf("want ErrRevokedKey for the revoked current key, got %v (%+v)", err, res)
	}
	if store.seq != 8 || len(store.sets) != 1 {
		t.Fatalf("a revoked-key manifest must not move the floor: seq=%d sets=%v", store.seq, store.sets)
	}

	// Recovery keeps working, and re-accepting it revokes nothing new.
	res, err = Check(context.Background(), checkConfig(signed(recovery, 9), store, keys, "v1.0.0"))
	if err != nil || res.VerifiedKey != KeyRecovery {
		t.Fatalf("second recovery-signed check: %+v, %v", res, err)
	}
	if fmt.Sprint(store.revoked) != fmt.Sprint([]uint64{currentID}) || slices.Contains(store.revoked, recoveryID) {
		t.Fatalf("revoked = %X after the second recovery manifest, want only %X", store.revoked, currentID)
	}
}

func TestCheckMirrorFallback(t *testing.T) {
	current, _, keys := genTestKeys(t)
	manifest, sig := signDoc(t, current, checkManifestDoc())
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	good := serveManifest(t, manifest, sig)

	store := &memSeqStore{}
	cfg := checkConfig(good, store, keys, "v1.0.0")
	cfg.ManifestURLs = []string{bad.URL + "/update.json", good.URL + "/update.json"}
	res, err := Check(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Check with a failing first mirror: %v", err)
	}
	if !res.Available {
		t.Fatalf("second mirror must win, got %+v", res)
	}
}

// TestCheckMirrorSignatureFallback pins that a mirror serving update.json
// but not its .minisig is skipped whole: the next mirror serves BOTH
// halves, and the first mirror's (different) manifest is never paired with
// the second mirror's signature — mixing would fail verification here.
func TestCheckMirrorSignatureFallback(t *testing.T) {
	current, _, keys := genTestKeys(t)
	lureDoc := checkManifestDoc()
	lureDoc["version"] = "v9.9.9"
	lure, _ := signDoc(t, current, lureDoc)
	manifest, sig := signDoc(t, current, checkManifestDoc())

	half := http.NewServeMux() // no .minisig route: that GET 404s
	half.HandleFunc("/update.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(lure)
	})
	halfSrv := httptest.NewServer(half)
	t.Cleanup(halfSrv.Close)
	good := serveManifest(t, manifest, sig)

	store := &memSeqStore{}
	cfg := checkConfig(good, store, keys, "v1.0.0")
	cfg.ManifestURLs = []string{halfSrv.URL + "/update.json", good.URL + "/update.json"}
	res, err := Check(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Check with a signature-less first mirror: %v", err)
	}
	if !res.Available || res.LatestVersion != "v1.2.3" {
		t.Fatalf("second mirror must win with its own pair, got %+v", res)
	}
}

func TestCheckAllMirrorsFail(t *testing.T) {
	_, _, keys := genTestKeys(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	store := &memSeqStore{}
	cfg := checkConfig(bad, store, keys, "v1.0.0")
	if _, err := Check(context.Background(), cfg); err == nil {
		t.Fatal("want error when every mirror fails")
	}
	if len(store.sets) != 0 {
		t.Fatalf("fetch failure must not persist seq, got %v", store.sets)
	}
}

func TestCheckBadSignature(t *testing.T) {
	current, _, keys := genTestKeys(t)
	manifest, sig := signDoc(t, current, checkManifestDoc())
	tampered := bytes.Clone(manifest)
	tampered[len(tampered)/2] ^= 0x01
	srv := serveManifest(t, tampered, sig)

	store := &memSeqStore{}
	_, err := Check(context.Background(), checkConfig(srv, store, keys, "v1.0.0"))
	if err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("want a signature failure, got %v", err)
	}
	if len(store.sets) != 0 {
		t.Fatalf("verify failure must not persist seq, got %v", store.sets)
	}
}

func TestCheckManifestOverSizeCap(t *testing.T) {
	_, _, keys := genTestKeys(t)
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxManifestSize+1))
	}))
	t.Cleanup(huge.Close)
	store := &memSeqStore{}
	cfg := checkConfig(huge, store, keys, "v1.0.0")
	if _, err := Check(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("want the size-cap error, got %v", err)
	}
}

func TestCheckSignatureOverSizeCap(t *testing.T) {
	current, _, keys := genTestKeys(t)
	manifest, _ := signDoc(t, current, checkManifestDoc())
	mux := http.NewServeMux()
	mux.HandleFunc("/update.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(manifest)
	})
	mux.HandleFunc("/update.json.minisig", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxSignatureSize+1))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := checkConfig(srv, &memSeqStore{}, keys, "v1.0.0")
	if _, err := Check(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("want the signature size-cap error, got %v", err)
	}
}

// TestCheckUnsupportedSchemaSoft pins the schema gate as a soft outcome: a
// signature-verified manifest of a future schema is "nothing for this
// daemon", never a failed check — an old daemon must keep reporting quiet
// success after a schema bump.
func TestCheckUnsupportedSchemaSoft(t *testing.T) {
	current, _, keys := genTestKeys(t)
	doc := checkManifestDoc()
	doc["schema"] = 2
	manifest, sig := signDoc(t, current, doc)
	srv := serveManifest(t, manifest, sig)

	store := &memSeqStore{}
	res, err := Check(context.Background(), checkConfig(srv, store, keys, "v1.0.0"))
	if err != nil {
		t.Fatalf("a future schema must not fail the check, got %v", err)
	}
	if res.Available || res.Stale || res.Artifact != nil || res.LatestVersion != "" {
		t.Fatalf("future schema result must carry nothing, got %+v", res)
	}
	if !res.CheckedAt.Equal(testNow) {
		t.Fatalf("CheckedAt = %v, want %v", res.CheckedAt, testNow)
	}
	if len(store.sets) != 0 {
		t.Fatalf("future schema must not persist seq, got %v", store.sets)
	}
}

// TestCheckRequestPrivacy pins the request shape: the one static generic
// UA, and no query parameters or other identifying headers.
func TestCheckRequestPrivacy(t *testing.T) {
	current, _, keys := genTestKeys(t)
	manifest, sig := signDoc(t, current, checkManifestDoc())
	var userAgents []string
	mux := http.NewServeMux()
	mux.HandleFunc("/update.json", func(w http.ResponseWriter, r *http.Request) {
		userAgents = append(userAgents, r.UserAgent())
		if r.URL.RawQuery != "" {
			t.Errorf("request carried query %q", r.URL.RawQuery)
		}
		_, _ = w.Write(manifest)
	})
	mux.HandleFunc("/update.json.minisig", func(w http.ResponseWriter, r *http.Request) {
		userAgents = append(userAgents, r.UserAgent())
		if r.URL.RawQuery != "" {
			t.Errorf("signature request carried query %q", r.URL.RawQuery)
		}
		_, _ = w.Write(sig)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	if _, err := Check(context.Background(), checkConfig(srv, &memSeqStore{}, keys, "v1.0.0")); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(userAgents) != 2 {
		t.Fatalf("want 2 requests, saw %d", len(userAgents))
	}
	for _, ua := range userAgents {
		if ua != checkUserAgent {
			t.Fatalf("User-Agent %q, want the static constant", ua)
		}
	}
}

func TestCheckConfigValidation(t *testing.T) {
	base := Config{
		CurrentVersion: "v1.0.0",
		ManifestURLs:   []string{"http://198.51.100.1/update.json"},
		Transport:      http.DefaultTransport,
		SeqStore:       &memSeqStore{},
	}
	cases := []struct {
		name   string
		mutate func(cfg *Config)
	}{
		{"no urls", func(cfg *Config) { cfg.ManifestURLs = nil }},
		{"nil transport", func(cfg *Config) { cfg.Transport = nil }},
		{"nil seq store", func(cfg *Config) { cfg.SeqStore = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			if _, err := Check(context.Background(), cfg); err == nil {
				t.Fatal("want a config error, got nil")
			}
		})
	}
}

// TestCheckFixtureEndToEnd runs Check against the static signed fixture
// with the COMPILED-IN dev keys (no injected key set) — the whole chain as
// shipped. Artifact expectations follow the fixture's platform table.
func TestCheckFixtureEndToEnd(t *testing.T) {
	manifest := fixtureManifest(t)
	sig, err := os.ReadFile("testdata/update.json.minisig")
	if err != nil {
		t.Fatalf("read fixture signature: %v", err)
	}
	var parsed minisign.Signature
	if err := parsed.UnmarshalText(sig); err != nil {
		t.Fatalf("parse fixture signature: %v", err)
	}
	keys, err := trustedKeys()
	if err != nil {
		t.Fatalf("trustedKeys: %v", err)
	}
	if keys[0].pub.ID() != parsed.KeyID {
		t.Skipf("fixture signed by key %X, compiled-in current key is %X — re-sign testdata/update.json with the current secret key (command in keys.go) to re-pin this test",
			parsed.KeyID, keys[0].pub.ID())
	}
	srv := serveManifest(t, manifest, sig)
	store := &memSeqStore{}
	cfg := checkConfig(srv, store, nil, "v1.0.0")
	res, err := Check(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Available || res.Stale {
		t.Fatalf("fixture must announce v1.2.3: %+v", res)
	}
	if res.LatestVersion != "v1.2.3" || res.VerifiedKey != KeyCurrent {
		t.Fatalf("unexpected result: %+v", res)
	}
	wantInstaller := runtime.GOOS == "windows" && runtime.GOARCH == "amd64"
	if gotInstaller := res.Artifact != nil; gotInstaller != wantInstaller {
		t.Fatalf("Artifact = %+v, want installer=%v on %s/%s",
			res.Artifact, wantInstaller, runtime.GOOS, runtime.GOARCH)
	}
	if fmt.Sprint(store.sets) != fmt.Sprint([]uint64{7}) {
		t.Fatalf("seq sets = %v, want [7]", store.sets)
	}
}

func TestNormalizeCurrentVersion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"v1.2.3", "v1.2.3"},
		{"v0.1.0.r12.gabc1234", "v0.1.0"},
		{"v0.0.0-abc1234", ""}, // CI untagged build: the no-version sentinel, never compared
		{"v0.0.0", ""},
		{"dev", ""},
		{"1.2.3", ""},                      // v-prefix required
		{"v0.1.0.alpha.8.r5.gabc1234", ""}, // prerelease-tag describe: not semver after the strip
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeCurrentVersion(tc.in); got != tc.want {
			t.Errorf("normalizeCurrentVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSelectArtifact(t *testing.T) {
	m := &Manifest{Artifacts: []Artifact{
		{OS: "windows", Arch: "amd64", Kind: KindInstaller},
		{OS: "linux", Arch: "amd64", Kind: KindNone},
	}}
	cases := []struct {
		goos, goarch string
		wantKind     string
		wantNil      bool
	}{
		{"windows", "amd64", KindInstaller, false},
		{"linux", "amd64", KindNone, false},
		{"linux", "arm64", "", true},
		{"darwin", "arm64", "", true},
	}
	for _, tc := range cases {
		got := selectArtifact(m, tc.goos, tc.goarch)
		if (got == nil) != tc.wantNil {
			t.Fatalf("selectArtifact(%s/%s) = %+v, want nil=%v", tc.goos, tc.goarch, got, tc.wantNil)
		}
		if got != nil && got.Kind != tc.wantKind {
			t.Fatalf("selectArtifact(%s/%s).Kind = %q, want %q", tc.goos, tc.goarch, got.Kind, tc.wantKind)
		}
	}
}
