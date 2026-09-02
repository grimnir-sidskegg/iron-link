package proxy

import (
	"reflect"
	"testing"
)

// A full-featured reality+xhttp link: every recognized query parameter must
// land in the parsed config.
func TestParseVlessBasic(t *testing.T) {
	url := "vless://88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a@188.188.0.1:443?security=reality&type=xhttp&headerType=&path=&host=&mode=auto&sni=google.com&fp=chrome&pbk=hsNnIVYyMIFj0RfkH9y7pQckA2fasdfetrwfv&sid=cb423123gfds#Server%20Name"

	want := &VlessConfig{
		ServerName: "Server Name",
		UUID:       "88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a",
		Address:    "188.188.0.1",
		Port:       443,
		Encryption: "none",
		Flow:       "",
		Security: Security{Kind: SecurityReality, Reality: &RealityParams{
			SNI: "google.com",
			Fp:  "chrome",
			Pbk: "hsNnIVYyMIFj0RfkH9y7pQckA2fasdfetrwfv",
			Sid: "cb423123gfds",
		}},
		Transport: Transport{Kind: TransportXhttp, Xhttp: &XhttpParams{
			Path: "",
			Host: "",
			Mode: "auto",
		}},
	}

	p, err := ParseURL(url)
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	got, ok := p.(*VlessConfig)
	if !ok {
		t.Fatalf("ParseURL returned %T, want *VlessConfig", p)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsed config mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestParseVlessDefaults(t *testing.T) {
	// No port, no query, no fragment: port 443, encryption none, security
	// none, transport tcp, the default display name.
	p, err := ParseURL("vless://u@example.com")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*VlessConfig)
	if c.Port != 443 || c.Encryption != "none" || c.ServerName != "New Profile" {
		t.Errorf("defaults wrong: %+v", c)
	}
	if c.Security.Kind != SecurityNone || c.Transport.Kind != TransportTCP {
		t.Errorf("default security/transport wrong: %+v", c)
	}
}

func TestParseVlessPlusInFragmentDecodesToSpace(t *testing.T) {
	// The fragment is read as form pairs, so '+' is a space.
	p, err := ParseURL("vless://u@example.com:443#Server+Name")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	if got := p.DisplayName(); got != "Server Name" {
		t.Errorf("DisplayName = %q, want %q", got, "Server Name")
	}
}

func TestParseVlessMissingHostIsError(t *testing.T) {
	if _, err := ParseURL("vless://u@:443"); err == nil {
		t.Error("expected error for missing host")
	}
}

func TestParseVlessRealityRequiresAllParams(t *testing.T) {
	// sni present, fp/pbk/sid missing → error ("reality requires fp").
	if _, err := ParseURL("vless://u@h:443?security=reality&sni=google.com"); err == nil {
		t.Error("expected error for reality link missing fp/pbk/sid")
	}
}

func TestParseVlessUnsupportedSchemeIsError(t *testing.T) {
	// ssr:// (ShadowsocksR) is not a registered protocol.
	if _, err := ParseURL("ssr://u@h:443"); err == nil {
		t.Error("expected error for unsupported scheme")
	}
}

func TestParseVlessWsTransport(t *testing.T) {
	p, err := ParseURL("vless://u@h:8443?security=tls&sni=cdn.example.com&type=ws&path=%2Fws&host=cdn.example.com#WS")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*VlessConfig)
	if c.Security.Kind != SecurityTLS || c.Security.TLS.SNI != "cdn.example.com" {
		t.Errorf("tls params wrong: %+v", c.Security)
	}
	if c.Transport.Kind != TransportWs || c.Transport.Ws.Path != "/ws" || c.Transport.Ws.Host != "cdn.example.com" {
		t.Errorf("ws params wrong: %+v", c.Transport)
	}
}
