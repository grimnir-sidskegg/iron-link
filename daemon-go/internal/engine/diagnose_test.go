// Staged-diagnosis verification (no root). The stages are exercised against
// reachable / unreachable endpoints so the ATTRIBUTION is what's asserted —
// which stage owns the failure — not raw latencies.
package engine

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	xcore "github.com/xtls/xray-core/core"

	"ironlink/daemon/internal/api"
	ilproxy "ironlink/daemon/internal/proxy"
)

// TestDiagnoseTCPStageFailsForDeadEndpoint: a closed local port (connection
// refused, deterministic) fails the first stage, and the proxy stage does not
// run.
func TestDiagnoseTCPStageFailsForDeadEndpoint(t *testing.T) {
	closedPort := freePort(t) // freePort closes its listener → connect is refused

	v := testVless(testReality(),
		ilproxy.Transport{Kind: ilproxy.TransportXhttp, Xhttp: &ilproxy.XhttpParams{}}, "")
	v.Address = "127.0.0.1"
	v.Port = uint16(closedPort)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d := DiagnoseNode(ctx, v, api.CoreXray, "dead", ProbeURL, false)

	if d.OK || d.FailedStage != StageTCP {
		t.Fatalf("a refused endpoint must fail at tcp: %+v", d)
	}
	// config passes (the node is well-formed), then the tcp stage runs and fails.
	if len(d.Stages) != 2 || d.Stages[0].Stage != StageConfig || !d.Stages[0].OK {
		t.Fatalf("config must pass before the tcp failure: %+v", d.Stages)
	}
	if d.Stages[1].Stage != StageTCP || d.Stages[1].OK || d.Stages[1].Err == "" {
		t.Fatalf("the tcp stage must run and fail with an error: %+v", d.Stages)
	}
}

// TestDiagnoseProxyStageFailsWhenTCPOkButNotAProxy: point the node at a plain
// TCP server (reachable — tcp passes) that is NOT a VLESS/Reality server, so
// the tunnel cannot open → the proxy stage owns the failure, response never
// runs.
func TestDiagnoseProxyStageFailsWhenTCPOkButNotAProxy(t *testing.T) {
	// A bare TCP listener that accepts then drops — TCP connects, the VLESS
	// handshake gets nothing back.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	v := testVless(testReality(),
		ilproxy.Transport{Kind: ilproxy.TransportTCP}, "")
	v.Address = "127.0.0.1"
	v.Port = uint16(port)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	d := DiagnoseNode(ctx, v, api.CoreXray, "noproxy", ProbeURL, false)

	if d.OK {
		t.Fatalf("a non-proxy endpoint must not pass: %+v", d)
	}
	if d.FailedStage != StageProxy {
		t.Fatalf("tcp-ok-but-not-a-proxy must fail at proxy, got %q: %+v", d.FailedStage, d.Stages)
	}
	// config ok, tcp ok, proxy fail.
	if len(d.Stages) != 3 || !d.Stages[0].OK || !d.Stages[1].OK || d.Stages[2].OK {
		t.Fatalf("config ok, tcp ok, proxy fail: %+v", d.Stages)
	}
}

// TestDiagnoseAllStagesOKThroughSelfDescribingNode is the no-root proxy-stage
// SUCCESS path: a tiny in-process VLESS server (xray) IS the node, so the
// ephemeral diagnosis instance completes the real VLESS handshake and the
// tunnel carries the HTTP round trip to a local target — all three stages
// pass. This pins the success staging without a remote node (the real-node
// path is the env-gated runtime diagnosis).
func TestDiagnoseAllStagesOKThroughSelfDescribingNode(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	node, vlessPort := startLocalVlessServer(t)
	defer node.Close()

	v := &ilproxy.VlessConfig{
		ServerName: "local-node", UUID: localVlessUUID, Address: "127.0.0.1", Port: uint16(vlessPort),
		Encryption: "none",
		Security:   ilproxy.Security{Kind: ilproxy.SecurityNone},
		Transport:  ilproxy.Transport{Kind: ilproxy.TransportTCP},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := DiagnoseNode(ctx, v, api.CoreXray, "ok", target.URL, false)

	if !d.OK || d.FailedStage != "" {
		t.Fatalf("all stages should pass through a real local node: %+v", d)
	}
	if len(d.Stages) != 3 { // config + tcp + proxy
		t.Fatalf("want 3 stages, got %+v", d.Stages)
	}
	for _, s := range d.Stages {
		if !s.OK {
			t.Errorf("stage %s should be OK: %+v", s.Stage, s)
		}
	}
}

const localVlessUUID = "88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a"

// startLocalVlessServer builds an xray instance with a plain VLESS inbound
// (no TLS) on a free port and a freedom outbound — a real proxy the diagnosis
// can complete a handshake against. Returns the instance and its port.
func startLocalVlessServer(t *testing.T) (*xcore.Instance, int) {
	t.Helper()
	port := freePort(t)
	inst, err := BuildXray([]byte(`{
      "log": {"loglevel":"warning"},
      "inbounds": [{
        "listen": "127.0.0.1", "port": ` + strconv.Itoa(port) + `,
        "protocol": "vless",
        "settings": {"clients": [{"id": "` + localVlessUUID + `"}], "decryption": "none"}
      }],
      "outbounds": [{"protocol": "freedom", "tag": "direct"}]
    }`))
	if err != nil {
		t.Fatalf("build local vless server: %v", err)
	}
	return inst, port
}

// TestDiagnoseSkipsTCPStageForQUIC: a QUIC node (ServerNetwork "udp") has no TCP
// front, so its diagnosis runs ONLY the proxy stage — the QUIC tunnel round trip
// — because TCP-connecting a UDP port would falsely report it unreachable. The
// node is dead, so the proxy stage fails; what's asserted is the STAGE STRUCTURE:
// no tcp stage ran. The native proxy stage routes through sing-box (its dial
// plumbing is pinned by TestSingBoxProbeMechanism).
func TestDiagnoseSkipsTCPStageForQUIC(t *testing.T) {
	h := &ilproxy.Hysteria2Config{
		ServerName: "hy2", Address: "203.0.113.40", Port: 443, Password: "pw", SNI: "cdn.example.com",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	d := DiagnoseNode(ctx, h, api.CoreSingBox, "quic", ProbeURL, false)

	// The point: a UDP node never runs the tcp stage (TCP-connecting a UDP port
	// would lie). It fails downstream — config (no with_quic) or proxy (dead
	// node) — but never at tcp.
	for _, s := range d.Stages {
		if s.Stage == StageTCP {
			t.Fatalf("a QUIC node must NOT run the tcp stage: %+v", d.Stages)
		}
	}
	if d.OK {
		t.Fatalf("the dead QUIC node must not pass: %+v", d)
	}
}

// TestDiagnoseConfigStageCatchesBadSSKey: a Shadowsocks 2022 node whose PSK is
// the wrong length never dials — the config stage names the key problem (the
// core's own validation) instead of a vague downstream network failure. This is
// the "is the key the right size / method valid" check.
func TestDiagnoseConfigStageCatchesBadSSKey(t *testing.T) {
	// 2022-blake3-aes-256-gcm requires a 32-byte PSK; hand it 28 bytes.
	badKey := url.QueryEscape(base64.StdEncoding.EncodeToString(make([]byte, 28)))
	link := "ss://2022-blake3-aes-256-gcm:" + badKey + "@203.0.113.50:8388#bad"
	p, err := ilproxy.ParseURL(link)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d := DiagnoseNode(ctx, p, api.CoreSingBox, "badss", ProbeURL, false)

	if d.OK || d.FailedStage != StageConfig {
		t.Fatalf("a wrong-length SS key must fail at config, got %+v", d)
	}
	if len(d.Stages) != 1 || !strings.Contains(d.Stages[0].Err, "key") {
		t.Fatalf("config stage should name the key problem, got %+v", d.Stages)
	}
}
