package proxy

import (
	"encoding/json"
	"reflect"
	"testing"

	"ironlink/daemon/internal/api"
)

func TestParseHysteriaBasic(t *testing.T) {
	u := "hysteria://host:443?auth=secret&peer=ex.com&insecure=1&upmbps=50&downmbps=100&obfs=xplus#x"

	want := &HysteriaConfig{
		ServerName: "x",
		Address:    "host",
		Port:       443,
		Auth:       "secret",
		SNI:        "ex.com",
		Insecure:   true,
		UpMbps:     50,
		DownMbps:   100,
		Obfs:       "xplus",
	}

	p, err := ParseURL(u)
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	got, ok := p.(*HysteriaConfig)
	if !ok {
		t.Fatalf("ParseURL returned %T, want *HysteriaConfig", p)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsed config mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestParseHysteriaDefaults(t *testing.T) {
	// No auth, no fragment: default display name, empty auth/sni, all flags off.
	p, err := ParseURL("hysteria://example.com:8443")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*HysteriaConfig)
	if c.ServerName != "New Hysteria" {
		t.Errorf("default name = %q, want %q", c.ServerName, "New Hysteria")
	}
	if c.Auth != "" || c.SNI != "" || c.Insecure || c.UpMbps != 0 || c.DownMbps != 0 {
		t.Errorf("defaults wrong: %+v", c)
	}
}

func TestParseHysteriaMissingPortIsError(t *testing.T) {
	if _, err := ParseURL("hysteria://host?auth=secret"); err == nil {
		t.Error("expected error for missing port")
	}
}

func TestParseHysteriaMissingHostIsError(t *testing.T) {
	if _, err := ParseURL("hysteria://:443?auth=secret"); err == nil {
		t.Error("expected error for missing host")
	}
}

func TestHysteriaSingBoxOutbound(t *testing.T) {
	p, err := ParseURL("hysteria://host:443?auth=secret&peer=ex.com&insecure=1&upmbps=50&downmbps=100&obfs=xplus&alpn=h3#x")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	ob, err := p.SingBoxOutbound("out")
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}

	if ob["type"] != "hysteria" {
		t.Errorf("type = %v, want hysteria", ob["type"])
	}
	if ob["tag"] != "out" {
		t.Errorf("tag = %v, want out", ob["tag"])
	}
	if ob["server"] != "host" {
		t.Errorf("server = %v, want host", ob["server"])
	}
	if ob["server_port"] != uint16(443) {
		t.Errorf("server_port = %v, want 443", ob["server_port"])
	}
	if ob["auth_str"] != "secret" {
		t.Errorf("auth_str = %v, want secret", ob["auth_str"])
	}
	if ob["up_mbps"] != 50 {
		t.Errorf("up_mbps = %v, want 50", ob["up_mbps"])
	}
	if ob["down_mbps"] != 100 {
		t.Errorf("down_mbps = %v, want 100", ob["down_mbps"])
	}
	if ob["obfs"] != "xplus" {
		t.Errorf("obfs = %v, want xplus", ob["obfs"])
	}

	tls, ok := ob["tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls is %T, want map", ob["tls"])
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
	if alpn, ok := tls["alpn"].([]string); !ok || len(alpn) != 1 || alpn[0] != "h3" {
		t.Errorf("tls.alpn = %v, want [h3]", tls["alpn"])
	}
}

func TestHysteriaSingBoxOutboundOmitsZeroBandwidth(t *testing.T) {
	// No upmbps/downmbps/obfs in the link → those keys are omitted entirely.
	p, err := ParseURL("hysteria://host:443?auth=secret&peer=ex.com")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	ob, err := p.SingBoxOutbound("out")
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if _, ok := ob["up_mbps"]; ok {
		t.Error("up_mbps should be omitted when 0")
	}
	if _, ok := ob["down_mbps"]; ok {
		t.Error("down_mbps should be omitted when 0")
	}
	if _, ok := ob["obfs"]; ok {
		t.Error("obfs should be omitted when empty")
	}
}

func TestHysteriaXrayOutboundUnsupported(t *testing.T) {
	c := &HysteriaConfig{Address: "host", Port: 443}
	ob, ok, err := c.XrayOutbound()
	if err != nil {
		t.Fatalf("XrayOutbound err: %v", err)
	}
	if ok {
		t.Error("XrayOutbound ok = true, want false (xray has no Hysteria)")
	}
	if ob != nil {
		t.Errorf("XrayOutbound outbound = %v, want nil", ob)
	}
}

func TestHysteriaDialableBy(t *testing.T) {
	c := &HysteriaConfig{Address: "host", Port: 443}
	if !c.dialableBy(api.CoreSingBox) {
		t.Error("dialableBy(CoreSingBox) = false, want true")
	}
	if c.dialableBy(api.CoreXray) {
		t.Error("dialableBy(CoreXray) = true, want false")
	}
}

func TestHysteriaLabelsAndCamouflage(t *testing.T) {
	c := &HysteriaConfig{Address: "host", Port: 443, SNI: "ex.com"}
	if c.TransportLabel() != "quic" {
		t.Errorf("TransportLabel = %q, want quic", c.TransportLabel())
	}
	if c.SecurityLabel() != "tls" {
		t.Errorf("SecurityLabel = %q, want tls", c.SecurityLabel())
	}
	sni, ok := c.CamouflageSNI()
	if !ok || sni != "ex.com" {
		t.Errorf("CamouflageSNI = (%q, %v), want (ex.com, true)", sni, ok)
	}
}

func TestHysteriaIdentityFallsBackToAddress(t *testing.T) {
	withAuth := &HysteriaConfig{Address: "host", Auth: "secret"}
	if withAuth.Identity() != "secret" {
		t.Errorf("Identity = %q, want secret", withAuth.Identity())
	}
	noAuth := &HysteriaConfig{Address: "host"}
	if noAuth.Identity() != "host" {
		t.Errorf("Identity (no auth) = %q, want host", noAuth.Identity())
	}
}

func TestHysteriaProfileDocRoundTrip(t *testing.T) {
	p, err := ParseURL("hysteria://host:443?auth=secret&peer=ex.com&insecure=1&upmbps=50&downmbps=100&obfs=xplus&alpn=h3&protocol=udp#x")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}

	doc, err := EncodeProfileDoc(p)
	if err != nil {
		t.Fatalf("EncodeProfileDoc: %v", err)
	}

	// The on-disk shape is the single-key tagged union {"hysteria": <body>}.
	var union map[string]json.RawMessage
	if err := json.Unmarshal(doc, &union); err != nil {
		t.Fatalf("union unmarshal: %v", err)
	}
	raw, ok := union["hysteria"]
	if !ok {
		t.Fatalf("doc has no \"hysteria\" key: %s", doc)
	}

	back, err := DecodeProfile(ProtocolHysteria, raw)
	if err != nil {
		t.Fatalf("DecodeProfile: %v", err)
	}
	if !reflect.DeepEqual(p, back) {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", back, p)
	}
}
