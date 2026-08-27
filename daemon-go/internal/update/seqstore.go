package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"
)

// seqStateName is the update trust state's file at the config root.
const seqStateName = "update_state.json"

// FileSeqStore is the SeqStore persisted as a small JSON document in the
// daemon's config root, written with the same-directory temp + rename
// pattern every other persisted state uses. Next to the anti-downgrade
// floor and the revoked key ids it keeps the check loop's attempt clock, so
// a daemon restart (every boot, for an auto-start service) resumes the
// daily cadence instead of fetching again right after boot.
type FileSeqStore struct {
	dir string
}

var _ SeqStore = (*FileSeqStore)(nil)

// NewFileSeqStore returns the store rooted at dir (the config root). The
// directory is created on the first write: a daemon that has persisted
// nothing yet (a fresh install) has no config root, and its first
// successful check must not fail on the floor write.
func NewFileSeqStore(dir string) *FileSeqStore { return &FileSeqStore{dir: dir} }

// seqState is the on-disk document. Key ids are hex, as minisign prints
// them; the attempt clock is RFC 3339.
type seqState struct {
	LastSeenSeq   uint64   `json:"last_seen_seq"`
	LastAttempt   string   `json:"last_attempt,omitempty"`
	RevokedKeyIDs []string `json:"revoked_key_ids,omitempty"`
}

// stateMu serializes the read-modify-write behind every setter: the check
// loop and a forced check_update may persist concurrently through separate
// store values over the same file.
var stateMu sync.Mutex

// LastSeenSeq loads the floor; a missing file means 0 (nothing accepted
// yet). A corrupt file is an error — silently reading it as 0 would drop
// the anti-downgrade protection.
func (s *FileSeqStore) LastSeenSeq() (uint64, error) {
	st, err := s.load()
	return st.LastSeenSeq, err
}

// SetLastSeenSeq atomically persists a new floor (temp + rename, 0600).
func (s *FileSeqStore) SetLastSeenSeq(seq uint64) error {
	return s.update(func(st *seqState) { st.LastSeenSeq = seq })
}

// RevokedKeyIDs loads the ids of the compiled-in keys this daemon no longer
// accepts signatures from.
func (s *FileSeqStore) RevokedKeyIDs() ([]uint64, error) {
	st, err := s.load()
	if err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(st.RevokedKeyIDs))
	for _, text := range st.RevokedKeyIDs {
		id, err := strconv.ParseUint(text, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("update: decode seq state: revoked key id %q: %w", text, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// RevokeKeyID persists id as revoked; already-revoked ids are a no-op.
func (s *FileSeqStore) RevokeKeyID(id uint64) error {
	return s.update(func(st *seqState) {
		text := strconv.FormatUint(id, 16)
		if !slices.Contains(st.RevokedKeyIDs, text) {
			st.RevokedKeyIDs = append(st.RevokedKeyIDs, text)
		}
	})
}

// LastAttempt loads the check loop's attempt clock; zero when none has
// been recorded.
func (s *FileSeqStore) LastAttempt() (time.Time, error) {
	st, err := s.load()
	if err != nil || st.LastAttempt == "" {
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339, st.LastAttempt)
	if err != nil {
		return time.Time{}, fmt.Errorf("update: decode seq state: last_attempt: %w", err)
	}
	return t, nil
}

// SetLastAttempt persists the check loop's attempt clock (second precision).
func (s *FileSeqStore) SetLastAttempt(t time.Time) error {
	return s.update(func(st *seqState) { st.LastAttempt = t.UTC().Format(time.RFC3339) })
}

func (s *FileSeqStore) load() (seqState, error) {
	var st seqState
	data, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("update: read seq state: %w", err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("update: decode seq state: %w", err)
	}
	return st, nil
}

// update applies mutate to the whole document and writes it back, so each
// setter preserves the fields it does not own.
func (s *FileSeqStore) update(mutate func(st *seqState)) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	st, err := s.load()
	if err != nil {
		return err
	}
	mutate(&st)
	return s.save(st)
}

func (s *FileSeqStore) save(st seqState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("update: write seq state: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("update: write seq state: %w", err)
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
	return renameWithRetry(tmpName, s.path())
}

func (s *FileSeqStore) path() string { return filepath.Join(s.dir, seqStateName) }
