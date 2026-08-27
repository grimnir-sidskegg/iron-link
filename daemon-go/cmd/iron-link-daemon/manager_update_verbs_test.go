//go:build with_utls

// The update verbs, offline: check_update cached-vs-force, the async
// download orchestration with its progress events, apply's re-verify, and
// the update_available dedupe. Everything network-shaped goes through stub
// transports (nothing is dialed); events are observed through a REAL
// subscribe connection against the manager's hub, exactly as a client
// would see them.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/ipc"
	"ironlink/daemon/internal/update"
)

// subscribeEvents serves m over a real unix-socket IPC server, opens a
// subscribe connection, drops the initial state snapshot, and streams every
// following event into the returned channel.
func subscribeEvents(t *testing.T, m *manager) <-chan api.Event {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := ipc.NewServer(ipc.HandlerFunc(m.Handle), m.hub)
	go srv.Serve(l)
	t.Cleanup(func() { l.Close() })
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := ipc.WriteFrame(conn, api.Request{Command: api.CmdSubscribe}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	var first api.Event
	if err := ipc.ReadFrame(conn, &first); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	ch := make(chan api.Event, 64)
	go func() {
		defer close(ch)
		for {
			var ev api.Event
			if err := ipc.ReadFrame(conn, &ev); err != nil {
				return
			}
			ch <- ev
		}
	}()
	return ch
}

// collectEvents drains ch for events tagged kind until it stays quiet.
func collectEvents(ch <-chan api.Event, kind string, quiet time.Duration) []api.Event {
	var out []api.Event
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			if ev.Event == kind {
				out = append(out, ev)
			}
		case <-time.After(quiet):
			return out
		}
	}
}

// updateFixtureManager wires the signed-manifest stub onto a fixtureManager:
// version v1.0.0 (the fixture manifest is v1.2.3) and a fixed clock inside
// the manifest's validity window.
func updateFixtureManager(t *testing.T) (*manager, *fixtureUpdateTransport) {
	t.Helper()
	m := fixtureManager(t)
	m.version = "v1.0.0"
	ft := &fixtureUpdateTransport{t: t}
	m.updateTransport = ft
	m.updateURLs = []string{"https://update.example.com/update.json"}
	fixedNow := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	m.updateNow = func() time.Time { return fixedNow }
	return m, ft
}

// TestCheckUpdateCachedVsForce: the bare verb serves the cache without any
// network I/O; force fetches through the (stub) transport regardless of
// auto_update; with both transport flags off force degrades to the cached
// status carrying transport "disabled".
func TestCheckUpdateCachedVsForce(t *testing.T) {
	m, ft := updateFixtureManager(t)

	// Cached path: never checked, never fetches.
	resp := m.Handle(api.Request{Command: api.CmdCheckUpdate})
	if resp.Status != api.StatusUpdateStatus || resp.UpdateStatus == nil {
		t.Fatalf("check_update: %+v", resp)
	}
	if n := ft.calls.Load(); n != 0 {
		t.Fatalf("a non-force check must not fetch, saw %d requests", n)
	}
	st := resp.UpdateStatus
	if st.CheckedAt != "" || st.Available || st.CurrentVersion != "v1.0.0" {
		t.Fatalf("pre-check status: %+v", st)
	}
	if st.Transport != "direct" {
		t.Fatalf("transport = %q, want direct (no session, defaults ON)", st.Transport)
	}

	// Force works even with the auto_update master OFF ("Check now").
	s, err := m.store.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.AutoUpdate = false
	if err := m.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	resp = m.Handle(api.Request{Command: api.CmdCheckUpdate, Force: true})
	if ft.calls.Load() == 0 {
		t.Fatal("force must fetch through the injected transport")
	}
	st = resp.UpdateStatus
	if st == nil || !st.Available || st.LatestVersion != "v1.2.3" {
		t.Fatalf("forced status: %+v", st)
	}
	if st.CheckedAt != "2026-08-27T12:00:00Z" {
		t.Fatalf("checked_at = %q", st.CheckedAt)
	}
	m.mu.Lock()
	attempted := !m.updateLastAttempt.IsZero()
	m.mu.Unlock()
	if !attempted {
		t.Fatal("a forced check must advance the loop's attempt clock")
	}

	// Both transport flags off: force cannot fetch; the cached verdict is
	// still served, with the skip explained by transport "disabled".
	s.UpdateViaTunnel, s.UpdateViaDirect = false, false
	if err := m.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	before := ft.calls.Load()
	resp = m.Handle(api.Request{Command: api.CmdCheckUpdate, Force: true})
	if n := ft.calls.Load(); n != before {
		t.Fatalf("force with the transport disabled must not fetch (%d -> %d)", before, n)
	}
	st = resp.UpdateStatus
	if st == nil || st.Transport != "disabled" {
		t.Fatalf("disabled-transport status: %+v", st)
	}
	if !st.Available || st.LatestVersion != "v1.2.3" {
		t.Fatalf("the cache must still be served when the transport is off: %+v", st)
	}
}

// TestUpdateAvailableDedupe: two forced checks that land the SAME version
// broadcast exactly one update_available.
func TestUpdateAvailableDedupe(t *testing.T) {
	m, _ := updateFixtureManager(t)
	ch := subscribeEvents(t, m)

	for i := 0; i < 2; i++ {
		if resp := m.Handle(api.Request{Command: api.CmdCheckUpdate, Force: true}); resp.Status != api.StatusUpdateStatus {
			t.Fatalf("check %d: %+v", i, resp)
		}
	}
	events := collectEvents(ch, api.EventUpdateAvailable, 500*time.Millisecond)
	if len(events) != 1 {
		t.Fatalf("update_available events = %d, want 1 (deduped per version)", len(events))
	}
	ev := events[0]
	if ev.Version != "v1.2.3" || ev.NotesURL != "https://example.com/iron-link/notes/v1.2.3" {
		t.Fatalf("update_available: %+v", ev)
	}
}

// gatedTransport serves a fixed payload: the first half immediately, the
// rest after release is closed — so a test can observe the "downloading"
// state and the idempotency guard deterministically. Like a real transport
// it honors the request context: a cancel mid-body ends the stream with
// the context error.
type gatedTransport struct {
	payload []byte
	release chan struct{}
	calls   atomic.Int32
}

func (g *gatedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	g.calls.Add(1)
	pr, pw := io.Pipe()
	go func() {
		half := len(g.payload) / 2
		if _, err := pw.Write(g.payload[:half]); err != nil {
			return
		}
		select {
		case <-g.release:
		case <-req.Context().Done():
			pw.CloseWithError(req.Context().Err())
			return
		}
		if _, err := pw.Write(g.payload[half:]); err != nil {
			return
		}
		pw.Close()
	}()
	return &http.Response{StatusCode: http.StatusOK, Body: pr, Request: req}, nil
}

// waitForTransfer blocks until the gated transport has been asked for its
// payload (the download goroutine races the caller's next assertion).
func waitForTransfer(t *testing.T, gt *gatedTransport) {
	t.Helper()
	for started := time.Now(); gt.calls.Load() == 0; {
		if time.Since(started) > 5*time.Second {
			t.Fatal("the download goroutine never issued its request")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForDownloadState polls the state machine until it leaves
// "downloading" and returns the settled state + path.
func waitForDownloadState(t *testing.T, m *manager) (string, string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		state, path := m.updateDownloadState, m.updateFilePath
		m.mu.Unlock()
		if state != updateStateDownloading {
			return state, path
		}
		if time.Now().After(deadline) {
			t.Fatal("download did not settle")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stageDownloadedFile lands payload in dir as if runUpdateDownload had
// finished: file on disk, state downloaded, path + artifact facts recorded.
func stageDownloadedFile(t *testing.T, m *manager, dir string, payload []byte, artifact update.Artifact) string {
	t.Helper()
	path := filepath.Join(dir, artifact.Name)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.updateFilePath = path
	m.updateFileArtifact = &artifact
	m.updateDownloadState = updateStateDownloaded
	m.mu.Unlock()
	return path
}

// dirNames lists the entries in dir.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// stagedArtifact builds a payload + the matching manifest artifact facts
// and plants them as m's cached check result, as if a verified check had
// found a downloadable update for THIS platform.
func stagedArtifact(t *testing.T, m *manager, size int) ([]byte, update.Artifact) {
	t.Helper()
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i)
	}
	sum := sha256.Sum256(payload)
	artifact := update.Artifact{
		OS: runtime.GOOS, Arch: runtime.GOARCH, Kind: update.KindInstaller,
		Name: "iron-link-setup.exe", Size: int64(len(payload)),
		SHA256: hex.EncodeToString(sum[:]),
		URLs:   []string{"https://update.example.com/iron-link-setup.exe"},
	}
	m.mu.Lock()
	m.updateResult = &update.Result{
		CheckedAt: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
		Available: true, LatestVersion: "v1.2.3", Artifact: &artifact,
	}
	m.mu.Unlock()
	return payload, artifact
}

// TestDownloadUpdateOrchestration drives the platform-independent download
// seam end to end: async start, live progress events, the one-at-a-time
// idempotency guard, and the landed verified file.
func TestDownloadUpdateOrchestration(t *testing.T) {
	m := fixtureManager(t)
	payload, artifact := stagedArtifact(t, m, 64<<10)
	gt := &gatedTransport{payload: payload, release: make(chan struct{})}
	m.updateTransport = gt
	m.updateDir = t.TempDir()
	ch := subscribeEvents(t, m)

	resp := m.startUpdateDownload()
	if resp.Status != api.StatusUpdateStatus || resp.UpdateStatus == nil {
		t.Fatalf("download_update: %+v", resp)
	}
	if resp.UpdateStatus.DownloadState != updateStateDownloading {
		t.Fatalf("download_state = %q, want downloading", resp.UpdateStatus.DownloadState)
	}
	if resp.UpdateStatus.DownloadTotal != artifact.Size {
		t.Fatalf("download_total = %d, want %d", resp.UpdateStatus.DownloadTotal, artifact.Size)
	}

	// A second download while one runs is idempotent: same status reply,
	// no second transfer. Wait for the first transfer to actually start
	// (the goroutine races this assertion) before pinning the count.
	again := m.startUpdateDownload()
	if again.Status != api.StatusUpdateStatus || again.UpdateStatus.DownloadState != updateStateDownloading {
		t.Fatalf("second download_update: %+v", again)
	}
	waitForTransfer(t, gt)
	if n := gt.calls.Load(); n != 1 {
		t.Fatalf("transfer count = %d, want 1 (idempotent)", n)
	}

	close(gt.release)
	state, path := waitForDownloadState(t, m)
	if state != updateStateDownloaded {
		t.Fatalf("settled state = %q, want downloaded", state)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read landed file: %v", err)
	}
	if len(data) != len(payload) {
		t.Fatalf("landed %d bytes, want %d", len(data), len(payload))
	}
	m.mu.Lock()
	landed := m.updateFileArtifact
	m.mu.Unlock()
	if landed == nil || !sameArtifact(*landed, artifact) {
		t.Fatalf("landed artifact facts = %+v, want %+v", landed, artifact)
	}

	events := collectEvents(ch, api.EventUpdateProgress, 500*time.Millisecond)
	if len(events) < 2 {
		t.Fatalf("update_progress events = %d, want at least a downloading + the final one", len(events))
	}
	first, last := events[0], events[len(events)-1]
	if first.State != updateStateDownloading || first.DownloadReceived <= 0 || first.DownloadTotal != artifact.Size {
		t.Fatalf("first progress event: %+v", first)
	}
	if last.State != updateStateDownloaded || last.DownloadReceived != artifact.Size || last.DownloadTotal != artifact.Size {
		t.Fatalf("final progress event: %+v", last)
	}
}

// TestDownloadUpdateRequiresArtifact: without a cached downloadable result
// the orchestration refuses instead of fetching anything.
func TestDownloadUpdateRequiresArtifact(t *testing.T) {
	m := fixtureManager(t)
	resp := m.startUpdateDownload()
	if resp.Status != api.StatusError {
		t.Fatalf("download without an artifact: %+v", resp)
	}
}

// TestUpdateVerbsNotSupportedOffWindows: the verb layer gates the platform
// — both download_update and apply_update answer the same error.
func TestUpdateVerbsNotSupportedOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows is the supported platform")
	}
	m := fixtureManager(t)
	stagedArtifact(t, m, 1024)
	for _, cmd := range []string{api.CmdDownloadUpdate, api.CmdApplyUpdate} {
		resp := m.Handle(api.Request{Command: cmd})
		if resp.Status != api.StatusError || resp.Message != updateNotSupportedMsg {
			t.Fatalf("%s: %+v", cmd, resp)
		}
	}
}

// TestApplyUpdateVerify drives the platform-independent apply seam: a good
// staged file verifies and hands back setup_path; a corrupted one is
// deleted and the state reset so the client can re-download.
func TestApplyUpdateVerify(t *testing.T) {
	m := fixtureManager(t)
	payload, artifact := stagedArtifact(t, m, 4096)
	dir := t.TempDir()

	// Nothing downloaded yet.
	if resp := m.verifyDownloadedUpdate(); resp.Status != api.StatusError {
		t.Fatalf("apply without a download: %+v", resp)
	}

	// Happy path: the staged file matches the facts it was landed against.
	good := stageDownloadedFile(t, m, dir, payload, artifact)
	resp := m.verifyDownloadedUpdate()
	if resp.Status != api.StatusUpdateStatus || resp.UpdateStatus == nil {
		t.Fatalf("apply: %+v", resp)
	}
	if resp.UpdateStatus.DownloadState != updateStateVerified || resp.UpdateStatus.SetupPath != good {
		t.Fatalf("apply status: %+v", resp.UpdateStatus)
	}

	// Verify failure: the file was swapped after the download — it must be
	// deleted and the state machine reset to none.
	if err := os.WriteFile(good, append(payload, 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.updateDownloadState = updateStateDownloaded
	m.mu.Unlock()
	resp = m.verifyDownloadedUpdate()
	if resp.Status != api.StatusError {
		t.Fatalf("apply of a swapped file: %+v", resp)
	}
	if _, err := os.Stat(good); !os.IsNotExist(err) {
		t.Fatalf("corrupt file must be deleted, stat err = %v", err)
	}
	m.mu.Lock()
	state, path, landed := m.updateDownloadState, m.updateFilePath, m.updateFileArtifact
	m.mu.Unlock()
	if state != "" || path != "" || landed != nil {
		t.Fatalf("state after verify failure = %q/%q/%v, want reset", state, path, landed)
	}
}

// TestApplyUpdateVerifiesAgainstLandedFacts: apply hashes the file against
// the facts it was downloaded with, not against whatever the cache holds
// now — the cache moving is handled by discarding the file, never by
// re-describing it.
func TestApplyUpdateVerifiesAgainstLandedFacts(t *testing.T) {
	m := fixtureManager(t)
	payload, artifact := stagedArtifact(t, m, 4096)
	good := stageDownloadedFile(t, m, t.TempDir(), payload, artifact)

	// The cache is replaced by a same-named artifact of another version
	// without going through runUpdateCheck (which would discard the file);
	// the staged file must still verify against its own facts.
	other := artifact
	other.Size, other.SHA256 = artifact.Size+1, "00"+artifact.SHA256[2:]
	m.mu.Lock()
	m.updateResult = &update.Result{Available: true, LatestVersion: "v1.2.4", Artifact: &other}
	m.mu.Unlock()
	resp := m.verifyDownloadedUpdate()
	if resp.Status != api.StatusUpdateStatus || resp.UpdateStatus == nil || resp.UpdateStatus.SetupPath != good {
		t.Fatalf("apply against the landed facts: %+v", resp)
	}
	if _, err := os.Stat(good); err != nil {
		t.Fatalf("a valid staged file must survive: %v", err)
	}
}

// TestUpdateCheckDiscardsSupersededFile: a check whose result no longer
// describes the staged file (another version, or no artifact at all)
// deletes it and resets the state; a check landing the SAME artifact
// keeps it.
func TestUpdateCheckDiscardsSupersededFile(t *testing.T) {
	newer := func(a update.Artifact) *update.Result {
		a.Name, a.SHA256 = "iron-link-1.2.4-setup.exe", "00"+a.SHA256[2:]
		return &update.Result{Available: true, LatestVersion: "v1.2.4", Artifact: &a}
	}
	cases := []struct {
		name    string
		result  func(a update.Artifact) *update.Result
		discard bool
	}{
		{"same artifact", func(a update.Artifact) *update.Result {
			return &update.Result{Available: true, LatestVersion: "v1.2.3", Artifact: &a}
		}, false},
		{"same artifact, other mirrors", func(a update.Artifact) *update.Result {
			a.URLs = []string{"https://mirror.example.com/iron-link-setup.exe"}
			return &update.Result{Available: true, LatestVersion: "v1.2.3", Artifact: &a}
		}, false},
		{"newer version", newer, true},
		{"expired manifest, artifact gone", func(update.Artifact) *update.Result {
			return &update.Result{Stale: true, LatestVersion: "v1.2.3"}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := fixtureManager(t)
			payload, artifact := stagedArtifact(t, m, 1024)
			path := stageDownloadedFile(t, m, t.TempDir(), payload, artifact)
			m.mu.Lock()
			m.updateResult = tc.result(artifact)
			superseded := m.stagedUpdateSupersededLocked()
			if superseded {
				if _, err := m.discardStagedUpdateLocked(); err != nil {
					t.Errorf("discard: %v", err)
				}
			}
			state, filePath := m.updateDownloadState, m.updateFilePath
			m.mu.Unlock()
			if superseded != tc.discard {
				t.Fatalf("superseded = %v, want %v", superseded, tc.discard)
			}
			_, statErr := os.Stat(path)
			if tc.discard {
				if state != "" || filePath != "" || !os.IsNotExist(statErr) {
					t.Fatalf("state %q/%q, stat err %v: want reset + file removed", state, filePath, statErr)
				}
			} else if state != updateStateDownloaded || filePath != path || statErr != nil {
				t.Fatalf("state %q/%q, stat err %v: want the file kept", state, filePath, statErr)
			}
		})
	}

	// Through the real verb: the fixture manifest (v1.2.3, notify-only on
	// this platform or a different installer) lands over a staged file of
	// another artifact — check_update must not keep reporting it.
	m, _ := updateFixtureManager(t)
	payload, artifact := stagedArtifact(t, m, 1024)
	path := stageDownloadedFile(t, m, t.TempDir(), payload, artifact)
	resp := m.Handle(api.Request{Command: api.CmdCheckUpdate, Force: true})
	if resp.Status != api.StatusUpdateStatus || resp.UpdateStatus == nil {
		t.Fatalf("forced check: %+v", resp)
	}
	if resp.UpdateStatus.DownloadState != "" {
		t.Fatalf("download_state = %q after the cache moved, want none", resp.UpdateStatus.DownloadState)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("superseded file must be removed, stat err = %v", err)
	}
	if resp := m.verifyDownloadedUpdate(); resp.Status != api.StatusError {
		t.Fatalf("apply after the cache moved: %+v", resp)
	}
}

// TestDownloadUpdateSupersededInFlight: a check that moves the cache while
// a transfer runs makes the landed file obsolete — it is discarded and the
// state reads failed (the client re-downloads the current artifact), never
// downloaded for a version that is not on disk.
func TestDownloadUpdateSupersededInFlight(t *testing.T) {
	m := fixtureManager(t)
	payload, artifact := stagedArtifact(t, m, 64<<10)
	gt := &gatedTransport{payload: payload, release: make(chan struct{})}
	m.updateTransport = gt
	m.updateDir = t.TempDir()
	ch := subscribeEvents(t, m)

	if resp := m.startUpdateDownload(); resp.Status != api.StatusUpdateStatus {
		t.Fatalf("download_update: %+v", resp)
	}
	waitForTransfer(t, gt)
	other := artifact
	other.Name, other.SHA256 = "iron-link-1.2.4-setup.exe", "00"+artifact.SHA256[2:]
	m.mu.Lock()
	m.updateResult = &update.Result{Available: true, LatestVersion: "v1.2.4", Artifact: &other}
	m.mu.Unlock()
	close(gt.release)

	state, path := waitForDownloadState(t, m)
	if state != updateStateFailed || path != "" {
		t.Fatalf("settled state = %q/%q, want failed with no file", state, path)
	}
	if names := dirNames(t, m.updateDir); len(names) != 0 {
		t.Fatalf("superseded file must be discarded, dir has %v", names)
	}
	events := collectEvents(ch, api.EventUpdateProgress, 500*time.Millisecond)
	if len(events) == 0 || events[len(events)-1].State != updateStateFailed {
		t.Fatalf("final progress event: %+v", events)
	}
}

// TestDownloadUpdateSweepsStaleTemps: temp files of a transfer the process
// died in (never finalized, never removed) are swept when the next
// download starts, so they cannot accumulate.
func TestDownloadUpdateSweepsStaleTemps(t *testing.T) {
	m := fixtureManager(t)
	payload, artifact := stagedArtifact(t, m, 4096)
	m.updateTransport = &gatedTransport{payload: payload, release: make(chan struct{})}
	m.updateDir = t.TempDir()
	for _, name := range []string{".update-1234", ".update-abcd"} {
		if err := os.WriteFile(filepath.Join(m.updateDir, name), []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	close(m.updateTransport.(*gatedTransport).release)
	if resp := m.startUpdateDownload(); resp.Status != api.StatusUpdateStatus {
		t.Fatalf("download_update: %+v", resp)
	}
	if state, _ := waitForDownloadState(t, m); state != updateStateDownloaded {
		t.Fatalf("settled state = %q, want downloaded", state)
	}
	if names := dirNames(t, m.updateDir); len(names) != 1 || names[0] != artifact.Name {
		t.Fatalf("dir after the download = %v, want only %s", names, artifact.Name)
	}
}

// TestShutdownAbortsDownload: shutdown cancels a running transfer and
// waits for it, so the temp file is removed before the process exits.
func TestShutdownAbortsDownload(t *testing.T) {
	m := fixtureManager(t)
	payload, _ := stagedArtifact(t, m, 64<<10)
	gt := &gatedTransport{payload: payload, release: make(chan struct{})}
	m.updateTransport = gt
	m.updateDir = t.TempDir()

	if resp := m.startUpdateDownload(); resp.Status != api.StatusUpdateStatus {
		t.Fatalf("download_update: %+v", resp)
	}
	// Wait until the transfer is mid-body (its temp file exists).
	for started := time.Now(); len(dirNames(t, m.updateDir)) == 0; {
		if time.Since(started) > 5*time.Second {
			t.Fatal("the transfer never opened its temp file")
		}
		time.Sleep(5 * time.Millisecond)
	}
	done := make(chan struct{})
	go func() {
		m.shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not return: the download goroutine was not waited for")
	}
	m.mu.Lock()
	state := m.updateDownloadState
	m.mu.Unlock()
	if state != updateStateFailed {
		t.Fatalf("state after shutdown = %q, want failed", state)
	}
	if names := dirNames(t, m.updateDir); len(names) != 0 {
		t.Fatalf("temp file left behind after shutdown: %v", names)
	}
}
