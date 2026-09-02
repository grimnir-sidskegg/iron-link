// The update system's daemon side: a jittered daily background check of
// the signed update manifest, cached in-memory and served over the wire by
// check_update, plus the download/apply verb orchestration. The transport
// follows the settings flags and is recomputed at every attempt: through
// the live session's outbound first, TUN-exempt direct as the fallback,
// disabled when both flags are off. Fail-soft throughout — a blocked
// mirror is a normal condition for this daemon, so errors are logged and
// the previous cache is kept.
package main

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/engine"
	"ironlink/daemon/internal/update"
)

// updateManifestURLs are the production manifest mirrors, tried in order
// (the detached signature sits at "<url>.minisig" beside each). The signed
// pair is committed to updates/ on main; the publish-update workflow copies
// it onto the matching GitHub release, which is what the second URL serves.
// Older daemons carry the retired "updates" branch URL — the same workflow
// mirrors the pair there so they keep seeing releases. A dedicated mirror
// is one added line.
var updateManifestURLs = []string{
	"https://raw.githubusercontent.com/grimnir-sidskegg/iron-link/main/updates/update.json",
	"https://github.com/grimnir-sidskegg/iron-link/releases/latest/download/update.json",
}

// updateURLEnv replaces the production mirrors for pre-publish testing: a
// comma-separated manifest URL list, read once at startup. Plain http:// is
// accepted under the override ONLY — the minisign signature carries the
// trust, so a local static server is enough to exercise the whole flow.
const updateURLEnv = "IRON_LINK_UPDATE_URL"

// parseUpdateURLOverride parses the updateURLEnv value: nil for an unset
// (or blank) variable, the URL list when every entry is an http(s) URL, and
// an error — the override is then ignored as a whole — on any entry that
// is not one.
func parseUpdateURLOverride(raw string) ([]string, error) {
	var urls []string
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		u, err := url.Parse(entry)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%s: %q is not an http(s) URL", updateURLEnv, entry)
		}
		urls = append(urls, entry)
	}
	return urls, nil
}

// applyUpdateURLOverride installs the updateURLEnv override at startup;
// the production list stays in force when the variable is unset or
// rejected. One line goes to logw whenever the variable is set.
func (m *manager) applyUpdateURLOverride(getenv func(string) string, logw io.Writer) {
	urls, err := parseUpdateURLOverride(getenv(updateURLEnv))
	if err != nil {
		fmt.Fprintf(logw, "iron-link-daemon: update manifest URL override ignored: %v\n", err)
		return
	}
	if urls == nil {
		return
	}
	m.updateURLs = urls
	fmt.Fprintf(logw, "iron-link-daemon: update manifest URL overridden by %s\n", updateURLEnv)
}

// manifestURLs is the list a check fetches from: the startup/test override
// when one is set, the production mirrors otherwise.
func (m *manager) manifestURLs() []string {
	if m.updateURLs != nil {
		return m.updateURLs
	}
	return updateManifestURLs
}

const (
	// updateInitialDelay (plus up to the same again of jitter) holds the
	// first attempt back until session restore has settled.
	updateInitialDelay = 60 * time.Second
	// updateTickInterval is how often the loop re-evaluates whether a check
	// is due — settings and the session change between ticks.
	updateTickInterval = time.Hour
	// updateCheckInterval is the target cadence between check attempts,
	// jittered by ±10% so checks never form a clock-aligned pattern.
	updateCheckInterval = 24 * time.Hour
	// updateDialTimeout bounds the direct dialer's connect; the fetch as a
	// whole is bounded inside update.Check.
	updateDialTimeout = 15 * time.Second
)

// updateLoop runs for the daemon's lifetime: a short jittered initial
// delay, then an hourly tick that runs one check when auto_update is on, a
// transport is available, and the jittered daily interval has elapsed since
// the last ATTEMPT. The attempt clock is seeded from the persisted state:
// an auto-start service restarts on every boot, and an in-memory clock
// alone would fetch 60-120 s after each boot — a boot-correlated pattern in
// place of the jittered daily one. m.updateLastAttempt is a manager field
// (not loop state) so a forced check_update also pushes the next automatic
// check out.
func (m *manager) updateLoop(ctx context.Context) {
	m.seedUpdateAttemptClock()
	select {
	case <-ctx.Done():
		return
	case <-time.After(updateInitialDelay + rand.N(updateInitialDelay)):
	}
	due := jitterInterval(updateCheckInterval)
	attempt := func() {
		m.mu.Lock()
		last := m.updateLastAttempt
		m.mu.Unlock()
		if !last.IsZero() && time.Since(last) < due {
			return
		}
		if m.tryUpdateCheck(ctx) {
			due = jitterInterval(updateCheckInterval)
		}
	}
	attempt()
	ticker := time.NewTicker(updateTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			attempt()
		}
	}
}

// jitterInterval spreads d by ±10%.
func jitterInterval(d time.Duration) time.Duration {
	return d - d/10 + rand.N(d/5)
}

// tryUpdateCheck runs one policy-gated check attempt and reports whether a
// fetch was actually attempted — the loop advances its cadence only then,
// so a flag flipped ON is picked up by the next hourly tick, not in a day.
// mu is held only for the settings/session snapshot and the result write,
// never across network I/O.
func (m *manager) tryUpdateCheck(ctx context.Context) bool {
	transport, mode, settings, ok := m.resolveUpdateTransport()
	if !ok || !settings.AutoUpdate || transport == nil {
		return false
	}
	m.noteUpdateAttempt()
	m.runUpdateCheck(ctx, transport, mode)
	return true
}

// forceUpdateCheck is the "Check now" path behind check_update{force}:
// allowed regardless of auto_update, but the transport flags still govern —
// with both off there is nothing to fetch through, so the caller serves the
// cached status unchanged (its transport field reads "disabled").
func (m *manager) forceUpdateCheck(ctx context.Context) {
	transport, mode, _, ok := m.resolveUpdateTransport()
	if !ok || transport == nil {
		return
	}
	m.noteUpdateAttempt()
	m.runUpdateCheck(ctx, transport, mode)
}

// resolveUpdateTransport snapshots settings + session under mu and applies
// the transport policy. The TEST transport substitutes only the MECHANISM —
// the flags stay authoritative either way (they govern check and download
// alike). ok=false on a settings error (logged).
func (m *manager) resolveUpdateTransport() (http.RoundTripper, string, api.Settings, bool) {
	m.mu.Lock()
	settings, err := m.store.LoadSettings()
	sess := m.sess
	m.mu.Unlock()
	if err != nil {
		m.logSink("info", "update check: settings: "+err.Error())
		return nil, "", api.Settings{}, false
	}
	transport, mode := chooseUpdateTransport(settings, sess)
	if transport != nil && m.updateTransport != nil {
		transport = m.updateTransport
	}
	return transport, mode, settings, true
}

// noteUpdateAttempt stamps the shared attempt clock the loop's cadence
// keys on, in memory and on disk. The persisted copy only seeds the next
// process (losing it costs one early check), so a write error is logged,
// not propagated.
func (m *manager) noteUpdateAttempt() {
	now := time.Now()
	m.mu.Lock()
	m.updateLastAttempt = now
	m.mu.Unlock()
	if err := m.updateStateStore().SetLastAttempt(now); err != nil {
		m.logSink("info", "update check: persist attempt clock: "+err.Error())
	}
}

// seedUpdateAttemptClock loads the persisted attempt clock into
// m.updateLastAttempt unless a check already ran in this process. A stamp
// from the future (the clock was set back) is discarded: honouring it could
// silence checks for as long as the clock had been ahead.
func (m *manager) seedUpdateAttemptClock() {
	last, err := m.updateStateStore().LastAttempt()
	if err != nil {
		m.logSink("info", "update check: attempt clock: "+err.Error())
		return
	}
	if last.IsZero() || last.After(time.Now()) {
		return
	}
	m.mu.Lock()
	if m.updateLastAttempt.IsZero() {
		m.updateLastAttempt = last
	}
	m.mu.Unlock()
}

// updateStateStore is the persisted update trust state at the config root:
// the anti-downgrade floor, the revoked key ids and the attempt clock.
func (m *manager) updateStateStore() *update.FileSeqStore {
	return update.NewFileSeqStore(m.store.Dir())
}

// chooseUpdateTransport maps the transport flags onto a way to reach the
// manifest, recomputed at every attempt (the session comes and goes):
// tunnel-first through the live selector outbound (in-process, so it works
// in TUN and SOCKS modes alike — a TUN session has no local SOCKS inbound
// to dial), else a TUN-exempt direct dial (fwmark on Linux, interface-bind
// elsewhere, plain direct when no TUN is up), else nothing — a nil
// transport skips the check entirely.
func chooseUpdateTransport(settings api.Settings, sess *engine.Session) (http.RoundTripper, string) {
	if sess != nil && settings.UpdateViaTunnel {
		return &http.Transport{DialContext: sess.ProxyDialContext}, "tunnel"
	}
	if settings.UpdateViaDirect {
		return &http.Transport{DialContext: engine.TunExemptDialer(updateDialTimeout).DialContext}, "direct"
	}
	return nil, ""
}

// runUpdateCheck fetches and evaluates the manifest, then caches the result
// under mu. Errors are informational only (a blocked endpoint is normal for
// this daemon) and keep the previous cached result.
func (m *manager) runUpdateCheck(ctx context.Context, transport http.RoundTripper, mode string) {
	res, err := update.Check(ctx, update.Config{
		CurrentVersion: m.version,
		ManifestURLs:   m.manifestURLs(),
		Transport:      transport,
		SeqStore:       m.updateStateStore(),
		Now:            m.updateNow, // nil = time.Now
	})
	// Drop any keep-alive connection: a tunnel-dialled idle conn must not
	// outlive the check (the session may close right after).
	if tr, ok := transport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
	if err != nil {
		m.logSink("info", fmt.Sprintf("update check (%s): %v", mode, err))
		return
	}
	m.mu.Lock()
	m.updateResult = res
	// A staged file the fresh result no longer describes (a newer version,
	// or the artifact gone with an expired manifest) is dropped here, so
	// download_state never reports a file for a version that is not on
	// disk and apply never re-hashes it against the wrong facts.
	var discarded string
	var rmErr error
	if m.stagedUpdateSupersededLocked() {
		discarded, rmErr = m.discardStagedUpdateLocked()
	}
	// Broadcast update_available once per version (a nudge only — the hub
	// has no replay, so check_update stays the source of truth): the daily
	// tick and repeated forced checks must not re-announce, but a NEWER
	// version arriving over an already-announced one must.
	announce := res.Available && res.LatestVersion != m.updateAnnounced
	if announce {
		m.updateAnnounced = res.LatestVersion
	}
	m.mu.Unlock()
	if discarded != "" {
		m.logSink("info", fmt.Sprintf("update check (%s): discarded staged %s, the cached update changed", mode, discarded))
		if rmErr != nil {
			m.logSink("info", "update check: remove staged file: "+rmErr.Error())
		}
	}
	if announce && m.hub != nil {
		kind := update.KindNone
		if res.Artifact != nil {
			kind = res.Artifact.Kind
		}
		m.hub.Broadcast(api.Event{
			Event:    api.EventUpdateAvailable,
			Version:  res.LatestVersion,
			NotesURL: res.NotesURL,
			Kind:     kind,
		})
	}
	switch {
	case res.Available:
		m.logSink("info", fmt.Sprintf("update check (%s): %s is available", mode, res.LatestVersion))
	case res.Stale:
		m.logSink("info", fmt.Sprintf("update check (%s): manifest is stale (latest %s)", mode, res.LatestVersion))
	default:
		m.logSink("info", fmt.Sprintf("update check (%s): up to date", mode))
	}
}

// ---- The update wire verbs --------------------------------------------------
//
// check_update is cheap by default (the cached verdict; force = "Check
// now"); download_update / apply_update are Windows-only in substance
// (Linux/macOS are notify-only channels) — the platform gate sits at the
// verb layer so the orchestration below stays cross-platform testable.

// The download_state machine values ("" = none).
const (
	updateStateDownloading = "downloading"
	updateStateDownloaded  = "downloaded"
	updateStateVerified    = "verified"
	updateStateFailed      = "failed"
)

const (
	// updateProgressInterval rate-limits the update_progress broadcast.
	updateProgressInterval = time.Second
	// updateDownloadTimeout bounds one installer download end to end (a
	// stuck transfer must not pin the one-download-at-a-time guard forever).
	updateDownloadTimeout = 30 * time.Minute
	// updateNotSupportedMsg answers download/apply off Windows.
	updateNotSupportedMsg = "update apply is not supported on this platform"
)

// checkUpdate serves the cached UpdateStatus; force runs a synchronous
// fresh check first (never holding mu across the fetch).
func (m *manager) checkUpdate(req api.Request) api.Response {
	if req.Force {
		m.forceUpdateCheck(context.Background())
	}
	return m.updateStatusResponse()
}

// updateStatusResponse snapshots the cached check verdict + the download
// state machine into the update_status reply.
func (m *manager) updateStatusResponse() api.Response {
	m.mu.Lock()
	defer m.mu.Unlock()
	settings, err := m.store.LoadSettings()
	if err != nil {
		return errResp(err.Error())
	}
	st := &api.UpdateStatus{CurrentVersion: m.version, Transport: "disabled"}
	if _, mode := chooseUpdateTransport(settings, m.sess); mode != "" {
		st.Transport = mode
	}
	if res := m.updateResult; res != nil {
		if !res.CheckedAt.IsZero() {
			st.CheckedAt = res.CheckedAt.UTC().Format(time.RFC3339)
		}
		st.Available = res.Available
		st.Stale = res.Stale
		st.LatestVersion = res.LatestVersion
		st.NotesURL = res.NotesURL
		if a := res.Artifact; a != nil {
			st.Artifact = &api.UpdateArtifact{
				OS: a.OS, Arch: a.Arch, Kind: a.Kind,
				Name: a.Name, Size: a.Size, SHA256: a.SHA256,
			}
		}
	}
	st.DownloadState = m.updateDownloadState
	if m.updateDownloadState == updateStateDownloading {
		st.DownloadReceived = m.updateDownloadReceived
		st.DownloadTotal = m.updateDownloadTotal
	}
	return api.Response{Status: api.StatusUpdateStatus, UpdateStatus: st}
}

// downloadUpdate starts the async installer download. Windows-only: the
// other platforms have no downloadable channel.
func (m *manager) downloadUpdate() api.Response {
	if !updateApplySupported {
		return errResp(updateNotSupportedMsg)
	}
	return m.startUpdateDownload()
}

// startUpdateDownload is the platform-independent orchestration behind
// download_update (testable everywhere via the updateDir/updateTransport
// seams). One download at a time: a second call while one runs is
// idempotent and just reports the running state. The reply returns
// immediately; the transfer itself runs in a goroutine that never holds mu.
func (m *manager) startUpdateDownload() api.Response {
	m.mu.Lock()
	if m.updateDownloadState == updateStateDownloading {
		m.mu.Unlock()
		return m.updateStatusResponse()
	}
	res := m.updateResult
	if res == nil || !res.Available || res.Artifact == nil {
		m.mu.Unlock()
		return errResp("no downloadable update; run check_update first")
	}
	settings, err := m.store.LoadSettings()
	if err != nil {
		m.mu.Unlock()
		return errResp(err.Error())
	}
	// The same transport flags govern the download as the check.
	transport, mode := chooseUpdateTransport(settings, m.sess)
	if transport == nil {
		m.mu.Unlock()
		return errResp("update transport is disabled by settings")
	}
	if m.updateTransport != nil {
		transport = m.updateTransport
	}
	dir := m.updateDir
	if dir == "" {
		if dir, err = update.UpdatesDir(m.store.Dir()); err != nil {
			m.mu.Unlock()
			return errResp(err.Error())
		}
	}
	artifact := *res.Artifact
	// The transfer's own context: bounded by the timeout, and cancelled by
	// shutdown so the process never exits over a half-written temp file.
	ctx, cancel := context.WithTimeout(context.Background(), updateDownloadTimeout)
	done := make(chan struct{})
	m.updateDownloadState = updateStateDownloading
	m.updateDownloadReceived, m.updateDownloadTotal = 0, artifact.Size
	m.updateFilePath = ""
	m.updateFileArtifact = nil
	m.updateProgressAt = time.Time{}
	m.updateDownloadCancel, m.updateDownloadDone = cancel, done
	m.mu.Unlock()
	go func() {
		defer close(done)
		defer cancel()
		m.runUpdateDownload(ctx, artifact, dir, transport, mode)
	}()
	return m.updateStatusResponse()
}

// runUpdateDownload is the download goroutine: stream the artifact through
// the counting transport into dir, then settle the state machine and emit
// the final progress event. Fail-soft — a blocked mirror is a normal
// condition for this daemon, so failure is an info log plus state=failed.
func (m *manager) runUpdateDownload(ctx context.Context, artifact update.Artifact, dir string, transport http.RoundTripper, mode string) {
	// Leftovers of a transfer the process died in the middle of: swept
	// here, under the one-download-at-a-time guard, where nothing else
	// writes to the directory.
	if n, err := update.RemoveStaleTemps(dir); err != nil {
		m.logSink("info", "update download: "+err.Error())
	} else if n > 0 {
		m.logSink("info", fmt.Sprintf("update download: removed %d stale temp file(s)", n))
	}
	counting := &countingTransport{inner: transport, note: m.noteUpdateProgress}
	path, err := update.Download(ctx, artifact, dir, counting)
	// Same keep-alive hygiene as the check: a tunnel-dialled idle conn must
	// not outlive the transfer (the session may close right after).
	if tr, ok := transport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
	m.mu.Lock()
	m.updateDownloadReceived, m.updateDownloadTotal = 0, 0
	// A check that landed while the transfer ran may have moved the cache
	// to a different artifact; a file the cache no longer describes is not
	// staged (the client re-downloads and gets the current one instead).
	superseded := false
	if err == nil {
		if cur := m.updateResult; cur == nil || !cur.Available || cur.Artifact == nil || !sameArtifact(*cur.Artifact, artifact) {
			superseded = true
			if rmErr := os.Remove(path); rmErr != nil {
				m.logSink("info", "update download: remove superseded file: "+rmErr.Error())
			}
		}
	}
	if err != nil || superseded {
		m.updateDownloadState = updateStateFailed
		m.updateFilePath = ""
		m.updateFileArtifact = nil
	} else {
		m.updateDownloadState = updateStateDownloaded
		m.updateFilePath = path
		landed := artifact
		m.updateFileArtifact = &landed
	}
	hub := m.hub
	m.mu.Unlock()
	if err != nil || superseded {
		if err != nil {
			m.logSink("info", fmt.Sprintf("update download (%s): %v", mode, err))
		} else {
			m.logSink("info", fmt.Sprintf("update download (%s): %s landed but the cached update changed; discarded", mode, artifact.Name))
		}
		if hub != nil {
			hub.Broadcast(api.Event{Event: api.EventUpdateProgress, State: updateStateFailed})
		}
		return
	}
	m.logSink("info", fmt.Sprintf("update download (%s): %s landed", mode, artifact.Name))
	if hub != nil {
		hub.Broadcast(api.Event{
			Event:            api.EventUpdateProgress,
			DownloadReceived: artifact.Size,
			DownloadTotal:    artifact.Size,
			State:            updateStateDownloaded,
		})
	}
}

// noteUpdateProgress lands one byte-counter sample from the download
// stream: state under mu, plus an update_progress broadcast at most once
// per updateProgressInterval (the final downloaded/failed event comes from
// the goroutine, not from here).
func (m *manager) noteUpdateProgress(received int64) {
	m.mu.Lock()
	m.updateDownloadReceived = received
	total := m.updateDownloadTotal
	emit := time.Since(m.updateProgressAt) >= updateProgressInterval
	if emit {
		m.updateProgressAt = time.Now()
	}
	hub := m.hub
	m.mu.Unlock()
	if emit && hub != nil {
		hub.Broadcast(api.Event{
			Event:            api.EventUpdateProgress,
			DownloadReceived: received,
			DownloadTotal:    total,
			State:            updateStateDownloading,
		})
	}
}

// countingTransport wraps the download transport to observe body bytes as
// they arrive. The count restarts at zero per response — a mirror retry
// restarts the file, and the progress must follow.
type countingTransport struct {
	inner http.RoundTripper
	note  func(received int64)
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.inner.RoundTrip(req)
	if err == nil && resp.Body != nil {
		resp.Body = &countingBody{inner: resp.Body, note: c.note}
	}
	return resp, err
}

type countingBody struct {
	inner io.ReadCloser
	note  func(int64)
	n     int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.inner.Read(p)
	if n > 0 {
		b.n += int64(n)
		b.note(b.n)
	}
	return n, err
}

func (b *countingBody) Close() error { return b.inner.Close() }

// applyUpdate re-verifies the downloaded installer and hands its path back
// for the CLIENT to launch (the launch stays client-side for the unelevated
// relaunch; the daemon never executes the file). Windows-only: the other
// platforms have no downloadable channel.
func (m *manager) applyUpdate() api.Response {
	if !updateApplySupported {
		return errResp(updateNotSupportedMsg)
	}
	return m.verifyDownloadedUpdate()
}

// verifyDownloadedUpdate is the platform-independent half of apply_update:
// re-hash the landed file against the facts it was downloaded against
// right before hand-off, so a file swapped after the download is caught.
// A verify failure deletes the corrupt file and resets the state so the
// client can re-download. The hash runs with mu released, so the state
// writes are compare-and-set: a download_update that slipped in meanwhile
// owns the state machine now and is left alone.
func (m *manager) verifyDownloadedUpdate() api.Response {
	m.mu.Lock()
	path := m.updateFilePath
	state := m.updateDownloadState
	artifact := m.updateFileArtifact
	m.mu.Unlock()
	staged := state == updateStateDownloaded || state == updateStateVerified
	if path == "" || !staged || artifact == nil {
		return errResp("no downloaded update to apply; run download_update first")
	}
	err := update.VerifyFile(path, *artifact)
	m.mu.Lock()
	if m.updateFilePath != path || m.updateDownloadState != state || m.updateFileArtifact != artifact {
		m.mu.Unlock()
		return errResp("update state changed during verification; run apply_update again")
	}
	if err != nil {
		_, rmErr := m.discardStagedUpdateLocked()
		m.mu.Unlock()
		if rmErr != nil {
			m.logSink("info", "update apply: remove corrupt file: "+rmErr.Error())
		}
		return errResp(err.Error())
	}
	m.updateDownloadState = updateStateVerified
	m.mu.Unlock()
	resp := m.updateStatusResponse()
	if resp.UpdateStatus != nil {
		// setup_path travels ONLY on a successful apply reply: the one
		// moment the daemon vouches for the file it just re-hashed.
		resp.UpdateStatus.SetupPath = path
	}
	return resp
}

// stagedUpdateSupersededLocked reports whether a staged (downloaded or
// verified) file is no longer the artifact the cached check result
// describes: a different version landed, or the artifact went away with an
// expired manifest.
func (m *manager) stagedUpdateSupersededLocked() bool {
	if m.updateFilePath == "" || m.updateFileArtifact == nil {
		return false
	}
	if m.updateDownloadState != updateStateDownloaded && m.updateDownloadState != updateStateVerified {
		return false
	}
	res := m.updateResult
	return res == nil || !res.Available || res.Artifact == nil || !sameArtifact(*res.Artifact, *m.updateFileArtifact)
}

// discardStagedUpdateLocked deletes the staged file and resets the state
// machine to none. The remove happens under mu on purpose: no download can
// start (and land a new file at the same path) between the decision and
// the delete. Returns the path and the remove error for the caller's log.
func (m *manager) discardStagedUpdateLocked() (string, error) {
	path := m.updateFilePath
	m.updateDownloadState = ""
	m.updateFilePath = ""
	m.updateFileArtifact = nil
	return path, os.Remove(path)
}

// sameArtifact reports whether two manifest artifacts describe the same
// file (mirror URLs aside).
func sameArtifact(a, b update.Artifact) bool {
	return a.OS == b.OS && a.Arch == b.Arch && a.Kind == b.Kind &&
		a.Name == b.Name && a.Size == b.Size && strings.EqualFold(a.SHA256, b.SHA256)
}
