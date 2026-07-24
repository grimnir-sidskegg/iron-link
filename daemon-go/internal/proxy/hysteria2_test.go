package proxy

import (
	"encoding/json"
	"reflect"
	"testing"

	"ironlink/daemon/internal/api"
)

// A hysteria2:// share link with auth, sni, salamander obfs and insecure parses
// field-by-field into the expected config.
func TestParseHysteria2Full(t *testing.T) {
	const link = "hysteria2://pass@host:443?sni=ex.com&obfs=salamander&obfs-password=xyz&insecure=1#x"

	p, err := ParseURL(link)
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	got, ok := p.(*Hysteria2Config)
	if !ok {
		t.Fatalf("ParseURL returned %T, want *Hysteria2Config", p)
	}

	want := &Hysteria2Config{
		ServerName:   "x",
		Address:      "host",
		Port:         443,
		Password:     "pass",
		SNI:          "ex.com",
		Insecure:     true,
		Obfs:         "salamander",
		ObfsPassword: "xyz",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsed config mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}

// The hy2:// alias registers to the same parser.
func TestParseHysteria2Alias(t *testing.T) {
	p, err := ParseURL("hy2://secret@example.com:8443?sni=front.org#node")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c, ok := p.(*Hysteria2Config)
	if !ok {
		t.Fatalf("ParseURL returned %T, want *Hysteria2Config", p)
	}
	if c.Password != "secret" || c.Address != "example.com" || c.Port != 8443 {
		t.Errorf("alias parse wrong: %+v", c)
	}
	if c.SNI != "front.org" || c.ServerName != "node" {
		t.Errorf("alias parse wrong: %+v", c)
	}
}

// Defaults: no port → 443, no fragment → "New Hysteria2", no obfs → empty.
func TestParseHysteria2Defaults(t *testing.T) {
	p, err := ParseURL("hysteria2://pw@example.com")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*Hysteria2Config)
	if c.Port != 443 || c.ServerName != "New Hysteria2" {
		t.Errorf("defaults wrong: %+v", c)
	}
	if c.Obfs != "" || c.Insecure {
		t.Errorf("expected no obfs / not insecure: %+v", c)
	}
}

// A missing host is an error.
func TestParseHysteria2MissingHost(t *testing.T) {
	if _, err := ParseURL("hysteria2://pass@"); err == nil {
		t.Fatal("expected error for missing host")
	}
}

// SingBoxOutbound emits the hysteria2 type with password, tls.server_name and
// the salamander obfs block.
func TestHysteria2SingBoxOutbound(t *testing.T) {
	c := &Hysteria2Config{
		Address:      "host",
		Port:         443,
		Password:     "pass",
		SNI:          "ex.com",
		Insecure:     true,
		Obfs:         "salamander",
		ObfsPassword: "xyz",
	}
	ob, err := c.SingBoxOutbound("proxy")
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if ob["type"] != "hysteria2" {
		t.Errorf("type = %v, want hysteria2", ob["type"])
	}
	if ob["tag"] != "proxy" || ob["server"] != "host" || ob["password"] != "pass" {
		t.Errorf("outbound core fields wrong: %+v", ob)
	}
	if ob["server_port"] != uint16(443) {
		t.Errorf("server_port = %v, want 443", ob["server_port"])
	}

	tls, ok := ob["tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls block missing or wrong type: %+v", ob["tls"])
	}
	if tls["enabled"] != true || tls["server_name"] != "ex.com" || tls["insecure"] != true {
		t.Errorf("tls block wrong: %+v", tls)
	}

	obfs, ok := ob["obfs"].(map[string]any)
	if !ok {
		t.Fatalf("obfs block missing or wrong type: %+v", ob["obfs"])
	}
	if obfs["type"] != "salamander" || obfs["password"] != "xyz" {
		t.Errorf("obfs block wrong: %+v", obfs)
	}
}

// With no SNI, server_name is omitted; with no obfs, no obfs block appears.
func TestHysteria2SingBoxOutboundMinimal(t *testing.T) {
	c := &Hysteria2Config{Address: "host", Port: 443, Password: "pass"}
	ob, err := c.SingBoxOutbound("proxy")
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	tls := ob["tls"].(map[string]any)
	if _, present := tls["server_name"]; present {
		t.Errorf("server_name should be omitted when SNI empty: %+v", tls)
	}
	if _, present := ob["obfs"]; present {
		t.Errorf("obfs block should be absent when Obfs empty: %+v", ob)
	}
}

// XrayOutbound reports ok=false — xray cannot dial Hysteria2.
func TestHysteria2XrayOutbound(t *testing.T) {
	c := &Hysteria2Config{Address: "host", Port: 443, Password: "pass"}
	ob, ok, err := c.XrayOutbound()
	if err != nil {
		t.Fatalf("XrayOutbound: %v", err)
	}
	if ok {
		t.Errorf("XrayOutbound ok = true, want false (xray has no hysteria2)")
	}
	if ob != nil {
		t.Errorf("XrayOutbound outbound = %v, want nil", ob)
	}
}

// dialableBy: sing-box yes, xray no.
func TestHysteria2DialableBy(t *testing.T) {
	c := &Hysteria2Config{Address: "host", Port: 443, Password: "pass"}
	if !c.dialableBy(api.CoreSingBox) {
		t.Error("dialableBy(CoreSingBox) = false, want true")
	}
	if c.dialableBy(api.CoreXray) {
		t.Error("dialableBy(CoreXray) = true, want false")
	}
}

// Labels and identity are the fixed Hysteria2 facts.
func TestHysteria2Labels(t *testing.T) {
	c := &Hysteria2Config{Password: "pw", SNI: "ex.com"}
	if c.TransportLabel() != "quic" || c.SecurityLabel() != "tls" {
		t.Errorf("labels wrong: %q / %q", c.TransportLabel(), c.SecurityLabel())
	}
	if c.Identity() != "pw" {
		t.Errorf("Identity = %q, want pw", c.Identity())
	}
	sni, ok := c.CamouflageSNI()
	if !ok || sni != "ex.com" {
		t.Errorf("CamouflageSNI = %q,%v want ex.com,true", sni, ok)
	}
}

// EncodeProfileDoc → DecodeProfile round-trips through the {"hysteria2": …}
// tagged-union body the store persists.
func TestHysteria2StoreRoundTrip(t *testing.T) {
	want := &Hysteria2Config{
		ServerName:   "node",
		Address:      "host",
		Port:         8443,
		Password:     "pass",
		SNI:          "ex.com",
		Insecure:     true,
		Obfs:         "salamander",
		ObfsPassword: "xyz",
	}

	doc, err := EncodeProfileDoc(want)
	if err != nil {
		t.Fatalf("EncodeProfileDoc: %v", err)
	}

	// The on-disk shape is a single-key union keyed by the protocol kind.
	var union map[string]json.RawMessage
	if err := json.Unmarshal(doc, &union); err != nil {
		t.Fatalf("union unmarshal: %v", err)
	}
	body, ok := union["hysteria2"]
	if !ok {
		t.Fatalf("union missing \"hysteria2\" key: %s", doc)
	}

	p, err := DecodeProfile(ProtocolHysteria2, body)
	if err != nil {
		t.Fatalf("DecodeProfile: %v", err)
	}
	got, ok := p.(*Hysteria2Config)
	if !ok {
		t.Fatalf("DecodeProfile returned %T, want *Hysteria2Config", p)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}
