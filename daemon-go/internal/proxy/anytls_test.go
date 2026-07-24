package proxy

import (
	"encoding/json"
	"reflect"
	"testing"

	"ironlink/daemon/internal/api"
)

func TestParseAnyTLSBasic(t *testing.T) {
	p, err := ParseURL("anytls://pass@host:443?sni=ex.com&insecure=1#x")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c, ok := p.(*AnyTLSConfig)
	if !ok {
		t.Fatalf("ParseURL returned %T, want *AnyTLSConfig", p)
	}
	want := &AnyTLSConfig{
		ServerName: "x",
		Address:    "host",
		Port:       443,
		Password:   "pass",
		SNI:        "ex.com",
		Insecure:   true,
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("parsed config mismatch:\n got: %+v\nwant: %+v", c, want)
	}
}

func TestParseAnyTLSDefaults(t *testing.T) {
	// No port, no query, no fragment: port 443, no SNI, not insecure, the
	// default display name.
	p, err := ParseURL("anytls://secret@example.com")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*AnyTLSConfig)
	if c.Port != 443 || c.Password != "secret" || c.ServerName != "New AnyTLS" {
		t.Errorf("defaults wrong: %+v", c)
	}
	if c.SNI != "" || c.Insecure {
		t.Errorf("default sni/insecure wrong: %+v", c)
	}
}

func TestParseAnyTLSPercentDecodedPassword(t *testing.T) {
	// Password with special chars is percent-encoded in the userinfo and must
	// decode back.
	p, err := ParseURL("anytls://p%40ss%2Fword@host:8443#node")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*AnyTLSConfig)
	if c.Password != "p@ss/word" {
		t.Errorf("password = %q, want %q", c.Password, "p@ss/word")
	}
	if c.Port != 8443 {
		t.Errorf("port = %d, want 8443", c.Port)
	}
}

func TestParseAnyTLSServerNameAlias(t *testing.T) {
	// `servername` is an accepted alias for `sni`.
	p, err := ParseURL("anytls://k@h:443?servername=cdn.example.com")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	if c := p.(*AnyTLSConfig); c.SNI != "cdn.example.com" {
		t.Errorf("sni = %q, want %q", c.SNI, "cdn.example.com")
	}
}

func TestParseAnyTLSMissingHostIsError(t *testing.T) {
	if _, err := ParseURL("anytls://pass@:443"); err == nil {
		t.Error("expected error for missing host")
	}
}

func TestAnyTLSSingBoxOutbound(t *testing.T) {
	c := &AnyTLSConfig{
		ServerName: "n",
		Address:    "host",
		Port:       8443,
		Password:   "pass",
		SNI:        "ex.com",
		Insecure:   true,
	}
	ob, err := c.SingBoxOutbound("anytls-out")
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if ob["type"] != "anytls" {
		t.Errorf("type = %v, want anytls", ob["type"])
	}
	if ob["tag"] != "anytls-out" {
		t.Errorf("tag = %v, want anytls-out", ob["tag"])
	}
	if ob["server"] != "host" || ob["server_port"] != uint16(8443) {
		t.Errorf("server/port wrong: %v %v", ob["server"], ob["server_port"])
	}
	if ob["password"] != "pass" {
		t.Errorf("password = %v, want pass", ob["password"])
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
}

func TestAnyTLSSingBoxOutboundOmitsEmptySNI(t *testing.T) {
	// No SNI / not insecure: server_name and insecure keys are omitted; the tls
	// block still carries enabled:true (sing-box requires the block).
	c := &AnyTLSConfig{Address: "host", Port: 443, Password: "p"}
	ob, err := c.SingBoxOutbound("t")
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	tls := ob["tls"].(map[string]any)
	if tls["enabled"] != true {
		t.Errorf("tls.enabled = %v, want true", tls["enabled"])
	}
	if _, present := tls["server_name"]; present {
		t.Errorf("tls.server_name should be omitted for empty SNI, got %v", tls["server_name"])
	}
	if _, present := tls["insecure"]; present {
		t.Errorf("tls.insecure should be omitted when not insecure")
	}
}

func TestAnyTLSXrayOutboundNotSupported(t *testing.T) {
	c := &AnyTLSConfig{Address: "host", Port: 443, Password: "p"}
	_, ok, err := c.XrayOutbound()
	if err != nil {
		t.Fatalf("XrayOutbound err: %v", err)
	}
	if ok {
		t.Error("XrayOutbound ok = true, want false (xray has no AnyTLS)")
	}
}

func TestAnyTLSDialableBy(t *testing.T) {
	c := &AnyTLSConfig{Address: "host", Port: 443, Password: "p"}
	if !c.dialableBy(api.CoreSingBox) {
		t.Error("dialableBy(CoreSingBox) = false, want true")
	}
	if c.dialableBy(api.CoreXray) {
		t.Error("dialableBy(CoreXray) = true, want false")
	}
}

func TestAnyTLSProfileMetadata(t *testing.T) {
	c := &AnyTLSConfig{ServerName: "Tokyo", Address: "h", Port: 443, Password: "secret", SNI: "ex.com"}
	if c.Kind() != ProtocolAnyTLS {
		t.Errorf("Kind = %v, want %v", c.Kind(), ProtocolAnyTLS)
	}
	if c.DisplayName() != "Tokyo" {
		t.Errorf("DisplayName = %q, want Tokyo", c.DisplayName())
	}
	if c.ServerAddress() != "h" || c.ServerPort() != 443 {
		t.Errorf("server addr/port wrong: %q %d", c.ServerAddress(), c.ServerPort())
	}
	if c.Identity() != "secret" {
		t.Errorf("Identity = %q, want secret", c.Identity())
	}
	if c.TransportLabel() != "" {
		t.Errorf("TransportLabel = %q, want empty", c.TransportLabel())
	}
	if c.SecurityLabel() != "tls" {
		t.Errorf("SecurityLabel = %q, want tls", c.SecurityLabel())
	}
	sni, ok := c.CamouflageSNI()
	if !ok || sni != "ex.com" {
		t.Errorf("CamouflageSNI = (%q, %v), want (ex.com, true)", sni, ok)
	}
}

func TestAnyTLSProfileDocRoundTrip(t *testing.T) {
	orig := &AnyTLSConfig{
		ServerName: "RT",
		Address:    "host",
		Port:       8443,
		Password:   "pass",
		SNI:        "ex.com",
		Insecure:   true,
	}
	doc, err := EncodeProfileDoc(orig)
	if err != nil {
		t.Fatalf("EncodeProfileDoc: %v", err)
	}

	// The on-disk shape is the single-key tagged union {"anytls": <body>}.
	var union map[string]json.RawMessage
	if err := json.Unmarshal(doc, &union); err != nil {
		t.Fatalf("union unmarshal: %v", err)
	}
	body, ok := union["anytls"]
	if !ok {
		t.Fatalf("doc has no \"anytls\" key: %s", doc)
	}

	got, err := DecodeProfile(ProtocolAnyTLS, body)
	if err != nil {
		t.Fatalf("DecodeProfile: %v", err)
	}
	if !reflect.DeepEqual(got, orig) {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", got, orig)
	}
}
