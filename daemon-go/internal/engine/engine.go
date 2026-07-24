package engine

import (
	"bytes"
	"context"

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

// xrayOptions carries the xray-reality outbound's options as declared in the
// sing-box config. Empty for now (the xray instance is built separately and
// injected); per-node params move here in a later step.
type xrayOptions struct{}

// BuildXray builds and STARTS an xray instance from a JSON config. The caller
// owns it and must Close it. (Ported from the spike.)
func BuildXray(cfg []byte) (*xcore.Instance, error) {
	c, err := xserial.LoadJSONConfig(bytes.NewReader(cfg))
	if err != nil {
		return nil, err
	}
	inst, err := xcore.New(c)
	if err != nil {
		return nil, err
	}
	if err := inst.Start(); err != nil {
		return nil, err
	}
	return inst, nil
}

// BuildBox assembles a sing-box instance from singBoxCfg, registering the
// xray-reality outbound so its DialContext dispatches to xinst via core.Dial.
// A non-nil logSink taps the box's log output (§6) via the PlatformLogWriter
// hook — lines still reach the box's own output too.
//
// It does NOT Start the returned box — construction opens no TUN device, so this
// needs no root. The caller Starts it (root / CAP_NET_ADMIN) to open the TUN and
// begin routing. Build the daemon with `-tags "with_gvisor,with_utls,with_clash_api,with_quic"` for the
// gvisor TUN stack used by the configs here (and the QUIC outbounds).
func BuildBox(singBoxCfg []byte, xinst *xcore.Instance, logSink LogSink) (*box.Box, error) {
	outReg := include.OutboundRegistry()
	outbound.Register[xrayOptions](outReg, OutboundType,
		func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options xrayOptions) (adapter.Outbound, error) {
			return &xrayOutbound{tag: tag, inst: xinst}, nil
		})

	ctx := box.Context(
		service.ContextWith(context.Background(), deprecated.NewStderrManager(log.StdLogger())),
		include.InboundRegistry(), outReg, include.EndpointRegistry(),
		include.DNSTransportRegistry(), include.ServiceRegistry(),
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
