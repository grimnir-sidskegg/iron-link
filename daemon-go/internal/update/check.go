package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// ChannelStable is the only release channel this daemon follows.
const ChannelStable = "stable"

// checkUserAgent is the only identifying header an update request carries.
// Static and generic on purpose (privacy: no daemon version, no unique IDs,
// no query parameters) — the check should look like ordinary browser
// traffic. Refresh the Chrome major to the current stable at release time
// so the string stays in the crowd instead of aging into a fingerprint.
const checkUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"

const (
	// maxManifestSize caps the update.json response body.
	maxManifestSize = 1 << 20
	// maxSignatureSize caps the .minisig response body (a minisign
	// signature is a few hundred bytes).
	maxSignatureSize = 64 << 10
	// fetchAttemptTimeout bounds each single GET.
	fetchAttemptTimeout = 15 * time.Second
	// checkTotalTimeout bounds one whole Check across all mirrors.
	checkTotalTimeout = 90 * time.Second
)

// ErrRollback marks a manifest whose seq is below the persisted
// last-seen floor: a replayed old manifest (rollback/freeze attack) —
// never a routine condition, so it is distinct from fetch errors.
var ErrRollback = errors.New("update: manifest seq below last seen")

// ErrRevokedKey marks a manifest signed by a compiled-in key this daemon
// has retired: a recovery-signed manifest was accepted earlier, and the
// recovery key is only ever used to rotate away from a lost or compromised
// current key.
var ErrRevokedKey = errors.New("update: manifest signed by a revoked key")

// SeqStore persists the trust state that must outlive the process: the
// highest manifest seq this daemon has accepted (the anti-downgrade floor;
// LastSeenSeq returns 0 when nothing has been stored yet) and the ids of
// the compiled-in keys it no longer accepts.
type SeqStore interface {
	LastSeenSeq() (uint64, error)
	SetLastSeenSeq(seq uint64) error
	RevokedKeyIDs() ([]uint64, error)
	RevokeKeyID(id uint64) error
}

// Config carries everything Check needs. The library knows nothing about
// the engine or settings: the caller decides how the network is reached
// (through the tunnel, TUN-exempt direct, …) and injects it as Transport.
type Config struct {
	// CurrentVersion is the stamped build version ("v1.2.3", a packaging
	// tail like "v1.2.3.r5.gabc1234", or "dev" on an unstamped build).
	CurrentVersion string
	// ManifestURLs are update.json locations tried in order; the detached
	// signature must sit beside each one at "<url>.minisig". The first
	// mirror that serves both wins.
	ManifestURLs []string
	// Transport reaches the URLs. Required — a nil transport would
	// silently fall back to a direct dial the caller did not choose.
	Transport http.RoundTripper
	// SeqStore holds the persisted anti-downgrade floor. Required.
	SeqStore SeqStore
	// Now is the clock (tests inject a fixed one); nil means time.Now.
	Now func() time.Time

	// keys overrides the compiled-in verification keys in package tests.
	keys []trustedKey
}

// Result is one completed update check. The caller caches it; a nil
// Artifact with Available=true means notify-only (no downloadable file
// for this platform — the explicit kind=none case and a missing platform
// entry collapse to the same thing).
type Result struct {
	CheckedAt     time.Time
	Available     bool // a newer stable version exists and the manifest is fresh
	Stale         bool // manifest past expires_at — informational, never banner-worthy
	LatestVersion string
	NotesURL      string
	Artifact      *Artifact // nil = notify-only
	VerifiedKey   string    // verifying compiled-in key (KeyCurrent/KeyRecovery); empty only on the unsupported-schema soft result
}

// Check fetches, verifies and evaluates the update manifest. Fail-soft
// contract: any network/verify/store error returns (nil, error) and the
// caller logs quietly and keeps its previous cache — a blocked endpoint is
// a normal condition for this daemon. A non-nil Result always means the
// manifest verified against a compiled-in key.
func Check(ctx context.Context, cfg Config) (*Result, error) {
	if len(cfg.ManifestURLs) == 0 {
		return nil, errors.New("update: no manifest URLs configured")
	}
	if cfg.Transport == nil {
		return nil, errors.New("update: no transport configured")
	}
	if cfg.SeqStore == nil {
		return nil, errors.New("update: no seq store configured")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithTimeout(ctx, checkTotalTimeout)
	defer cancel()

	manifestJSON, sig, err := fetchManifest(ctx, cfg)
	if err != nil {
		return nil, err
	}
	keys := cfg.keys
	if keys == nil {
		if keys, err = trustedKeys(); err != nil {
			return nil, err
		}
	}
	v, err := verifyManifest(keys, manifestJSON, sig)
	if err != nil {
		if errors.Is(err, errUnsupportedSchema) {
			// The parse (and so the schema gate) runs only after the
			// signature verified, so this is an authentic manifest of a
			// newer schema: not an update for this daemon, not a failed
			// check — same soft outcome as a foreign channel. The verifier
			// does not expose the key on this path, so VerifiedKey is empty.
			return &Result{CheckedAt: now()}, nil
		}
		return nil, err
	}
	revoked, err := cfg.SeqStore.RevokedKeyIDs()
	if err != nil {
		return nil, fmt.Errorf("update: load revoked keys: %w", err)
	}
	if slices.Contains(revoked, v.KeyID) {
		return nil, fmt.Errorf("%w: %s key %X", ErrRevokedKey, v.KeyName, v.KeyID)
	}
	m := v.Manifest
	res := &Result{CheckedAt: now(), VerifiedKey: v.KeyName}

	// Policy gates, in order. The schema gate sits above: the verifier's
	// parse rejects any schema but 1, mapped to a soft result.
	if m.Channel != ChannelStable {
		return res, nil // not an update for this daemon; caller logs at info
	}
	last, err := cfg.SeqStore.LastSeenSeq()
	if err != nil {
		return nil, fmt.Errorf("update: load last seen seq: %w", err)
	}
	if m.Seq < last {
		return nil, fmt.Errorf("%w: manifest seq %d < last seen %d", ErrRollback, m.Seq, last)
	}
	// A recovery signature means the current key is gone (lost or
	// compromised): retire every other compiled-in key on this daemon, so a
	// holder of the old current key cannot outrun the owner with later
	// current-signed manifests. Only past the seq gate — a replayed old
	// recovery-signed manifest must not revoke a rotated-in current key.
	if v.KeyName == KeyRecovery {
		for _, k := range keys {
			if id := k.pub.ID(); k.name != KeyRecovery && !slices.Contains(revoked, id) {
				if err := cfg.SeqStore.RevokeKeyID(id); err != nil {
					return nil, fmt.Errorf("update: persist key revocation: %w", err)
				}
			}
		}
	}
	// Advance the floor only after full verification (above); a bad
	// signature or a rollback must never move it.
	if m.Seq > last {
		if err := cfg.SeqStore.SetLastSeenSeq(m.Seq); err != nil {
			return nil, fmt.Errorf("update: persist last seen seq: %w", err)
		}
	}
	res.LatestVersion = m.Version
	res.NotesURL = m.NotesURL
	if !m.ExpiresAt.IsZero() && now().After(m.ExpiresAt) {
		// Soft freshness only: an expired manifest is cached and logged
		// but never announced — the publisher may simply have gone quiet.
		res.Stale = true
		return res, nil
	}
	current := normalizeCurrentVersion(cfg.CurrentVersion)
	if current == "" {
		return res, nil // unstamped/dev build: checked and cached, never prompted
	}
	if semver.Compare(m.Version, current) <= 0 {
		return res, nil
	}
	res.Available = true
	// Attach only what Download accepts: any other kind (an explicit none,
	// a future pkg) collapses to notify-only, keeping the non-nil-Artifact
	// = downloadable contract of Result.
	if a := selectArtifact(&m, runtime.GOOS, runtime.GOARCH); a != nil && a.Kind == KindInstaller {
		artifact := *a
		res.Artifact = &artifact
	}
	return res, nil
}

// gitDescribeTail matches the packaging version suffix appended after a
// clean tag (Arch pkgver style): v0.1.0.r12.gabc1234 → v0.1.0.
var gitDescribeTail = regexp.MustCompile(`\.r\d+\.g[0-9a-f]+.*$`)

// normalizeCurrentVersion maps the stamped build version onto comparable
// semver. Anything that does not normalize to a valid v-prefixed semver
// ("dev", exotic tails) yields "" — the caller treats that as "cannot
// compare, never prompt". So does 0.0.0: CI stamps untagged builds as
// v0.0.0-<sha>, and as valid semver that sorts below every release, which
// would offer each tester build a downgrade to the latest release.
func normalizeCurrentVersion(v string) string {
	v = gitDescribeTail.ReplaceAllString(v, "")
	if !strings.HasPrefix(v, "v") || !semver.IsValid(v) {
		return ""
	}
	if strings.HasPrefix(v, "v0.0.0") {
		return ""
	}
	return v
}

// selectArtifact returns the manifest entry for the given platform, nil
// when the manifest carries none — which policy treats exactly like an
// explicit kind=none entry: notify-only.
func selectArtifact(m *Manifest, goos, goarch string) *Artifact {
	for i := range m.Artifacts {
		if m.Artifacts[i].OS == goos && m.Artifacts[i].Arch == goarch {
			return &m.Artifacts[i]
		}
	}
	return nil
}

// fetchManifest tries each manifest URL in order and returns the first
// mirror that serves both the manifest and its detached signature.
func fetchManifest(ctx context.Context, cfg Config) (manifestJSON, sig []byte, err error) {
	client := &http.Client{Transport: cfg.Transport}
	var errs []error
	for _, url := range cfg.ManifestURLs {
		m, err := fetchOne(ctx, client, url, maxManifestSize)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		s, err := fetchOne(ctx, client, url+".minisig", maxSignatureSize)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		return m, s, nil
	}
	return nil, nil, fmt.Errorf("update: all manifest mirrors failed: %w", errors.Join(errs...))
}

func fetchOne(ctx context.Context, client *http.Client, url string, maxSize int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchAttemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("update: %s: %w", url, err)
	}
	req.Header.Set("User-Agent", checkUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: GET %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("update: read %s: %w", url, err)
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("update: %s exceeds the %d byte cap", url, maxSize)
	}
	return data, nil
}
