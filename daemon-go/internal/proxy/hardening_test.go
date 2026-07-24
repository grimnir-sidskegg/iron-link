package proxy

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// A malformed stored union (kind set, payload pointer nil — reachable via a
// corrupt/hand-edited profile file, since DecodeProfile does no validation)
// must NOT panic the daemon when its outbounds are compiled.
func TestMalformedUnionDoesNotPanic(t *testing.T) {
	v := &VlessConfig{
		ServerName: "x", UUID: "u", Address: "h", Port: 443, Encryption: "none",
		Security:  Security{Kind: SecurityReality}, // Reality == nil
		Transport: Transport{Kind: TransportWs},    // Ws == nil
	}
	if _, err := v.SingBoxOutbound("t"); err != nil {
		t.Fatalf("SingBoxOutbound on malformed union errored: %v", err)
	}
	if _, _, err := v.XrayOutbound(); err != nil {
		t.Fatalf("XrayOutbound on malformed union errored: %v", err)
	}

	// Same via the decode path (a stored doc with kind but no payload).
	raw := json.RawMessage(`{"server_name":"x","uuid":"u","address":"h","port":443,"security":{"kind":"reality"},"transport":{"kind":"grpc"}}`)
	p, err := DecodeProfile(ProtocolVless, raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := p.SingBoxOutbound("t"); err != nil {
		t.Fatalf("decoded malformed union SingBoxOutbound errored: %v", err)
	}
}

// xray rejects a VLESS user without encryption; a decoded doc that omits the
// field (Encryption == "") must still emit encryption:"none".
func TestVlessXrayDefaultsEncryption(t *testing.T) {
	v := &VlessConfig{UUID: "u", Address: "h", Port: 443} // Encryption == ""
	ob, ok, err := v.XrayOutbound()
	if !ok || err != nil {
		t.Fatalf("XrayOutbound: ok=%v err=%v", ok, err)
	}
	user := ob["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
	if user["encryption"] != "none" {
		t.Errorf("encryption = %v, want \"none\"", user["encryption"])
	}
}

// A vmess link declaring reality carries no pbk/sid in the v2rayN format, so it
// is unconstructable and must be rejected (not stored as a dead node).
func TestVmessRealityRejected(t *testing.T) {
	jsonBody := `{"v":"2","ps":"x","add":"h","port":"443","id":"b831381d-6324-4d53-ad4f-8cda48b30811","aid":"0","net":"tcp","tls":"reality","sni":"e.com"}`
	link := "vmess://" + base64.StdEncoding.EncodeToString([]byte(jsonBody))
	if _, err := ParseURL(link); err == nil || !strings.Contains(err.Error(), "reality") {
		t.Errorf("expected a reality-unsupported error, got %v", err)
	}
}
