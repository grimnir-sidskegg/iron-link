// The universal latency probe's sing-box half. ProbeLatency (instrument.go)
// dials through an ephemeral xray and so cannot measure the protocols xray
// cannot dial (anytls + the QUIC family hysteria2/tuic/hysteria). This probes
// those through an ephemeral in-process SING-BOX outbound instead — sing-box
// dials every protocol we model. The manager routes each node to the matching
// probe by its selected core, so xray is used only for the xhttp resolves it is
// the specialist for, exactly as the live data path does.

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"ironlink/daemon/internal/proxy"
)

// ProbeLatencySingBox measures one native node's latency through an EPHEMERAL
// in-process sing-box outbound. It builds JUST the node's outbound (no inbound,
// no TUN → constructing needs no root), starts the box, and times an
// established round trip dialled straight through the constructed outbound (no
// SOCKS hop, like ProbeLatency's core.Dial through xray).
//
// When tunExempt is set, the outbound carries routing_mark =
// AutoRedirectOutputMark so that, under an active auto_redirect TUN, the probe's
// own dials are exempt and measure the REAL path to the node rather than a
// re-captured loop back through the live session (the own-output fwmark
// CompileXrayClient stamps on the xray probe). The mark is set ONLY then: SO_MARK
// needs CAP_NET_ADMIN, so stamping it without an active TUN (no root) would fault
// every dial with EPERM — and with no TUN capturing traffic there is nothing to
// exempt. The caller passes tunExempt = "a live TUN session is up".
//
// The mark is also gated to Linux: auto_redirect (the fwmark mechanism it feeds)
// is a Linux-only sing-box feature, and sing-box v1.13 REJECTS `routing_mark` at
// outbound-init off-Linux ("'routing_mark' is only supported on Linux") rather
// than ignoring it — so off-Linux there is no fwmark to exempt from and setting
// it would break every probe (mirrors autoRedirectField's Linux gate in config.go).
//
// Off Linux the exemption is route.auto_detect_interface instead (set only when
// tunExempt): it binds the outbound's underlay dial to the physical default
// interface, escaping the live TUN the cross-OS way — the same bind sing-box
// applies to its own outbounds and the markControl probes use (dialmark_other.go),
// independent of the session's route_exclude_address.
func ProbeLatencySingBox(ctx context.Context, p proxy.Profile, url string, tunExempt bool) (time.Duration, error) {
	ob, err := p.SingBoxOutbound("probe")
	if err != nil {
		return 0, fmt.Errorf("compile probe outbound: %w", err)
	}
	if tunExempt && runtime.GOOS == "linux" {
		ob["routing_mark"] = AutoRedirectOutputMark
	}

	cfgMap := map[string]any{
		"log":       map[string]any{"disabled": true},
		"outbounds": []any{ob},
	}
	// Off Linux there is no routing_mark, so under a live TUN enable
	// auto_detect_interface: it binds the outbound's OWN dials (the underlay to
	// the node server) to the physical default interface — excluding the live TUN
	// exactly as sing-box keeps its own outbounds from looping. This is the
	// cross-OS analogue of the markControl interface-bind the doctor probes use,
	// and it escapes the TUN WITHOUT relying on the live session's
	// route_exclude_address, so even a non-member node probes the real path.
	if tunExempt && runtime.GOOS != "linux" {
		cfgMap["route"] = map[string]any{"auto_detect_interface": true}
	}

	cfg, err := json.Marshal(cfgMap)
	if err != nil {
		return 0, err
	}
	b, err := BuildBox(cfg, nil, nil)
	if err != nil {
		return 0, fmt.Errorf("build probe box: %w", err)
	}
	defer b.Close()
	if err := b.Start(); err != nil {
		return 0, fmt.Errorf("start probe box: %w", err)
	}
	outbound, ok := b.Outbound().Outbound("probe")
	if !ok {
		return 0, fmt.Errorf("probe outbound %q not constructed", "probe")
	}
	return probeThroughSingBox(ctx, outbound, url)
}

// probeThroughSingBox times an established-tunnel round trip to url through a
// constructed sing-box outbound (warm-up then measure, like probeThroughXray):
// the first GET opens the tunnel, the second reuses the keep-alive connection
// and ITS round trip is the reported latency.
func probeThroughSingBox(ctx context.Context, ob adapter.Outbound, url string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return ob.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(addr))
		},
	}}
	defer client.CloseIdleConnections()

	var warmErr error
	for try := 0; try < warmupTries; try++ {
		if warmErr = probeGet(ctx, client, url); warmErr == nil {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	if warmErr != nil {
		return 0, fmt.Errorf("warm-up: %w", warmErr)
	}
	start := time.Now()
	if err := probeGet(ctx, client, url); err != nil {
		return 0, fmt.Errorf("measure: %w", err)
	}
	return time.Since(start), nil
}
