package proxy

import (
	"testing"

	"ironlink/daemon/internal/api"
)

// vlessNode builds a test VLESS profile, mirroring the Rust test helper.
func vlessNode(transport Transport, security Security, flow string) *VlessConfig {
	return &VlessConfig{
		ServerName: "t",
		UUID:       "u",
		Address:    "1.2.3.4",
		Port:       443,
		Encryption: "none",
		Flow:       flow,
		Security:   security,
		Transport:  transport,
	}
}

func reality() Security {
	return Security{Kind: SecurityReality, Reality: &RealityParams{
		SNI: "google.com", Fp: "chrome", Pbk: "pbk", Sid: "sid",
	}}
}

func xhttp() Transport {
	return Transport{Kind: TransportXhttp, Xhttp: &XhttpParams{}}
}

func grpc() Transport {
	return Transport{Kind: TransportGrpc, Grpc: &GrpcParams{}}
}

func coreCanDial(core api.CoreType, p Profile) bool {
	return p.dialableBy(core)
}

func TestSingBoxCanDialGrpcReality(t *testing.T) {
	node := vlessNode(grpc(), reality(), "")
	if !coreCanDial(api.CoreSingBox, node) {
		t.Error("sing-box must dial grpc+reality")
	}
}

func TestSingBoxCannotDialXhttp(t *testing.T) {
	node := vlessNode(xhttp(), reality(), "")
	if coreCanDial(api.CoreSingBox, node) {
		t.Error("sing-box has no xhttp client and must not cover xhttp")
	}
}

func TestXrayCanDialXhttp(t *testing.T) {
	node := vlessNode(xhttp(), reality(), "")
	if !coreCanDial(api.CoreXray, node) {
		t.Error("xray must dial xhttp+reality")
	}
}

func TestBothCoresDialTcpRealityVision(t *testing.T) {
	node := vlessNode(Transport{Kind: TransportTCP}, reality(), "xtls-rprx-vision")
	if !coreCanDial(api.CoreSingBox, node) || !coreCanDial(api.CoreXray, node) {
		t.Error("both cores must dial tcp+reality+vision")
	}
}

func TestVisionUdp443FlowIsRecognized(t *testing.T) {
	node := vlessNode(Transport{Kind: TransportTCP}, reality(), "xtls-rprx-vision-udp443")
	if !coreCanDial(api.CoreSingBox, node) {
		t.Error("the -udp443 vision suffix must be recognized")
	}
}

func TestCanTunMatchesDescriptors(t *testing.T) {
	if !CoreCaps(api.CoreSingBox).CanTUN {
		t.Error("sing-box must be TUN-capable")
	}
	if CoreCaps(api.CoreXray).CanTUN {
		t.Error("xray must not be TUN-capable")
	}
}

func TestCoversIgnoresTransportPayload(t *testing.T) {
	// Matching is by variant kind: a ws transport with a payload is still ws.
	node := vlessNode(
		Transport{Kind: TransportWs, Ws: &WsParams{Path: "/x", Host: "h"}},
		Security{Kind: SecurityNone}, "")
	if !coreCanDial(api.CoreSingBox, node) {
		t.Error("ws payload must not affect kind matching")
	}
}

func TestUnknownCoreCoversNothing(t *testing.T) {
	node := vlessNode(Transport{Kind: TransportTCP}, Security{Kind: SecurityNone}, "")
	if node.dialableBy(api.CoreType("Mihomo")) {
		t.Error("an unknown core type must cover nothing")
	}
}
