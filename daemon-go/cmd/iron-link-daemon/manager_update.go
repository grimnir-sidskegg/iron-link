// The background update check: a jittered daily fetch of the signed update
// manifest, cached in-memory for the client (the wire verbs arrive with a
// later milestone). The transport follows the settings flags and is
// recomputed at every attempt: through the live session's outbound first,
// TUN-exempt direct as the fallback, disabled when both flags are off.
// Fail-soft throughout — a blocked mirror is a normal condition for this
// daemon, so errors are logged and the previous cache is kept.
package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/engine"
	"ironlink/daemon/internal/update"
)

// updateManifestURLs are the production manifest mirrors, tried in order
// (the detached signature sits at "<url>.minisig" beside each). One generic
// high-reputation host today; a dedicated mirror is one added line.
var updateManifestURLs = []string{
	"https://raw.githubusercontent.com/grimnir-sidskegg/iron-link/updates/update.json",
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
// the last ATTEMPT. lastAttempt is in-memory on purpose — a daemon restart
// may check early, which is harmless; what must persist is the
// anti-downgrade floor, and that lives in the FileSeqStore.
func (m *manager) updateLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(updateInitialDelay + rand.N(updateInitialDelay)):
	}
	var lastAttempt time.Time
	due := jitterInterval(updateCheckInterval)
	attempt := func() {
		if !lastAttempt.IsZero() && time.Since(lastAttempt) < due {
			return
		}
		if m.tryUpdateCheck(ctx) {
			lastAttempt = time.Now()
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
	m.mu.Lock()
	settings, err := m.store.LoadSettings()
	sess := m.sess
	m.mu.Unlock()
	if err != nil {
		m.logSink("info", "update check: settings: "+err.Error())
		return false
	}
	if !settings.AutoUpdate {
		return false
	}
	transport, mode := m.updateTransport, "test"
	if transport == nil {
		transport, mode = chooseUpdateTransport(settings, sess)
	}
	if transport == nil {
		return false // both transport flags off: checking is disabled
	}
	m.runUpdateCheck(ctx, transport, mode)
	return true
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
	urls := m.updateURLs
	if urls == nil {
		urls = updateManifestURLs
	}
	res, err := update.Check(ctx, update.Config{
		CurrentVersion: m.version,
		ManifestURLs:   urls,
		Transport:      transport,
		SeqStore:       update.NewFileSeqStore(m.store.Dir()),
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
	m.mu.Unlock()
	switch {
	case res.Available:
		m.logSink("info", fmt.Sprintf("update check (%s): %s is available", mode, res.LatestVersion))
	case res.Stale:
		m.logSink("info", fmt.Sprintf("update check (%s): manifest is stale (latest %s)", mode, res.LatestVersion))
	default:
		m.logSink("info", fmt.Sprintf("update check (%s): up to date", mode))
	}
}
