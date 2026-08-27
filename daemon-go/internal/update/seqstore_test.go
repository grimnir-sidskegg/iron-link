package update

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFileSeqStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewFileSeqStore(dir)

	if seq, err := s.LastSeenSeq(); err != nil || seq != 0 {
		t.Fatalf("missing file must read as 0, got %d, %v", seq, err)
	}
	if err := s.SetLastSeenSeq(7); err != nil {
		t.Fatalf("SetLastSeenSeq: %v", err)
	}
	if seq, err := s.LastSeenSeq(); err != nil || seq != 7 {
		t.Fatalf("read back = %d, %v; want 7", seq, err)
	}
	// A fresh store over the same dir sees the persisted floor.
	if seq, err := NewFileSeqStore(dir).LastSeenSeq(); err != nil || seq != 7 {
		t.Fatalf("fresh store read = %d, %v; want 7", seq, err)
	}
	if err := s.SetLastSeenSeq(9); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if seq, _ := s.LastSeenSeq(); seq != 9 {
		t.Fatalf("after advance = %d, want 9", seq)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, seqStateName))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("state file mode = %o, want 600", perm)
		}
	}
}

func TestFileSeqStoreCorruptFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, seqStateName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileSeqStore(dir).LastSeenSeq(); err == nil {
		t.Fatal("a corrupt state file must error, not read as seq 0")
	}
}
