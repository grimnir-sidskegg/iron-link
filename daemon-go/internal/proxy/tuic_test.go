package proxy

import (
	"encoding/json"
	"reflect"
	"testing"

	"ironlink/daemon/internal/api"
)

func TestParseTUICBasic(t *testing.T) {
	url := "tuic://88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a:s3cr3t@example.com:443?sni=ex.com&congestion_control=bbr&alpn=h3&udp_relay_mode=native&allow_insecure=1#My%20TUIC"

	want := &TUICConfig{
		ServerName:        "My TUIC",
		Address:           "example.com",
		Port:              443,
		UUID:              "88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a",
		Password:          "s3cr3t",
		SNI:               "ex.com",
		CongestionControl: "bbr",
		UDPRelayMode:      "native",
		ALPN:              []string{"h3"},
		Insecure:          true,
	}

	p, err := ParseURL(url)
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	got, ok := p.(*TUICConfig)
	if !ok {
		t.Fatalf("ParseURL returned %T, want *TUICConfig", p)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsed config mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestParseTUICDefaults(t *testing.T) {
	// No port, no query, no fragment: port 443, the default display name, no
	// optional fields.
	p, err := ParseURL("tuic://u:pw@host.example")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*TUICConfig)
	if c.Port != 443 || c.ServerName != "New TUIC" {
		t.Errorf("defaults wrong: %+v", c)
	}
	if c.UUID != "u" || c.Password != "pw" {
		t.Errorf("userinfo wrong: uuid=%q password=%q", c.UUID, c.Password)
	}
	if c.SNI != "" || c.CongestionControl != "" || c.UDPRelayMode != "" || len(c.ALPN) != 0 || c.Insecure {
		t.Errorf("expected empty optionals, got: %+v", c)
	}
}

func TestParseTUICCommaSeparatedALPN(t *testing.T) {
	p, err := ParseURL("tuic://u:pw@host:443?alpn=h3,spdy/3.1")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*TUICConfig)
	if !reflect.DeepEqual(c.ALPN, []string{"h3", "spdy/3.1"}) {
		t.Errorf("ALPN = %v, want [h3 spdy/3.1]", c.ALPN)
	}
}

func TestParseTUICMissingHostIsError(t *testing.T) {
	if _, err := ParseURL("tuic://u:pw@:443"); err == nil {
		t.Error("expected error for missing host")
	}
}

func TestTUICSingBoxOutbound(t *testing.T) {
	c := &TUICConfig{
		Address:           "example.com",
		Port:              443,
		UUID:              "the-uuid",
		Password:          "the-pass",
		SNI:               "ex.com",
		CongestionControl: "bbr",
		UDPRelayMode:      "native",
		ALPN:              []string{"h3"},
		Insecure:          true,
	}
	ob, err := c.SingBoxOutbound("proxy")
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if ob["type"] != "tuic" {
		t.Errorf("type = %v, want tuic", ob["type"])
	}
	if ob["tag"] != "proxy" {
		t.Errorf("tag = %v, want proxy", ob["tag"])
	}
	if ob["server"] != "example.com" || ob["server_port"] != uint16(443) {
		t.Errorf("server/port wrong: %v / %v", ob["server"], ob["server_port"])
	}
	if ob["uuid"] != "the-uuid" || ob["password"] != "the-pass" {
		t.Errorf("uuid/password wrong: %v / %v", ob["uuid"], ob["password"])
	}
	if ob["congestion_control"] != "bbr" || ob["udp_relay_mode"] != "native" {
		t.Errorf("cc/mode wrong: %v / %v", ob["congestion_control"], ob["udp_relay_mode"])
	}
	tls, ok := ob["tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls block missing or wrong type: %T", ob["tls"])
	}
	if tls["enabled"] != true {
		t.Errorf("tls.enabled = %v, want true", tls["enabled"])
	}
	if tls["server_name"] != "ex.com" {
		t.Errorf("tls.server_name = %v, want ex.com", tls["server_name"])
	}
	if tls["insecure"] != true {
		t.Errorf("tls.insecure = %v, want true", tls["insecure"])
	}
	if !reflect.DeepEqual(tls["alpn"], []string{"h3"}) {
		t.Errorf("tls.alpn = %v, want [h3]", tls["alpn"])
	}
}

func TestTUICSingBoxOutboundOmitsEmptyOptionals(t *testing.T) {
	c := &TUICConfig{Address: "h", Port: 443, UUID: "u", Password: "p"}
	ob, err := c.SingBoxOutbound("proxy")
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if _, has := ob["congestion_control"]; has {
		t.Error("congestion_control should be omitted when empty")
	}
	if _, has := ob["udp_relay_mode"]; has {
		t.Error("udp_relay_mode should be omitted when empty")
	}
	tls := ob["tls"].(map[string]any)
	if _, has := tls["server_name"]; has {
		t.Error("tls.server_name should be omitted when empty")
	}
	if _, has := tls["alpn"]; has {
		t.Error("tls.alpn should be omitted when empty")
	}
	// insecure defaults to false but is always present.
	if tls["insecure"] != false {
		t.Errorf("tls.insecure = %v, want false", tls["insecure"])
	}
}

func TestTUICXrayOutboundUnsupported(t *testing.T) {
	c := &TUICConfig{Address: "h", Port: 443, UUID: "u", Password: "p"}
	ob, ok, err := c.XrayOutbound()
	if err != nil {
		t.Fatalf("XrayOutbound: %v", err)
	}
	if ok {
		t.Error("XrayOutbound ok = true, want false (xray has no TUIC)")
	}
	if ob != nil {
		t.Errorf("XrayOutbound outbound = %v, want nil", ob)
	}
}

func TestTUICDialableBy(t *testing.T) {
	c := &TUICConfig{Address: "h", Port: 443, UUID: "u", Password: "p"}
	if !c.dialableBy(api.CoreSingBox) {
		t.Error("dialableBy(CoreSingBox) = false, want true")
	}
	if c.dialableBy(api.CoreXray) {
		t.Error("dialableBy(CoreXray) = true, want false")
	}
}

func TestTUICLabels(t *testing.T) {
	c := &TUICConfig{SNI: "ex.com"}
	if c.TransportLabel() != "quic" {
		t.Errorf("TransportLabel = %q, want quic", c.TransportLabel())
	}
	if c.SecurityLabel() != "tls" {
		t.Errorf("SecurityLabel = %q, want tls", c.SecurityLabel())
	}
	if sni, ok := c.CamouflageSNI(); !ok || sni != "ex.com" {
		t.Errorf("CamouflageSNI = (%q, %v), want (ex.com, true)", sni, ok)
	}
}

func TestTUICProfileDocRoundTrip(t *testing.T) {
	orig := &TUICConfig{
		ServerName:        "node",
		Address:           "example.com",
		Port:              8443,
		UUID:              "u-1234",
		Password:          "p-5678",
		SNI:               "ex.com",
		CongestionControl: "bbr",
		UDPRelayMode:      "native",
		ALPN:              []string{"h3"},
		Insecure:          true,
	}

	doc, err := EncodeProfileDoc(orig)
	if err != nil {
		t.Fatalf("EncodeProfileDoc: %v", err)
	}

	// The on-disk shape is the single-key {"tuic": <body>} tagged union.
	var union map[string]json.RawMessage
	if err := json.Unmarshal(doc, &union); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	raw, ok := union["tuic"]
	if !ok {
		t.Fatalf("doc has no \"tuic\" key: %s", doc)
	}

	back, err := DecodeProfile(ProtocolTUIC, raw)
	if err != nil {
		t.Fatalf("DecodeProfile: %v", err)
	}
	got, ok := back.(*TUICConfig)
	if !ok {
		t.Fatalf("DecodeProfile returned %T, want *TUICConfig", back)
	}
	if !reflect.DeepEqual(got, orig) {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", got, orig)
	}
}
