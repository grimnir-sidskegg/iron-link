package engine

import (
	"bytes"
	"context"
	"sync"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/experimental/deprecated"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"

	xcore "github.com/xtls/xray-core/core"
	xserial "github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all" // register xray's protocols/transports
)

// backendOptions carries a backend outbound's options as declared in the
// sing-box config. NodeID is the backend-side node id the constructed
// backendOutbound dispatches under (the inbound tag the backend routes on
// — see compile.go); empty selects the backend's default outbound, which is the
// single hosted node, so the existing single-node configs keep working unchanged.
type backendOptions struct {
	NodeID string `json:"node_id,omitempty"`
}

// xrayNewMu serializes xray instance construction: core.New rewrites
// process-wide dialer state (transport/internet's system dialer globals)
// without synchronization, so the parallel ephemeral probe cores of a
// test_latency sweep would race on it.
var xrayNewMu sync.Mutex

// BuildXray builds and STARTS an xray instance from a JSON config. The caller
// owns it and must Close it. (Ported from the spike.)
func BuildXray(cfg []byte) (*xcore.Instance, error) {
	c, err := xserial.LoadJSONConfig(bytes.NewReader(cfg))
	if err != nil {
		return nil, err
	}
	xrayNewMu.Lock()
	inst, err := xcore.New(c)
	xrayNewMu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := inst.Start(); err != nil {
		inst.Close() // H1: a failed Start still holds the instance's resources
		return nil, err
	}
	return inst, nil
}

// BuildBox assembles a sing-box instance from singBoxCfg, registering ONE
// outbound constructor per backend (keyed by backend.Type()) so each member
// outbound's Dial/Listen delegates to its backend, resolving the node id from
// the outbound's own options — no single instance is captured, so one backend
// can host many nodes. A non-nil logSink taps the box's log output (§6) via the
// PlatformLogWriter hook — lines still reach the box's own output too.
//
// It does NOT Start the returned box — construction opens no TUN device, so this
// needs no root. The caller Starts it (root / CAP_NET_ADMIN) to open the TUN and
// begin routing. Build the daemon with `-tags "with_gvisor,with_utls,with_clash_api,with_quic"` for the
// gvisor TUN stack used by the configs here (and the QUIC outbounds).
func BuildBox(singBoxCfg []byte, backends []Backend, logSink LogSink) (*box.Box, error) {
	outReg := include.OutboundRegistry()
	for _, be := range backends {
		be := be // capture per iteration, not a shared loop variable
		outbound.Register[backendOptions](outReg, be.Type(),
			func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options backendOptions) (adapter.Outbound, error) {
				return &backendOutbound{typ: be.Type(), tag: tag, nodeID: options.NodeID, be: be}, nil
			})
	}

	ctx := box.Context(
		service.ContextWith(context.Background(), deprecated.NewStderrManager(log.StdLogger())),
		include.InboundRegistry(), outReg, include.EndpointRegistry(),
		include.DNSTransportRegistry(), include.ServiceRegistry(),
		include.CertificateProviderRegistry(),
	)

	options, err := json.UnmarshalExtendedContext[option.Options](ctx, singBoxCfg)
	if err != nil {
		return nil, err
	}
	boxOptions := box.Options{Context: ctx, Options: options}
	// The platform-writer hook needs the with_clash_api build (see
	// logtap_clash.go); without it the sink silently degrades.
	if logSink != nil && logSinkSupported() {
		boxOptions.PlatformLogWriter = sinkWriter{sink: logSink}
	}
	return box.New(boxOptions)
}
