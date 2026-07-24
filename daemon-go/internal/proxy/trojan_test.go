package proxy

import (
	"encoding/json"
	"slices"
	"testing"

	"ironlink/daemon/internal/api"
)

func TestParseTrojanBasicTLS(t *testing.T) {
	p, err := ParseURL("trojan://pass@host:443?security=tls&sni=ex.com#x")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c, ok := p.(*TrojanConfig)
	if !ok {
		t.Fatalf("ParseURL returned %T, want *TrojanConfig", p)
	}
	if c.Password != "pass" {
		t.Errorf("password = %q, want %q", c.Password, "pass")
	}
	if c.Address != "host" || c.Port != 443 {
		t.Errorf("endpoint = %s:%d, want host:443", c.Address, c.Port)
	}
	if c.Security.Kind != SecurityTLS || c.Security.SNI() != "ex.com" {
		t.Errorf("security wrong: %+v", c.Security)
	}
	if c.ServerName != "x" {
		t.Errorf("name = %q, want %q", c.ServerName, "x")
	}
	if c.Kind() != ProtocolTrojan {
		t.Errorf("kind = %q, want %q", c.Kind(), ProtocolTrojan)
	}
	if c.Identity() != "pass" {
		t.Errorf("identity = %q, want password", c.Identity())
	}
}

func TestParseTrojanDefaultsToTLS(t *testing.T) {
	// Trojan is TLS by definition: an absent `security` query is still tls,
	// with the default port 443 and the default display name.
	p, err := ParseURL("trojan://secret@example.com")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*TrojanConfig)
	if c.Security.Kind != SecurityTLS {
		t.Errorf("security = %q, want tls (Trojan is always TLS)", c.Security.Kind)
	}
	if c.Port != 443 || c.ServerName != "New Trojan" || c.Transport.Kind != TransportTCP {
		t.Errorf("defaults wrong: %+v", c)
	}
}

func TestParseTrojanWsTransport(t *testing.T) {
	p, err := ParseURL("trojan://pw@h:8443?security=tls&sni=cdn.example.com&type=ws&path=%2Fws&host=cdn.example.com#WS")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*TrojanConfig)
	if c.Transport.Kind != TransportWs || c.Transport.Ws.Path != "/ws" || c.Transport.Ws.Host != "cdn.example.com" {
		t.Errorf("ws params wrong: %+v", c.Transport)
	}
}

func TestParseTrojanReality(t *testing.T) {
	p, err := ParseURL("trojan://pw@h:443?security=reality&sni=g.com&fp=chrome&pbk=PBK&sid=01ab#R")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*TrojanConfig)
	if c.Security.Kind != SecurityReality {
		t.Fatalf("security = %q, want reality", c.Security.Kind)
	}
	if c.Security.Reality.Pbk != "PBK" || c.Security.Reality.Sid != "01ab" {
		t.Errorf("reality params wrong: %+v", c.Security.Reality)
	}
}

func TestParseTrojanRealityRequiresAllParams(t *testing.T) {
	if _, err := ParseURL("trojan://pw@h:443?security=reality&sni=g.com"); err == nil {
		t.Error("expected error for reality link missing fp/pbk/sid")
	}
}

func TestTrojanSingBoxOutbound(t *testing.T) {
	p, _ := ParseURL("trojan://pass@host:443?security=tls&sni=ex.com#x")
	ob, err := p.(*TrojanConfig).SingBoxOutbound("trojan-out")
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if ob["type"] != "trojan" {
		t.Errorf("type = %v, want trojan", ob["type"])
	}
	if ob["password"] != "pass" {
		t.Errorf("password = %v, want pass", ob["password"])
	}
	if ob["server"] != "host" || ob["server_port"] != uint16(443) {
		t.Errorf("endpoint = %v:%v", ob["server"], ob["server_port"])
	}
	tls, ok := ob["tls"].(map[string]any)
	if !ok {
		t.Fatalf("missing tls block: %+v", ob)
	}
	if tls["enabled"] != true || tls["server_name"] != "ex.com" {
		t.Errorf("tls block wrong: %+v", tls)
	}
}

func TestTrojanXrayOutbound(t *testing.T) {
	p, _ := ParseURL("trojan://pass@host:443?security=tls&sni=ex.com#x")
	ob, ok, err := p.(*TrojanConfig).XrayOutbound()
	if err != nil {
		t.Fatalf("XrayOutbound: %v", err)
	}
	if !ok {
		t.Fatal("XrayOutbound ok = false, want true (xray speaks Trojan)")
	}
	if ob["protocol"] != "trojan" {
		t.Errorf("protocol = %v, want trojan", ob["protocol"])
	}
	settings := ob["settings"].(map[string]any)
	servers := settings["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(servers))
	}
	srv := servers[0].(map[string]any)
	if srv["address"] != "host" || srv["password"] != "pass" || srv["port"] != uint16(443) {
		t.Errorf("server target wrong: %+v", srv)
	}
	if _, ok := ob["streamSettings"]; !ok {
		t.Error("missing streamSettings (engine needs it for sockopt injection)")
	}
}

func TestTrojanDialableBy(t *testing.T) {
	// tcp/tls → both cores.
	tcp, _ := ParseURL("trojan://pw@h:443?security=tls&sni=ex.com")
	tc := tcp.(*TrojanConfig)
	if !tc.dialableBy(api.CoreSingBox) || !tc.dialableBy(api.CoreXray) {
		t.Error("tcp/tls Trojan must be dialable by both cores")
	}

	// xhttp → xray only (sing-box has no xhttp client).
	xh, _ := ParseURL("trojan://pw@h:443?security=tls&sni=ex.com&type=xhttp&mode=auto")
	xc := xh.(*TrojanConfig)
	if xc.dialableBy(api.CoreSingBox) {
		t.Error("xhttp Trojan must NOT be dialable by sing-box")
	}
	if !xc.dialableBy(api.CoreXray) {
		t.Error("xhttp Trojan must be dialable by xray")
	}
}

func TestTrojanRoundTrip(t *testing.T) {
	p, _ := ParseURL("trojan://pass@host:8443?security=reality&sni=g.com&fp=chrome&pbk=PBK&sid=01ab&type=grpc&serviceName=gs#node")
	doc, err := EncodeProfileDoc(p)
	if err != nil {
		t.Fatalf("EncodeProfileDoc: %v", err)
	}

	// The on-disk shape is a single-key tagged union under "trojan".
	var union map[string]json.RawMessage
	if err := json.Unmarshal(doc, &union); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	raw, ok := union["trojan"]
	if !ok {
		t.Fatalf("doc not keyed by trojan: %s", doc)
	}

	back, err := DecodeProfile(ProtocolTrojan, raw)
	if err != nil {
		t.Fatalf("DecodeProfile: %v", err)
	}
	got := back.(*TrojanConfig)
	want := p.(*TrojanConfig)
	if got.Password != want.Password || got.Address != want.Address || got.Port != want.Port {
		t.Errorf("round-trip endpoint mismatch:\n got %+v\nwant %+v", got, want)
	}
	if got.Security.Kind != SecurityReality || got.Security.Reality.Pbk != "PBK" {
		t.Errorf("round-trip security mismatch: %+v", got.Security)
	}
	if got.Transport.Kind != TransportGrpc || got.Transport.Grpc.ServiceName != "gs" {
		t.Errorf("round-trip transport mismatch: %+v", got.Transport)
	}
}

// Guard that the registry actually wired the trojan scheme + kind.
func TestTrojanRegistered(t *testing.T) {
	if _, err := ParseURL("trojan://pw@h:443"); err != nil {
		t.Errorf("trojan scheme not registered: %v", err)
	}
	p, err := ParseURL("trojan://pw@h:443?security=tls&sni=ex.com")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	eligible := EligibleCores(p)
	if !slices.Contains(eligible, api.CoreSingBox) || !slices.Contains(eligible, api.CoreXray) {
		t.Errorf("EligibleCores = %v, want both cores", eligible)
	}
}
