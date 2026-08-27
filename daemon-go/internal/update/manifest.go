// Package update implements the signed update manifest: the schema-1
// document format, its structural validation, and minisign signature
// verification against the compiled-in public keys. It is a pure library —
// no network code; fetching, downloading and applying live in later layers.
package update

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/mod/semver"
)

// errUnsupportedSchema marks a manifest whose schema version this daemon
// does not understand. Verification still fails on it, but Check maps it
// to a soft "not an update for this daemon" result — a future schema bump
// must not read as a failing check on older daemons.
var errUnsupportedSchema = errors.New("update: unsupported manifest schema")

// SchemaVersion is the manifest schema this daemon understands.
const SchemaVersion = 1

// Artifact kinds. KindNone marks a platform whose update channel is
// notify-only (no downloadable artifact).
const (
	KindInstaller = "installer"
	KindPkg       = "pkg"
	KindNone      = "none"
)

// Manifest is one published update announcement. Ordering and downgrade
// protection rest on Seq alone; PublishedAt/ExpiresAt are display and
// soft-freshness data, never trusted for ordering.
type Manifest struct {
	Schema      int        `json:"schema"`
	Seq         uint64     `json:"seq"` // monotonic publish counter (anti-downgrade)
	Channel     string     `json:"channel"`
	Version     string     `json:"version"` // v-prefixed semver
	MinVersion  string     `json:"min_version,omitempty"`
	PublishedAt time.Time  `json:"published_at"`
	ExpiresAt   time.Time  `json:"expires_at"` // soft freshness horizon (~180 days)
	NotesURL    string     `json:"notes_url,omitempty"`
	Artifacts   []Artifact `json:"artifacts"`
}

// Artifact is one per-platform deliverable referenced by a manifest.
type Artifact struct {
	OS     string   `json:"os"`
	Arch   string   `json:"arch"`
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	Size   int64    `json:"size"`
	SHA256 string   `json:"sha256"`
	URLs   []string `json:"urls"`
}

// ParseManifest decodes and structurally validates a manifest document.
// Unknown fields are ignored on purpose — a newer manifest with extra fields
// must stay readable by an older daemon.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("update: parse manifest: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	if m.Schema != SchemaVersion {
		return fmt.Errorf("%w %d", errUnsupportedSchema, m.Schema)
	}
	if !validVersion(m.Version) {
		return fmt.Errorf("update: invalid manifest version %q", m.Version)
	}
	if m.MinVersion != "" && !validVersion(m.MinVersion) {
		return fmt.Errorf("update: invalid manifest min_version %q", m.MinVersion)
	}
	for i := range m.Artifacts {
		if err := m.Artifacts[i].validate(); err != nil {
			return fmt.Errorf("update: artifact %d: %w", i, err)
		}
	}
	return nil
}

func (a *Artifact) validate() error {
	switch a.Kind {
	case KindInstaller, KindPkg:
	case KindNone:
		return nil // notify-only entry: no file facts to check
	default:
		return fmt.Errorf("unknown kind %q", a.Kind)
	}
	if a.Size <= 0 {
		return fmt.Errorf("size %d is not positive", a.Size)
	}
	if len(a.SHA256) != 64 {
		return fmt.Errorf("sha256 is not 64 hex characters")
	}
	if _, err := hex.DecodeString(a.SHA256); err != nil {
		return fmt.Errorf("sha256 is not hex: %v", err)
	}
	if len(a.URLs) == 0 {
		return fmt.Errorf("urls is empty")
	}
	return nil
}

// validVersion reports whether v is a full v-prefixed semver — vX.Y.Z with
// an optional prerelease, no build metadata (canonical per x/mod/semver).
func validVersion(v string) bool {
	return semver.IsValid(v) && semver.Canonical(v) == v
}
