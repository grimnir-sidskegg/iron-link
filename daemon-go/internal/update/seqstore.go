package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// seqStateName is the anti-downgrade floor's file at the config root.
const seqStateName = "update_state.json"

// FileSeqStore is the SeqStore persisted as a small JSON document
// ({"last_seen_seq": N}) in the daemon's config root, written with the
// same-directory temp + rename pattern every other persisted state uses.
type FileSeqStore struct {
	dir string
}

var _ SeqStore = (*FileSeqStore)(nil)

// NewFileSeqStore returns the store rooted at dir (the config root). The
// directory is created on the first write: a daemon that has persisted
// nothing yet (a fresh install) has no config root, and its first
// successful check must not fail on the floor write.
func NewFileSeqStore(dir string) *FileSeqStore { return &FileSeqStore{dir: dir} }

type seqState struct {
	LastSeenSeq uint64 `json:"last_seen_seq"`
}

// LastSeenSeq loads the floor; a missing file means 0 (nothing accepted
// yet). A corrupt file is an error — silently reading it as 0 would drop
// the anti-downgrade protection.
func (s *FileSeqStore) LastSeenSeq() (uint64, error) {
	data, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("update: read seq state: %w", err)
	}
	var st seqState
	if err := json.Unmarshal(data, &st); err != nil {
		return 0, fmt.Errorf("update: decode seq state: %w", err)
	}
	return st.LastSeenSeq, nil
}

// SetLastSeenSeq atomically persists a new floor (temp + rename, 0600).
func (s *FileSeqStore) SetLastSeenSeq(seq uint64) error {
	data, err := json.Marshal(seqState{LastSeenSeq: seq})
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
