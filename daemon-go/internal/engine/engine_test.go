//go:build with_gvisor

// Construct-time verification (no root): reproduces the spike's 1d-construct
// claim INSIDE daemon-go — a sing-box TUN inbound routing to the custom
// xray-reality outbound (core.Dial) assembles in one process with the pinned
// cores. Run: `go test -tags "with_gvisor,with_utls,with_clash_api" ./internal/engine/`. The runtime 204
// (box.Start opens the TUN — needs root) is exercised separately.
package engine

import (
	"testing"

	ilproxy "ironlink/daemon/internal/proxy"
)

func TestConstructTunPlusXrayBridge(t *testing.T) {
	xinst, err := BuildXray([]byte(`{
      "log":{"loglevel":"warning"},
      "inbounds":[{"listen":"@il-xray-stub","protocol":"socks","settings":{"udp":false}}],
      "outbounds":[{"protocol":"freedom","tag":"direct"}]
    }`))
	if err != nil {
		t.Fatalf("BuildXray: %v", err)
	}
	defer xinst.Close()

	b, err := BuildBox([]byte(`{
      "log": {"level":"warn"},
      "inbounds": [{"type":"tun","tag":"tun-in","address":["172.18.0.1/30"],"auto_route":false,"stack":"gvisor"}],
      "outbounds": [{"type":"xray-reality","tag":"proxy"},{"type":"direct","tag":"direct"}],
      "route": {"final":"proxy","rules":[{"action":"sniff"}]}
    }`), []Backend{newXrayBackend(xinst)}, nil)
	if err != nil {
		t.Fatalf("BuildBox (construct TUN + xray-reality): %v", err)
	}
	if b == nil {
		t.Fatal("BuildBox returned nil box")
	}
	b.Close()
}

// TestConstructFreedomValidationConfig parses & assembles the freedom validation
// config — crucially the `dns` section the spike lacked. If the sing-box 1.12 DNS
// schema were wrong, box.New would reject it here (no root needed).
func TestConstructFreedomValidationConfig(t *testing.T) {
	sbCfg, xrayCfg := FreedomValidationConfigs("iltest0")
	xinst, err := BuildXray(xrayCfg)
	if err != nil {
		t.Fatalf("BuildXray: %v", err)
	}
	defer xinst.Close()
	b, err := BuildBox(sbCfg, []Backend{newXrayBackend(xinst)}, nil)
	if err != nil {
		t.Fatalf("BuildBox with dns section: %v", err)
	}
	b.Close()
}

// TestConstructNodeTUNConfigs assembles the COMPILED real-node TUN pair with
// the real cores (no root — the TUN opens only at Start): the proven sing-box
// TUN structure plus a CompileXrayClient config instead of the freedom stub.
func TestConstructNodeTUNConfigs(t *testing.T) {
	v := testVless(testReality(),
		ilproxy.Transport{Kind: ilproxy.TransportXhttp, Xhttp: &ilproxy.XhttpParams{}}, "")
	sbCfg, xrayCfg, err := NodeTUNConfigs(v, "iltest1")
	if err != nil {
		t.Fatalf("NodeTUNConfigs: %v", err)
	}
	xinst, err := BuildXray(xrayCfg)
	if err != nil {
		t.Fatalf("BuildXray (compiled node config): %v", err)
	}
	defer xinst.Close()
	b, err := BuildBox(sbCfg, []Backend{newXrayBackend(xinst)}, nil)
	if err != nil {
		t.Fatalf("BuildBox (TUN + compiled node config): %v", err)
	}
	b.Close()
}
