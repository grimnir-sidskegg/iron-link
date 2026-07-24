// Package store is the iron-link profile store (`profiles/<name>.json` +
// `state.json` under the config root). The on-disk encoding is Go-native
// (schema v2, plain encoding/json over the model structs, atomic 0600
// files); pre-v2 (Rust-serde) files are rejected with a clear error — the
// legacy decode path and its startup migration were removed 2026-06-12.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"ironlink/daemon/internal/api"
)

// DefaultDir returns the iron-link config root: $IRON_LINK_CONFIG_DIR if set
// (the test/dev override the Rust paths honor), else the platform config dir
// (~/.config, %APPDATA%, ~/Library/Application Support) + "iron-link".
//
// SPECIAL CASE — elevated for TUN via sudo: TUN needs root, but the user's
// profiles live under THEIR home, not root's. Under sudo, os.UserConfigDir
// would return /root/.config (empty) unless the sudo config preserves HOME — so
// when running as root we resolve to the INVOKING user's config dir (SUDO_UID).
// Mirrors the control-socket fix (ipc.DefaultAddr); without it `sudo
// iron-link-daemon` shows no profiles unless invoked with `sudo -E`.
func DefaultDir() (string, error) {
	if dir := os.Getenv("IRON_LINK_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if os.Geteuid() == 0 {
		if dir, ok := sudoUserConfigDir(os.Getenv); ok {
			return dir, nil
		}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return filepath.Join(base, "iron-link"), nil
}

// sudoUserConfigDir resolves the iron-link config root of the user that invoked
// sudo (from SUDO_UID → their home dir), so a root daemon reads that user's
// existing profiles. Returns ok=false when not under sudo or the user can't be
// resolved — DefaultDir then falls back to os.UserConfigDir. Matches the Rust
// client's config_root for the common case (no XDG_CONFIG_HOME override; that
// edge is covered by IRON_LINK_CONFIG_DIR).
func sudoUserConfigDir(getenv func(string) string) (string, bool) {
	uid := getenv("SUDO_UID")
	if uid == "" {
		return "", false
	}
	u, err := user.LookupId(uid)
	if err != nil || u.HomeDir == "" {
		return "", false
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(u.HomeDir, "Library", "Application Support", "iron-link"), true
	}
	return filepath.Join(u.HomeDir, ".config", "iron-link"), true
}

// Store reads and writes one config root. Zero-value is unusable; construct
// with Open / OpenAt. The store itself is not goroutine-safe — the daemon
// serializes access (one writer by design).
type Store struct {
	baseDir string
}

// Open opens the store at DefaultDir.
func Open() (*Store, error) {
	dir, err := DefaultDir()
	if err != nil {
		return nil, err
	}
	return OpenAt(dir), nil
}

// OpenAt opens the store rooted at baseDir (tests point this at a fixture
// dir).
func OpenAt(baseDir string) *Store {
	return &Store{baseDir: baseDir}
}

// Dir returns the config root this store is bound to (the daemon parks
// engine-adjacent files — e.g. the sing-box cache — next to the profiles).
func (s *Store) Dir() string { return s.baseDir }

// ValidateName enforces ^[A-Za-z0-9._-]{1,64}$ — names double as on-disk
// filenames, so the set rejects path separators, "..", and whitespace
// (mirrors the Rust validate_name).
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("name %q exceeds 64 characters", name)
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
		default:
			return fmt.Errorf("name %q contains characters outside [A-Za-z0-9._-]", name)
		}
	}
	return nil
}

func (s *Store) profilePath(name string) string {
	return filepath.Join(s.baseDir, "profiles", name+".json")
}

// atomicWrite persists bytes at path via a same-directory temp file + rename,
// tightened to 0600 BEFORE the content lands (profiles hold node credentials).
// Mode-setting is a no-op on Windows, like the Rust set_file_mode_0600 — the
// named-pipe/dir ACLs are the protection there.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if runtime.GOOS != "windows" {
		if err := tmp.Chmod(0o600); err != nil {
			tmp.Close()
			return err
		}
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return renameWithRetry(tmpName, path)
}

// renameWithRetry is the final atomic step of atomicWrite. On Windows a
// freshly written destination can be transiently locked — Defender / the
// indexer hold a handle for a beat after the file is closed — so MoveFileEx
// fails with ERROR_ACCESS_DENIED / ERROR_SHARING_VIOLATION. Rapid back-to-back
// writes to one profile (the live smoke's upsert → select → remove routing
// chain) hit this intermittently. Retry briefly, the same way the smoke
// suite's teardown retries its scratch-dir delete. On Unix this is one attempt.
func renameWithRetry(from, to string) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if runtime.GOOS != "windows" || attempt >= 10 {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// LoadProfile reads and decodes profiles/<name>.json (schema v2). The
// version probe is an explicit GUARD, not a dispatch: Go's case-insensitive
// JSON field matching would otherwise HALF-decode a pre-v2 (Rust-serde)
// file silently — better a clear error. (The v1 decode path and its
// startup migration were removed 2026-06-12 after the only deployment
// migrated.)
func (s *Store) LoadProfile(name string) (*Profile, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.profilePath(name))
	if err != nil {
		return nil, fmt.Errorf("load profile %q: %w", name, err)
	}
	var probe struct {
		SchemaVersion uint32 `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("decode profile %q: %w", name, err)
	}
	if probe.SchemaVersion < 2 {
		return nil, fmt.Errorf(
			"profile %q is in the pre-v2 on-disk format (schema_version %d): start a daemon build from the v1→v2 migration window (≤ 2026-06-12) once to migrate it",
			name, probe.SchemaVersion)
	}
	p := new(Profile)
	if err := json.Unmarshal(raw, p); err != nil {
		return nil, fmt.Errorf("decode profile %q: %w", name, err)
	}
	p.normalize()
	return p, nil
}

// SaveProfile persists p as profiles/<name>.json (atomic, 0600) in the
// current (v2, Go-native) shape — normalize stamps the schema version.
// Pretty-printed so profiles stay human-diffable; raw blocks
// (routing_configs conditions, xhttp extra) are whitespace-normalized in
// the process, which reads back identically.
func (s *Store) SaveProfile(name string, p *Profile) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	p.normalize()
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encode profile %q: %w", name, err)
	}
	return atomicWrite(s.profilePath(name), data)
}

// CreateProfile creates a NEW named profile (Rust Profile::new shape),
// rejecting an existing one.
func (s *Store) CreateProfile(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, err := os.Stat(s.profilePath(name)); err == nil {
		return fmt.Errorf("profile %q already exists", name)
	}
	return s.SaveProfile(name, NewProfile(name))
}

// DeleteProfile removes profiles/<name>.json and clears the active selection
// if it pointed at the deleted profile.
func (s *Store) DeleteProfile(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := os.Remove(s.profilePath(name)); err != nil {
		return fmt.Errorf("delete profile %q: %w", name, err)
	}
	if active, err := s.ActiveProfileName(); err == nil && active == name {
		return s.writeState(nil)
	}
	return nil
}

// ListProfiles returns the stored profile names, sorted.
func (s *Store) ListProfiles() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.baseDir, "profiles"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	slices.Sort(names)
	return names, nil
}

// SetActiveProfile selects an EXISTING profile as active (state.json).
func (s *Store) SetActiveProfile(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, err := os.Stat(s.profilePath(name)); err != nil {
		return fmt.Errorf("profile %q does not exist", name)
	}
	return s.writeState(&name)
}

// ActiveProfileName reads state.json's active selection; "" means none (or no
// state file yet).
func (s *Store) ActiveProfileName() (string, error) {
	raw, err := os.ReadFile(filepath.Join(s.baseDir, "state.json"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read state.json: %w", err)
	}
	var state struct {
		Active *string `json:"active"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return "", fmt.Errorf("decode state.json: %w", err)
	}
	if state.Active == nil {
		return "", nil
	}
	return *state.Active, nil
}

func (s *Store) writeState(active *string) error {
	data, err := json.Marshal(map[string]any{"active": active})
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.baseDir, "state.json"), data)
}

// -- last session (restore-on-start) ------------------------------------------

// lastSession mirrors the Rust LastSession: the persisted Activate INTENT —
// names + the tun flag, never paths/argv (a poisoned record can at worst name
// a different profile/node; the daemon re-plans on restore).
type lastSession struct {
	Context api.PersistedEntry `json:"context"`
}

func (s *Store) lastSessionPath() string {
	return filepath.Join(s.baseDir, "last_session.json")
}

// SaveLastSession persists the activation context for restore-on-start.
func (s *Store) SaveLastSession(context api.PersistedEntry) error {
	data, err := json.MarshalIndent(lastSession{Context: context}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.lastSessionPath(), data)
}

// LoadLastSession returns the persisted activation context, or nil when none
// is recorded.
func (s *Store) LoadLastSession() (*api.PersistedEntry, error) {
	raw, err := os.ReadFile(s.lastSessionPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ls lastSession
	if err := json.Unmarshal(raw, &ls); err != nil {
		return nil, fmt.Errorf("decode last_session.json: %w", err)
	}
	return &ls.Context, nil
}

// ClearLastSession removes the record (a clean Stop must not restore).
func (s *Store) ClearLastSession() error {
	err := os.Remove(s.lastSessionPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
