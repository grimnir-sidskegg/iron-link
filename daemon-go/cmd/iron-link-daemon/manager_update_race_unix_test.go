//go:build with_utls && !windows

// The compare-and-set half of apply_update, pinned deterministically with
// a FIFO as the staged file: VerifyFile parks reading it until the test's
// writer end closes, which opens a window to slip a download_update in
// while the hash is running with mu released.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/update"
)

// TestApplyUpdateLeavesNewerDownloadAlone: a download_update that starts
// while apply is hashing owns the state machine afterwards — apply must
// neither stamp verified/none over it nor delete anything, and the
// one-download-at-a-time guard must still hold. Both verify outcomes
// (the hash matching or not) hit the same compare-and-set.
func TestApplyUpdateLeavesNewerDownloadAlone(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "verify ok", true: "verify fails"}[corrupt], func(t *testing.T) {
			m := fixtureManager(t)
			payload, _ := stagedArtifact(t, m, 64<<10)
			// The staged "file" is a FIFO whose declared facts are the empty
			// body: Stat passes (size 0) and the hash reads until the writer
			// closes — a byte written first makes it fail instead.
			fifo := filepath.Join(t.TempDir(), "staged.exe")
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			empty := sha256.Sum256(nil)
			facts := update.Artifact{
				OS: runtime.GOOS, Arch: runtime.GOARCH, Kind: update.KindInstaller,
				Name: "staged.exe", SHA256: hex.EncodeToString(empty[:]),
			}
			m.mu.Lock()
			m.updateFilePath = fifo
			m.updateFileArtifact = &facts
			m.updateDownloadState = updateStateDownloaded
			m.mu.Unlock()

			applied := make(chan api.Response, 1)
			go func() { applied <- m.verifyDownloadedUpdate() }()
			// The blocking writer open returns only once apply's reader open
			// has arrived: from here apply is inside VerifyFile until Close.
			w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}

			gt := &gatedTransport{payload: payload, release: make(chan struct{})}
			m.updateTransport = gt
			m.updateDir = t.TempDir()
			if resp := m.startUpdateDownload(); resp.Status != api.StatusUpdateStatus || resp.UpdateStatus.DownloadState != updateStateDownloading {
				t.Fatalf("download_update during apply: %+v", resp)
			}
			waitForTransfer(t, gt)

			if corrupt {
				if _, err := w.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			}
			w.Close()
			var resp api.Response
			select {
			case resp = <-applied:
			case <-time.After(5 * time.Second):
				t.Fatal("apply did not return")
			}
			if resp.Status != api.StatusError || !strings.Contains(resp.Message, "state changed") {
				t.Fatalf("apply over a newer download: %+v", resp)
			}
			m.mu.Lock()
			state, path := m.updateDownloadState, m.updateFilePath
			m.mu.Unlock()
			if state != updateStateDownloading || path != "" {
				t.Fatalf("the newer download's state was clobbered: %q/%q", state, path)
			}
			if _, err := os.Stat(fifo); err != nil {
				t.Fatalf("apply must not delete a file it no longer owns: %v", err)
			}
			// The guard still holds: no second transfer.
			if again := m.startUpdateDownload(); again.UpdateStatus == nil || again.UpdateStatus.DownloadState != updateStateDownloading {
				t.Fatalf("second download_update: %+v", again)
			}
			if n := gt.calls.Load(); n != 1 {
				t.Fatalf("transfer count = %d, want 1", n)
			}
			close(gt.release)
			if state, _ := waitForDownloadState(t, m); state != updateStateDownloaded {
				t.Fatalf("settled state = %q, want downloaded", state)
			}
		})
	}
}
