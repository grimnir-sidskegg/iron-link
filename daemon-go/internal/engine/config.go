package engine

import (
	"fmt"
	"runtime"
)

// autoRedirectField emits the TUN `auto_redirect` option ONLY on Linux.
// auto_redirect is a Linux-only sing-box feature (nftables/eBPF) and sing-box
// v1.13 REJECTS it at construct time off-Linux ("auto-redirect: invalid
// argument") where v1.12 silently ignored it — so these construct-validation
// configs must gate it just like the production path (plan.go PlanTUNConfigs).
// The trailing comma keeps the surrounding JSON literal valid when omitted.
func autoRedirectField() string {
	if runtime.GOOS == "linux" {
		return `"auto_redirect":true,`
	}
	return ""
}

// AutoRedirectOutputMark is sing-box's own-output exempt fwmark in auto_redirect
// mode (sing-tun's DefaultAutoRedirectOutputMark = 0x2024): packets carrying it
// are NOT re-redirected by the TUN nftables, so they egress normally. sing-box
// tags its OWN outbound connections with it; the in-process xray core does not,
// so we set it on xray's sockets to give them the same bypass.
const AutoRedirectOutputMark = 0x2024

// FreedomValidationConfigs returns a (sing-box, xray) config pair that drives the
// WHOLE TUN data path through the in-memory bridge with a direct `freedom` xray —
// no real node. It uses the proven TUN structure: 172.18.0.1/30 + IPv6,
// auto_route + auto_redirect (Linux nftables), strict_route, stack "mixed",
// route_exclude_address, DoT resolver.
//
// Loop fix (the auto_route trap the spike hit): the base TUN was confirmed working
// via TestRuntimeDirectTun, so the only remaining looper is xray's OWN upstream —
// in auto_redirect mode sing-box exempts its own output by fwmark, so xray's
// freedom outbound carries `sockopt.mark = AutoRedirectOutputMark`. DNS resolves
// via the `direct` DoT server (sing-box-protected), decoupled from xray.
// tunName is the TUN interface name (use a throwaway like "iltun0").
//
// auto_route is ON, so starting this hijacks the host's traffic into the TUN until
// the session is Closed — run only on a throwaway box / with care.
func FreedomValidationConfigs(tunName string) (singBox, xray []byte) {
	xray = []byte(fmt.Sprintf(`{
      "log": {"loglevel":"warning"},
      "inbounds": [{"listen":"@il-xray-stub-tun","protocol":"socks","settings":{"udp":false}}],
      "outbounds": [{"protocol":"freedom","tag":"direct","streamSettings":{"sockopt":{"mark":%d}}}]
    }`, AutoRedirectOutputMark))

	return singBoxTUNConfig(tunName), xray
}

// singBoxTUNConfig is the PROVEN sing-box side of the TUN data plane (the
// structure TestRuntime204 closed the G0 gate with): TUN inbound in
// auto_redirect mode routing everything to the xray-reality outbound. It is
// shared by the freedom validation pair and the real-node compilation
// (compile.go) — the sing-box side does not change between a freedom xray and
// a real node; only the xray config does.
//
// An empty tunName omits interface_name so sing-box auto-assigns one — macOS
// only allows kernel-control utunN device names, so fixed names are
// Linux/Windows-only.
func singBoxTUNConfig(tunName string) []byte {
	iface := ""
	if tunName != "" {
		iface = fmt.Sprintf(`"interface_name":%q,`, tunName)
	}
	return []byte(fmt.Sprintf(`{
      "log": {"level":"info"},
      "dns": {
        "independent_cache": true,
        "strategy": "prefer_ipv4",
        "servers": [
          {"tag":"dns-google","type":"tls","server":"8.8.8.8"},
          {"tag":"local","type":"tls","server":"1.1.1.1"}
        ],
        "rules": [{"query_type":["A","AAAA"],"server":"dns-google"}]
      },
      "inbounds": [{
        "type":"tun","tag":"tun-in",%s
        "address":["172.18.0.1/30","fdfe:dcba:9876::1/126"],
        "mtu":1500,"auto_route":true,%s"strict_route":true,
        "stack":"mixed","route_exclude_address":["192.168.0.0/16"]
      }],
      "outbounds": [{"type":"xray-reality","tag":"proxy"},{"type":"direct","tag":"direct"}],
      "route": {
        "auto_detect_interface": true,
        "default_domain_resolver": "dns-google",
        "final":"proxy",
        "rules":[{"action":"sniff"},{"protocol":["dns"],"action":"hijack-dns"}]
      }
    }`, iface, autoRedirectField()))
}

// DirectTunValidationConfig isolates the BASE TUN data path with NO xray: a TUN
// inbound routes everything to sing-box's own `direct` outbound, DNS via a DoT
// server (also direct). It uses the same proven TUN structure:
// 172.18.0.1/30 + IPv6, mtu 1500, auto_route + auto_redirect
// (Linux nftables), strict_route, stack "mixed", route_exclude_address, and a
// dns-google DoT resolver. If this gets 204 but FreedomValidationConfigs does
// not, the base TUN is fine and the fault is xray-under-TUN; if this also fails,
// the base config itself is wrong.
//
// The returned xray config is an unused freedom stub (Start builds one anyway).
func DirectTunValidationConfig(tunName string) (singBox, xray []byte) {
	xray = []byte(`{
      "log": {"loglevel":"warning"},
      "inbounds": [{"listen":"@il-xray-stub-direct","protocol":"socks","settings":{"udp":false}}],
      "outbounds": [{"protocol":"freedom","tag":"direct"}]
    }`)

	singBox = []byte(fmt.Sprintf(`{
      "log": {"level":"info"},
      "dns": {
        "independent_cache": true,
        "strategy": "prefer_ipv4",
        "servers": [
          {"tag":"dns-google","type":"tls","server":"8.8.8.8"},
          {"tag":"local","type":"tls","server":"1.1.1.1"}
        ],
        "rules": [{"query_type":["A","AAAA"],"server":"dns-google"}]
      },
      "inbounds": [{
        "type":"tun","tag":"tun-in","interface_name":%q,
        "address":["172.18.0.1/30","fdfe:dcba:9876::1/126"],
        "mtu":1500,"auto_route":true,%s"strict_route":true,
        "stack":"mixed","route_exclude_address":["192.168.0.0/16"]
      }],
      "outbounds": [{"type":"direct","tag":"direct"}],
      "route": {
        "auto_detect_interface": true,
        "default_domain_resolver": "dns-google",
        "final": "direct",
        "rules": [{"action":"sniff"},{"protocol":["dns"],"action":"hijack-dns"}]
      }
    }`, tunName, autoRedirectField()))

	return singBox, xray
}

// SocksValidationConfigs returns a (sing-box, xray) pair that exercises the
// in-memory bridge END-TO-END WITHOUT a TUN: a sing-box SOCKS inbound on
// host:port routes to the xray-reality outbound (core.Dial) backed by a direct
// freedom xray. Binding a localhost port needs NO root and no gvisor stack, so
// this is the part of the data plane that is self-verifiable anywhere — it proves
// sing-box inbound -> route -> core.Dial -> xray -> real internet. DNS is resolved
// inside xray (the SOCKS client passes the domain), so no dns section is needed.
func SocksValidationConfigs(host string, port int) (singBox, xray []byte) {
	xray = []byte(`{
      "log": {"loglevel":"warning"},
      "inbounds": [{"listen":"@il-xray-stub-socks","protocol":"socks","settings":{"udp":false}}],
      "outbounds": [{"protocol":"freedom","tag":"direct"}]
    }`)

	return singBoxSocksConfig(host, port), xray
}

// singBoxSocksConfig is the no-root sing-box side: a SOCKS inbound on
// host:port routing everything to the xray-reality outbound. Shared by the
// freedom validation pair and the real-node compilation (compile.go).
func singBoxSocksConfig(host string, port int) []byte {
	return []byte(fmt.Sprintf(`{
      "log": {"level":"warn"},
      "inbounds": [{"type":"socks","tag":"socks-in","listen":%q,"listen_port":%d}],
      "outbounds": [{"type":"xray-reality","tag":"proxy"},{"type":"direct","tag":"direct"}],
      "route": {"final":"proxy","rules":[{"action":"sniff"}]}
    }`, host, port))
}
