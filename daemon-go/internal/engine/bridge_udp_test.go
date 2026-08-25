package engine

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	xcore "github.com/xtls/xray-core/core"
)

// TestBridgeUDPDNS closes the plan §9 open item "UDP via core.DialUDP — wired
// in the spike glue, not yet runtime-tested": the bridge outbound's
// ListenPacket (sing-box's packet path → xcore.DialUDP → xray freedom) carries
// a REAL UDP round trip — a DNS A query to 8.8.8.8:53. No root; needs network
// egress (like TestBridgeViaSocks). QUIC rides this same packet path.
func TestBridgeUDPDNS(t *testing.T) {
	xinst, err := BuildXray([]byte(`{
      "log": {"loglevel":"warning"},
      "inbounds": [{"listen":"@il-xray-stub-udp","protocol":"socks","settings":{"udp":false}}],
      "outbounds": [{"protocol":"freedom","tag":"direct"}]
    }`))
	if err != nil {
		t.Fatalf("BuildXray: %v", err)
	}
	defer xinst.Close()

	exchangeDNSOverBridge(t, xinst)
}

// TestRealNodeUDPDNS is the same UDP round trip THROUGH A REAL NODE (vless
// xudp encapsulation): part of the env-gated real-node suite.
func TestRealNodeUDPDNS(t *testing.T) {
	v := realNodeFromEnv(t)
	cfg, err := CompileXrayClient(v, "@il-xray-node-udp")
	if err != nil {
		t.Fatalf("CompileXrayClient: %v", err)
	}
	xinst, err := BuildXray(cfg)
	if err != nil {
		t.Fatalf("BuildXray: %v", err)
	}
	defer xinst.Close()

	exchangeDNSOverBridge(t, xinst)
}

// exchangeDNSOverBridge sends a DNS A query to 8.8.8.8:53 through the bridge
// outbound's packet path and asserts a matching response arrives.
func exchangeDNSOverBridge(t *testing.T, xinst *xcore.Instance) {
	t.Helper()
	ob := &backendOutbound{typ: OutboundType, tag: "proxy", nodeID: singleNodeTag, be: newXrayBackend(xinst)}
	dest := M.ParseSocksaddr("8.8.8.8:53")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pc, err := ob.ListenPacket(ctx, dest)
	if err != nil {
		t.Fatalf("ListenPacket (core.DialUDP): %v", err)
	}
	defer pc.Close()

	query := dnsQuery(0x4242, "google.com")
	if _, err := pc.WriteTo(query, &net.UDPAddr{IP: net.IPv4(8, 8, 8, 8), Port: 53}); err != nil {
		t.Fatalf("WriteTo through the bridge: %v", err)
	}

	pc.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom through the bridge: %v", err)
	}
	if n < 12 {
		t.Fatalf("short DNS response: %d bytes", n)
	}
	if id := binary.BigEndian.Uint16(buf[:2]); id != 0x4242 {
		t.Errorf("DNS response ID = %#x, want 0x4242", id)
	}
	if buf[2]&0x80 == 0 {
		t.Error("DNS response QR bit not set — not a response")
	}
}

// dnsQuery builds a minimal DNS A query for name (no EDNS, recursion
// desired).
func dnsQuery(id uint16, name string) []byte {
	q := make([]byte, 0, 12+len(name)+6)
	q = binary.BigEndian.AppendUint16(q, id)
	q = append(q, 0x01, 0x00)               // RD
	q = binary.BigEndian.AppendUint16(q, 1) // QDCOUNT
	q = append(q, 0, 0, 0, 0, 0, 0)         // AN/NS/AR
	start := 0
	label := []byte(name + ".")
	for i, c := range label {
		if c == '.' {
			q = append(q, byte(i-start))
			q = append(q, label[start:i]...)
			start = i + 1
		}
	}
	q = append(q, 0)                        // root
	q = binary.BigEndian.AppendUint16(q, 1) // QTYPE A
	q = binary.BigEndian.AppendUint16(q, 1) // QCLASS IN
	return q
}
