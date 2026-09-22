// Staged node diagnosis (GO_DAEMON_PLAN.md §7 G5) — typed per-stage failure
// attribution built on the §6 in-process dial path. A single latency probe
// answers "does it work, how fast"; a diagnosis answers "WHERE does it break":
//
//	tcp   — a raw (fwmark-exempt) TCP connect to the node's server:port.
//	        Fails → the endpoint is down / the port is blocked / the address is
//	        wrong. Reality nodes usually PASS this (they front a real host on
//	        443), so a tcp pass with a proxy failure is the common "your config
//	        is wrong" signal.
//	proxy — the full round trip THROUGH the tunnel: the VLESS + Reality/TLS +
//	        transport handshake completes and the HTTP first byte returns. tcp
//	        ok but this fails → wrong pbk/sid/sni, rejected fingerprint,
//	        transport mismatch, server rejects auth, or the endpoint is not
//	        actually a proxy.
//
// Why two stages and not the idealized tcp→handshake→first-byte three: xray's
// `core.Dial` is LAZY — the VLESS/Reality handshake to the server does not run
// at dial time, it runs on the first write. The handshake and the first byte
// therefore happen together inside one round trip and are not separable from
// OUTSIDE xray; folding them into one honest `proxy` stage beats faking a
// boundary that doesn't exist. This is the seam G5's variant probing
// (SNI × transport × fp × …) generalizes next.
//
// Stages run in order and stop at the first failure (downstream can't run).

package engine

import (
	"context"
	"net"
	"strconv"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/proxy"
)

// Stage names one diagnosis step.
type Stage string

const (
	StageConfig Stage = "config"
	StageTCP    Stage = "tcp"
	StageProxy  Stage = "proxy"
)

// tcpStageTimeout bounds the raw TCP connect so a dead endpoint does not eat
// the whole diagnosis budget.
const tcpStageTimeout = 6 * time.Second

// StageResult is one stage's outcome.
type StageResult struct {
	Stage      Stage
	OK         bool
	DurationMs int64
	// Err is the failure reason ("" on success).
	Err string
}

// Diagnosis is the ordered stage results plus the verdict. OK is true only
// when every stage passed; FailedStage names the first failure (empty when
// OK).
type Diagnosis struct {
	Stages      []StageResult
	OK          bool
	FailedStage Stage
}

// DiagnoseNode runs the staged diagnosis for one node through core — the core
// the live session would dial it with (sing-box for native protocols, xray for
// the xhttp resolves xray is the specialist for). stubSuffix names the ephemeral
// xray instance's abstract stub socket (must be unique among concurrent
// diagnoses); url is the probe target (ProbeURL); tunExempt fwmark-exempts the
// sing-box proxy stage under a live TUN (see ProbeLatencySingBox).
//
// The tcp stage applies only to TCP-fronted nodes: a QUIC node (ServerNetwork
// "udp") has no TCP socket to connect, so it runs the single proxy stage — the
// QUIC tunnel round trip — which is its reachability-and-works verdict.
func DiagnoseNode(ctx context.Context, p proxy.Profile, core api.CoreType, stubSuffix, url string, tunExempt bool) Diagnosis {
	var d Diagnosis

	// config first: a structurally invalid node (bad cipher/key, missing Reality
	// keys) can never dial, so name THAT instead of a downstream network failure.
	cfg := stageConfig(p, core)
	d.Stages = append(d.Stages, cfg)
	if !cfg.OK {
		d.FailedStage = StageConfig
		return d
	}

	if p.ServerNetwork() == "tcp" {
		tcp := stageTCP(ctx, p)
		d.Stages = append(d.Stages, tcp)
		if !tcp.OK {
			d.FailedStage = StageTCP
			return d
		}
	}

	px := stageProxy(ctx, p, core, stubSuffix, url, tunExempt)
	d.Stages = append(d.Stages, px)
	if !px.OK {
		d.FailedStage = StageProxy
		return d
	}
	d.OK = true
	return d
}

// stageConfig validates the node's config by constructing its outbound in the
// core without dialing (ValidateNodeConfig). A failure here is a broken node
// definition — the wrong Shadowsocks key length, an unknown cipher, a Reality
// block missing pbk/sid — reported precisely instead of as a network symptom.
func stageConfig(p proxy.Profile, core api.CoreType) StageResult {
	res := StageResult{Stage: StageConfig}
	start := time.Now()
	err := ValidateNodeConfig(p, core)
	res.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Err = err.Error()
		return res
	}
	res.OK = true
	return res
}

// stageTCP dials the node's bare server:port TUN-exempt (probeDialer: own-output
// fwmark on Linux, interface-bind off Linux), so it measures endpoint
// reachability even under an active TUN. A domain address resolves through the
// exempt resolver too, so the DNS that picks the IP is not captured by the
// tunnel's hijack-dns either.
func stageTCP(ctx context.Context, p proxy.Profile) StageResult {
	res := StageResult{Stage: StageTCP}
	addr := net.JoinHostPort(p.ServerAddress(), strconv.Itoa(int(p.ServerPort())))
	dialer := probeDialer(tcpStageTimeout)

	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	res.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Err = err.Error()
		return res
	}
	conn.Close()
	res.OK = true
	return res
}

// stageProxy runs the full round trip through the node's core — the protocol
// handshake and the HTTP first byte (which, given a lazy dial, happen together).
// Success = the proxy genuinely carries traffic. Sing-box-routed nodes go
// through an ephemeral sing-box; xray-routed nodes through an ephemeral xray. A
// failure means the tunnel could not open.
func stageProxy(ctx context.Context, p proxy.Profile, core api.CoreType, stubSuffix, url string, tunExempt bool) StageResult {
	res := StageResult{Stage: StageProxy}

	var (
		d   time.Duration
		err error
	)
	if core == api.CoreXray {
		d, err = ProbeLatency(ctx, p, stubSuffix, url)
	} else {
		d, err = ProbeLatencySingBox(ctx, p, url, tunExempt)
	}
	if err != nil {
		res.Err = err.Error()
		return res
	}
	res.DurationMs = d.Milliseconds()
	res.OK = true
	return res
}
