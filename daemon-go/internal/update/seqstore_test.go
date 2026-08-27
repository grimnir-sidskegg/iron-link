package update

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

// TestFileSeqStoreCreatesRoot: a config root that does not exist yet (a
// daemon that has persisted nothing) is created by the first write, owner
// only — the first successful check must not fail on the floor write.
func TestFileSeqStoreCreatesRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	s := NewFileSeqStore(dir)
	if seq, err := s.LastSeenSeq(); err != nil || seq != 0 {
		t.Fatalf("missing root must read as 0, got %d, %v", seq, err)
	}
	if err := s.SetLastSeenSeq(7); err != nil {
		t.Fatalf("SetLastSeenSeq into a missing root: %v", err)
	}
	if seq, err := s.LastSeenSeq(); err != nil || seq != 7 {
		t.Fatalf("read back = %d, %v; want 7", seq, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("created root mode = %o, want 700", perm)
		}
	}
}

// TestFileSeqStoreFieldsCoexist: the floor, the revoked key ids and the
// attempt clock share one document, and each setter leaves the others as
// they were.
func TestFileSeqStoreFieldsCoexist(t *testing.T) {
	dir := t.TempDir()
	s := NewFileSeqStore(dir)
	if last, err := s.LastAttempt(); err != nil || !last.IsZero() {
		t.Fatalf("missing file must read as a zero attempt clock, got %v, %v", last, err)
	}
	if ids, err := s.RevokedKeyIDs(); err != nil || len(ids) != 0 {
		t.Fatalf("missing file must read as no revoked ids, got %v, %v", ids, err)
	}

	stamp := time.Date(2026, 8, 27, 12, 34, 56, 0, time.UTC)
	if err := s.SetLastAttempt(stamp); err != nil {
		t.Fatalf("SetLastAttempt: %v", err)
	}
	if err := s.RevokeKeyID(0xDEADBEEFCAFEF00D); err != nil {
		t.Fatalf("RevokeKeyID: %v", err)
	}
	if err := s.RevokeKeyID(0xDEADBEEFCAFEF00D); err != nil {
		t.Fatalf("RevokeKeyID again: %v", err)
	}
	if err := s.SetLastSeenSeq(7); err != nil {
		t.Fatalf("SetLastSeenSeq: %v", err)
	}

	fresh := NewFileSeqStore(dir)
	if seq, err := fresh.LastSeenSeq(); err != nil || seq != 7 {
		t.Fatalf("seq = %d, %v; want 7", seq, err)
	}
	if last, err := fresh.LastAttempt(); err != nil || !last.Equal(stamp) {
		t.Fatalf("attempt clock = %v, %v; want %v", last, err, stamp)
	}
	if ids, err := fresh.RevokedKeyIDs(); err != nil || fmt.Sprint(ids) != fmt.Sprint([]uint64{0xDEADBEEFCAFEF00D}) {
		t.Fatalf("revoked ids = %X, %v; want one DEADBEEFCAFEF00D", ids, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, seqStateName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"revoked_key_ids":["deadbeefcafef00d"]`) {
		t.Fatalf("revoked ids must persist as hex, got %s", data)
	}
}

func TestFileSeqStoreCorruptRevokedIDErrors(t *testing.T) {
	dir := t.TempDir()
	doc := `{"last_seen_seq":7,"revoked_key_ids":["not-hex"]}`
	if err := os.WriteFile(filepath.Join(dir, seqStateName), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileSeqStore(dir).RevokedKeyIDs(); err == nil {
		t.Fatal("a corrupt revoked id must error, not read as none")
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
