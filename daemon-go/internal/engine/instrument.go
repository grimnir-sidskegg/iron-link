// In-process instrumentation (GO_DAEMON_PLAN.md §6) — everything the deleted
// Clash-API controller used to provide, done inside the daemon process:
//
//   - traffic: a ConnectionTracker appended to the box router wraps every
//     routed connection with sing's counter conns (read = upload, write =
//     download — the same orientation as clash trafficontrol);
//   - logs: a PlatformLogWriter fans sing-box log lines into a caller sink
//     (the daemon turns them into Log events; the hub drops under load);
//   - latency: an EPHEMERAL in-process xray instance per probed node dials a
//     204 endpoint via core.Dial — no Observatory, no /debug/vars port, and
//     ONE mechanism for every node kind (xray dials everything we model).
//
// This is also the seam G5 instruments for per-stage failure attribution.

package engine

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	boxlog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	xcore "github.com/xtls/xray-core/core"

	"ironlink/daemon/internal/proxy"
)

// TrafficCounter accumulates the session's total routed bytes. It implements
// adapter.ConnectionTracker; Session.Start appends it to the box router, so
// EVERY routed connection (native, bridge, direct) is counted in-process.
//
// It also keeps a PER-APP aggregate, keyed by the originating local process's
// executable path: with find_process enabled in the route block (route.go) the
// router resolves metadata.ProcessInfo before this tracker fires, so every
// connection carries its owning app. Connections with no resolved process (or
// an empty path) accrue to the unattributedKey bucket. Each app's bytes are
// SPLIT BY ROUTE OUTCOME (direct / proxy / blocked / dpi — see routeOutcome),
// classified once from the matched outbound's tag.
type TrafficCounter struct {
	up   atomic.Int64
	down atomic.Int64

	mu   sync.Mutex
	apps map[string]*appBytes
}

// routeOutcomes are the buckets one app's bytes split into, by the matched
// outbound's tag. outcomeDPI is RESERVED for the future byedpi DPI-bypass
// outbound — no such outbound exists yet, so that bucket stays 0 for now.
const (
	outcomeDirect  = "direct"
	outcomeProxy   = "proxy"
	outcomeBlocked = "blocked"
	outcomeDPI     = "dpi"
)

// routeOutcome maps a matched outbound's tag to the per-app outcome bucket: the
// session's "direct"/"block" sinks and the reserved DPI outbound classify by
// tag; the selector ("proxy") and every node's own tag are proxied traffic.
func routeOutcome(tag string) string {
	switch tag {
	case "direct":
		return outcomeDirect
	case "block":
		return outcomeBlocked
	case "dpi", "byedpi":
		return outcomeDPI
	default: // SelectorTag ("proxy") + any node tag
		return outcomeProxy
	}
}

// appBytes is one app's cumulative routed bytes, split per route outcome
// (keyed by the routeOutcome constants).
type appBytes struct {
	byOutcome map[string]*routeBytes
}

// routeBytes is one (app, outcome) pair's cumulative up/down counters.
type routeBytes struct {
	up   atomic.Int64
	down atomic.Int64
}

// RouteBytes is a snapshot of one (app, outcome) pair's cumulative bytes.
type RouteBytes struct {
	Up   int64
	Down int64
}

// AppBytes is a snapshot of one app's cumulative routed bytes (Path "" = the
// unattributed bucket): the Up/Down totals (summed across outcomes) plus the
// per-outcome split (keyed by the routeOutcome constants).
type AppBytes struct {
	Path    string
	Up      int64
	Down    int64
	ByRoute map[string]RouteBytes
}

// unattributedKey buckets connections with no resolved owning process.
const unattributedKey = ""

// Totals returns the cumulative (upload, download) byte counts.
func (c *TrafficCounter) Totals() (up, down int64) {
	return c.up.Load(), c.down.Load()
}

// AppTotals returns a snapshot of the cumulative per-app byte counts, each
// app's bytes split per route outcome (ByRoute) with the Up/Down totals being
// the sum across outcomes.
func (c *TrafficCounter) AppTotals() []AppBytes {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]AppBytes, 0, len(c.apps))
	for path, a := range c.apps {
		app := AppBytes{Path: path, ByRoute: make(map[string]RouteBytes, len(a.byOutcome))}
		for outcome, rb := range a.byOutcome {
			up, down := rb.up.Load(), rb.down.Load()
			app.Up += up
			app.Down += down
			app.ByRoute[outcome] = RouteBytes{Up: up, Down: down}
		}
		out = append(out, app)
	}
	return out
}

// appFor resolves (creating on first use) the per-app counter for this
// connection's owning process; the unattributed bucket when none is known.
func (c *TrafficCounter) appFor(metadata adapter.InboundContext) *appBytes {
	key := unattributedKey
	if metadata.ProcessInfo != nil && metadata.ProcessInfo.ProcessPath != "" {
		key = metadata.ProcessInfo.ProcessPath
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.apps == nil {
		c.apps = make(map[string]*appBytes)
	}
	a := c.apps[key]
	if a == nil {
		a = &appBytes{byOutcome: make(map[string]*routeBytes)}
		c.apps[key] = a
	}
	return a
}

// counterFor resolves the (app, outcome) byte counter for this connection: the
// owning app's bucket and, within it, the bucket for the route outcome.
// Created on first use, both under the same lock as appFor.
func (c *TrafficCounter) counterFor(metadata adapter.InboundContext, outcome string) *routeBytes {
	a := c.appFor(metadata)
	c.mu.Lock()
	defer c.mu.Unlock()
	rb := a.byOutcome[outcome]
	if rb == nil {
		rb = &routeBytes{}
		a.byOutcome[outcome] = rb
	}
	return rb
}

// outcomeOf classifies the matched outbound's route outcome (proxy when the
// router resolved none — the selector is the route final).
func outcomeOf(matchOutbound adapter.Outbound) string {
	if matchOutbound == nil {
		return outcomeProxy
	}
	return routeOutcome(matchOutbound.Tag())
}

// RoutedConnection wraps the inbound-side conn with counters: bytes READ from
// it flow toward the outbound (upload), bytes WRITTEN to it come back
// (download). Each byte is counted into BOTH the session total and the owning
// app's per-outcome bucket (app + route outcome resolved once per connection).
func (c *TrafficCounter) RoutedConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) net.Conn {
	rb := c.counterFor(metadata, outcomeOf(matchOutbound))
	return bufio.NewCounterConn(conn,
		[]N.CountFunc{func(n int64) { c.up.Add(n); rb.up.Add(n) }},
		[]N.CountFunc{func(n int64) { c.down.Add(n); rb.down.Add(n) }})
}

// RoutedPacketConnection is RoutedConnection for UDP.
func (c *TrafficCounter) RoutedPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) N.PacketConn {
	rb := c.counterFor(metadata, outcomeOf(matchOutbound))
	return bufio.NewCounterPacketConn(conn,
		[]N.CountFunc{func(n int64) { c.up.Add(n); rb.up.Add(n) }},
		[]N.CountFunc{func(n int64) { c.down.Add(n); rb.down.Add(n) }})
}

// LogSink receives one sing-box log line. It is called on the logging path —
// it must be cheap and MUST NOT block (the daemon's sink hands off to the
// event hub, which drops when subscribers lag).
type LogSink func(level, message string)

// sinkWriter adapts a LogSink to sing-box's PlatformWriter hook.
type sinkWriter struct {
	sink LogSink
}

func (w sinkWriter) DisableColors() bool { return true }

func (w sinkWriter) WriteMessage(level boxlog.Level, message string) {
	if isProcessSearchLine(message) {
		return
	}
	w.sink(boxlog.FormatLevel(level), message)
}

// processSearchMarkers are the per-connection lines sing-box's router emits at
// INFO level once find_process is on (route/route.go) — one per routed
// connection. Per-app traffic accounting needs find_process always-on, but
// these would flood the GUI log feed, so they are dropped from the sink. The
// formatted message carries a level + connection-id prefix, hence a substring
// (not prefix) match on the stable router wording.
var processSearchMarkers = []string{
	"found process path: ",
	"found package name: ",
	"found user: ",
	"found user id: ",
	"failed to search process: ",
}

func isProcessSearchLine(message string) bool {
	for _, m := range processSearchMarkers {
		if strings.Contains(message, m) {
			return true
		}
	}
	return false
}

// probeTimeout bounds one whole probe (warm-up tries + measure). It is the
// FALLBACK only: probeThroughXray honours a caller's shorter ctx deadline but
// never extends past this.
//
// Sized for SLOW, FLAKY transports: an xhttp node with xmux / stream-one
// (scStreamUpServerSecs, xPaddingBytes) cold-starts in ~8–16s and the first
// attempt intermittently drops mid-handshake — so the budget must fit two
// warm-up tries plus the measure. tcp/grpc nodes connect in tens of ms and
// finish long before this matters.
const probeTimeout = 45 * time.Second

// warmupTries is how many times the warm-up dial is attempted before giving
// up: xhttp/xmux cold start is flaky and usually succeeds on a retry; tcp/grpc
// pass on the first try and never reach the second.
const warmupTries = 2

// ProbeURL is the latency probe endpoint — the classic generate_204 returns
// an empty 204, so the round trip measures connection latency without a body
// (same endpoint the Rust Observatory used).
const ProbeURL = "https://www.gstatic.com/generate_204"

// ProbeLatency measures one node's latency: an ephemeral in-process xray
// instance compiled for the node, dialed via core.Dial to url, timed to the
// response header. stubSuffix must be unique among concurrent probes (it
// names the instance's abstract stub socket).
func ProbeLatency(ctx context.Context, p proxy.Profile, stubSuffix, url string) (time.Duration, error) {
	cfg, err := CompileXrayClient(p, "@il-probe-"+stubSuffix)
	if err != nil {
		return 0, fmt.Errorf("compile probe config: %w", err)
	}
	xinst, err := BuildXray(cfg)
	if err != nil {
		return 0, fmt.Errorf("build probe instance: %w", err)
	}
	defer xinst.Close()
	return probeThroughXray(ctx, xinst, url)
}

// DownloadThroughXray fetches url through an ephemeral xray for v and returns
// the bytes read + elapsed — a THROUGHPUT probe that isolates xray's own data
// path for a node (no sing-box, no bridge wrapping, no TUN). Used to tell a
// broken/slow EXIT SERVER apart from a broken in-process bridge: run it on a
// network with no TUN capturing the upstream, and the number is xray↔server raw.
func DownloadThroughXray(ctx context.Context, p proxy.Profile, stubSuffix, url string) (int64, time.Duration, error) {
	cfg, err := CompileXrayClient(p, "@il-dl-"+stubSuffix)
	if err != nil {
		return 0, 0, fmt.Errorf("compile config: %w", err)
	}
	xinst, err := BuildXray(cfg)
	if err != nil {
		return 0, 0, fmt.Errorf("build instance: %w", err)
	}
	defer xinst.Close()

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	// Dispatch through the engine-agnostic seam rather than an xray-specific
	// outbound: the ephemeral xinst hosts the node under singleNodeTag (or, for a
	// freedom stub, one default outbound), which either the rule or the default
	// reaches. xinst's lifetime stays with the defer above; the backend wrapper
	// owns no extra resources, so it is not Closed here.
	ob := &backendOutbound{typ: OutboundType, tag: "dl", nodeID: singleNodeTag, be: newXrayBackend(xinst)}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return ob.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(addr))
		},
	}}
	defer client.CloseIdleConnections()

	// Warm up the tunnel (xhttp/xmux cold start) before timing throughput.
	for try := 0; try < warmupTries; try++ {
		if err := probeGet(ctx, client, ProbeURL); err == nil {
			break
		} else if ctx.Err() != nil {
			return 0, 0, fmt.Errorf("warm-up: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, time.Since(start), err
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	return n, time.Since(start), nil
}

// probeThroughXray measures one node's ESTABLISHED-tunnel latency through the
// xray instance (the bridge path: core.Dial, no sockets between us and xray).
//
// Two requests, warm-up then measure: the FIRST request establishes the
// tunnel — for an xhttp/xmux node this cold-start runs the long, variable
// stream-up/xmux setup (seconds, sometimes the whole timeout); the SECOND
// request reuses that established tunnel (keep-alive is ON, so it does not
// re-dial) and ITS round trip is the reported latency. This measures how the
// node behaves IN USE, not the one-shot cold start that intermittently
// mislabelled working xhttp nodes unreachable. For tcp/grpc nodes the warm-up
// is sub-second and this is simply a cleaner established-latency reading.
func probeThroughXray(ctx context.Context, xinst *xcore.Instance, url string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	// Dispatch through the engine-agnostic seam (see DownloadThroughXray); xinst
	// is owned by the caller, so the backend wrapper is not Closed here.
	ob := &backendOutbound{typ: OutboundType, tag: "probe", nodeID: singleNodeTag, be: newXrayBackend(xinst)}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return ob.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(addr))
			},
			// Keep-alive ON: the warm-up's tunnel is reused by the measure
			// request, so what we time is the established round trip.
		},
	}
	defer client.CloseIdleConnections()

	// Warm-up: open the tunnel (xhttp/xmux cold start is slow AND flaky — the
	// first attempt drops mid-handshake a fraction of the time, so retry).
	var warmErr error
	for try := 0; try < warmupTries; try++ {
		if warmErr = probeGet(ctx, client, url); warmErr == nil {
			break
		}
		if ctx.Err() != nil {
			break // deadline hit — a retry can't help
		}
	}
	if warmErr != nil {
		return 0, fmt.Errorf("warm-up: %w", warmErr)
	}
	// Measure: an established round trip over the reused tunnel.
	start := time.Now()
	if err := probeGet(ctx, client, url); err != nil {
		return 0, fmt.Errorf("measure: %w", err)
	}
	return time.Since(start), nil
}

// probeGet runs one GET and DRAINS the body so the connection returns to the
// keep-alive pool for reuse (an undrained body is not reused). Any HTTP status
// counts — reachability, not correctness.
func probeGet(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}
