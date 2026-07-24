// Profile → native core config compilation (GO_DAEMON_PLAN.md §5): the app
// VLESS profile becomes (a) an xray client config dialing the REAL node and
// (b) the proven sing-box config whose xray-reality outbound bridges to it
// in-memory. This REPLACES the Rust cores/xray.rs schema mirror — the old
// loopback-era sections (socks/http/metrics inbounds, api/stats, fakedns,
// level-8 policy) are gone; the embedded config is minimal and is proven by
// the runtime gates instead of byte-compat with the old generator.

package engine

import (
	"encoding/json"
	"fmt"
	"os"

	"ironlink/daemon/internal/proxy"
)

// xrayLogLevel is the embedded xray instance's log level. Default "warning"
// (quiet); set IRON_LINK_XRAY_LOGLEVEL=debug to make xray log every dial — used
// to diagnose the data path (e.g. whether xray reaches the node under TUN).
func xrayLogLevel() string {
	if lvl := os.Getenv("IRON_LINK_XRAY_LOGLEVEL"); lvl != "" {
		return lvl
	}
	return "warning"
}

// CompileXrayClient builds the xray client config for one node: the node's xray
// outbound tagged "proxy" (from Profile.XrayOutbound), plus a marked direct
// freedom outbound for xray's own DNS. It errors when xray cannot dial the
// node's protocol (e.g. hysteria2/tuic — sing-box-only). stubListen is the
// abstract-UDS name for the mandatory no-op socks inbound (xray refuses to
// start with zero inbounds) — give each live config a distinct name so two
// instances can coexist.
//
// Two own-traffic rules keep the node reachable under the auto_redirect TUN,
// injected here into the protocol's stream settings:
//   - every outbound socket carries sockopt.mark = AutoRedirectOutputMark, the
//     own-output exempt fwmark the TUN nftables skips (Linux; ignored elsewhere
//     — the darwin/windows exemption is part of the pending G0 cross-OS gate);
//   - a domain node address resolves through xray's OWN dns (sockopt
//     domainStrategy UseIP), whose queries route to the marked "direct"
//     outbound — never through the OS resolver, whose unmarked sockets the TUN
//     would re-capture (the strict_route deadlock).
func CompileXrayClient(p proxy.Profile, stubListen string) ([]byte, error) {
	ob, ok, err := p.XrayOutbound()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("xray cannot dial %s nodes", p.Kind())
	}
	ob["tag"] = "proxy"
	stream, _ := ob["streamSettings"].(map[string]any)
	if stream == nil {
		stream = map[string]any{}
		ob["streamSettings"] = stream
	}
	stream["sockopt"] = map[string]any{
		"mark":           AutoRedirectOutputMark,
		"domainStrategy": "UseIP",
	}

	cfg := map[string]any{
		"log": map[string]any{"loglevel": xrayLogLevel()},
		"dns": map[string]any{
			"queryStrategy": "UseIPv4",
			"servers":       []any{"1.1.1.1", "8.8.8.8"},
			"tag":           "dns-internal",
		},
		"inbounds": []any{map[string]any{
			"listen":   stubListen,
			"protocol": "socks",
			"settings": map[string]any{"udp": false},
		}},
		"outbounds": []any{
			ob,
			map[string]any{
				"tag":      "direct",
				"protocol": "freedom",
				"settings": map[string]any{"domainStrategy": "UseIP"},
				"streamSettings": map[string]any{
					"sockopt": map[string]any{"mark": AutoRedirectOutputMark},
				},
			},
		},
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules": []any{map[string]any{
				"inboundTag":  []any{"dns-internal"},
				"outboundTag": "direct",
			}},
		},
	}
	return json.Marshal(cfg)
}

// NodeTUNConfigs compiles the live data plane for one xray-routed node: the
// proven sing-box TUN config (auto_redirect structure, final → xray-reality)
// + the real xray client config for v. Starting the pair hijacks host traffic
// into the TUN until the Session closes (root required).
func NodeTUNConfigs(p proxy.Profile, tunName string) (singBox, xray []byte, err error) {
	xray, err = CompileXrayClient(p, "@il-xray-node-tun")
	if err != nil {
		return nil, nil, fmt.Errorf("compile xray client: %w", err)
	}
	return singBoxTUNConfig(tunName), xray, nil
}

// NodeSocksConfigs compiles the NO-ROOT variant for one xray-routed node: a
// sing-box SOCKS inbound on host:port routes through the in-memory bridge to
// the real node. Same data plane as NodeTUNConfigs minus the TUN — the
// anywhere-runnable proof that the compiled node config carries real traffic.
func NodeSocksConfigs(p proxy.Profile, host string, port int) (singBox, xray []byte, err error) {
	xray, err = CompileXrayClient(p, "@il-xray-node-socks")
	if err != nil {
		return nil, nil, fmt.Errorf("compile xray client: %w", err)
	}
	return singBoxSocksConfig(host, port), xray, nil
}
