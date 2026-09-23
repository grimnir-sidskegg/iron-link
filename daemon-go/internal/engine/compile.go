// Profile → native core config compilation: the app VLESS profile becomes
// (a) an xray client config dialing the REAL node and (b) the proven
// sing-box config whose xray-reality outbound bridges to it in-memory. The
// embedded config is minimal — no loopback-era sections (socks/http/metrics
// inbounds, api/stats, fakedns, level-8 policy) — and is proven by the
// runtime gates.

package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"ironlink/daemon/internal/proxy"
)

// singleNodeTag is the xray outbound tag — and the dispatch node id — for the
// single-node client config CompileXrayClient builds. Kept as "proxy", the tag
// the node outbound has always carried, so the produced config is unchanged bar
// the added inboundTag rule. The backend sets ContextWithInbound{Tag:
// singleNodeTag} to reach this node; because the node outbound is also declared
// FIRST (xray's default), a dispatch with no/an-unmatched inbound tag still
// reaches it, keeping every existing single-node caller working.
const singleNodeTag = "proxy"

// xrayClientNode pairs a node's dispatch id (its xray outbound tag and the key
// of its inboundTag->outboundTag rule) with the profile that dials it.
type xrayClientNode struct {
	ID      string
	Profile proxy.Profile
}

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
// node's protocol (e.g. tuic/hysteria/anytls — sing-box-only — or a hysteria2
// node xray cannot verify). stubListen is the abstract-UDS name for the
// mandatory no-op socks inbound (xray refuses to start with zero inbounds) —
// give each live config a distinct name so two instances can coexist.
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
	return compileXrayClientNodes([]xrayClientNode{{ID: singleNodeTag, Profile: p}}, stubListen)
}

// compileXrayClientNodes is the generalized compiler: it hosts N nodes in one
// xray client config. For each node it emits a uniquely-tagged outbound (tag ==
// node.ID) plus an inboundTag->outboundTag routing rule keyed on that same id,
// so a dispatch carrying ContextWithInbound{Tag: node.ID} egresses through that
// node. The FIRST node's outbound is declared first, making it xray's default
// (a no/unmatched-inbound-tag dispatch still reaches it) — which is why the
// single-node CompileXrayClient path is behaviour-preserving.
//
// The shared scaffolding is unchanged from the single-node config: xray's own
// dns (queryStrategy UseIPv4, servers 1.1.1.1/8.8.8.8) with the dns-internal ->
// direct rule kept FIRST in the rule list, a marked direct freedom outbound, and
// the mandatory stub socks inbound. Each node outbound carries sockopt.mark (the
// own-output fwmark the auto_redirect TUN skips) and domainStrategy UseIP (xray
// resolves node domains through its own dns, never the OS resolver the TUN would
// re-capture).
func compileXrayClientNodes(nodes []xrayClientNode, stubListen string) ([]byte, error) {
	if len(nodes) == 0 {
		return nil, errors.New("xray client config needs at least one node")
	}
	outbounds := make([]any, 0, len(nodes)+1)
	// dns-internal -> direct stays rules[0] (the own-dns invariant compile_test
	// locks); per-node rules follow. The inbound tags are disjoint, so order
	// among them is immaterial.
	rules := []any{map[string]any{
		"inboundTag":  []any{"dns-internal"},
		"outboundTag": "direct",
	}}
	for _, n := range nodes {
		ob, ok, err := n.Profile.XrayOutbound()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("xray cannot dial %s nodes", n.Profile.Kind())
		}
		ob["tag"] = n.ID
		stream, _ := ob["streamSettings"].(map[string]any)
		if stream == nil {
			stream = map[string]any{}
			ob["streamSettings"] = stream
		}
		stream["sockopt"] = map[string]any{
			"mark":           AutoRedirectOutputMark,
			"domainStrategy": "UseIP",
		}
		outbounds = append(outbounds, ob)
		rules = append(rules, map[string]any{
			"inboundTag":  []any{n.ID},
			"outboundTag": n.ID,
		})
	}
	outbounds = append(outbounds, map[string]any{
		"tag":      "direct",
		"protocol": "freedom",
		"settings": map[string]any{"targetStrategy": "UseIP"}, // freedom.domainStrategy is deprecated since xray 26.7
		"streamSettings": map[string]any{
			"sockopt": map[string]any{"mark": AutoRedirectOutputMark},
		},
	})

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
		"outbounds": outbounds,
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules":          rules,
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
