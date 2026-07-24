package proxy

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"ironlink/daemon/internal/api"
)

// vmessLinkURL builds a `vmess://base64(json)` share link from a field map.
func vmessLinkURL(t *testing.T, fields map[string]any) string {
	t.Helper()
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal vmess body: %v", err)
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(body)
}

func TestParseVmessBasic(t *testing.T) {
	// Numeric port/aid, tcp, no tls — the plainest node.
	link := vmessLinkURL(t, map[string]any{
		"v":    "2",
		"ps":   "Tokyo",
		"add":  "1.2.3.4",
		"port": 443,
		"id":   "27848739-7e62-4138-9fd3-098a63964b6b",
		"aid":  0,
		"scy":  "auto",
		"net":  "tcp",
		"tls":  "",
	})

	p, err := ParseURL(link)
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c, ok := p.(*VmessConfig)
	if !ok {
		t.Fatalf("ParseURL returned %T, want *VmessConfig", p)
	}
	if c.Address != "1.2.3.4" || c.Port != 443 {
		t.Errorf("add/port wrong: %s:%d", c.Address, c.Port)
	}
	if c.UUID != "27848739-7e62-4138-9fd3-098a63964b6b" {
		t.Errorf("id wrong: %s", c.UUID)
	}
	if c.AlterID != 0 {
		t.Errorf("aid wrong: %d", c.AlterID)
	}
	if c.Transport.Kind != TransportTCP {
		t.Errorf("net wrong: %s", c.Transport.Kind)
	}
	if c.Security.Kind != SecurityNone {
		t.Errorf("tls wrong: %s", c.Security.Kind)
	}
	if c.DisplayName() != "Tokyo" {
		t.Errorf("ps wrong: %s", c.DisplayName())
	}
	if c.Kind() != ProtocolVmess {
		t.Errorf("kind wrong: %s", c.Kind())
	}
}

func TestParseVmessStringPortAndAid(t *testing.T) {
	// v2rayN exporters that emit port/aid as STRINGS must parse too.
	link := vmessLinkURL(t, map[string]any{
		"add":  "example.com",
		"port": "8443",
		"id":   "uuid-x",
		"aid":  "64",
		"net":  "tcp",
	})
	p, err := ParseURL(link)
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*VmessConfig)
	if c.Port != 8443 {
		t.Errorf("string port wrong: %d", c.Port)
	}
	if c.AlterID != 64 {
		t.Errorf("string aid wrong: %d", c.AlterID)
	}
}

func TestParseVmessWsTLS(t *testing.T) {
	link := vmessLinkURL(t, map[string]any{
		"add":  "cdn.example.com",
		"port": 443,
		"id":   "u",
		"aid":  0,
		"net":  "ws",
		"host": "cdn.example.com",
		"path": "/ws",
		"tls":  "tls",
		"sni":  "cdn.example.com",
		"fp":   "chrome",
	})
	p, err := ParseURL(link)
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*VmessConfig)
	if c.Transport.Kind != TransportWs {
		t.Fatalf("transport kind = %s, want ws", c.Transport.Kind)
	}
	if c.Transport.Ws.Path != "/ws" || c.Transport.Ws.Host != "cdn.example.com" {
		t.Errorf("ws params wrong: %+v", c.Transport.Ws)
	}
	if c.Security.Kind != SecurityTLS {
		t.Fatalf("security kind = %s, want tls", c.Security.Kind)
	}
	if c.Security.TLS.SNI != "cdn.example.com" || c.Security.TLS.Fp != "chrome" {
		t.Errorf("tls params wrong: %+v", c.Security.TLS)
	}
	sni, ok := c.CamouflageSNI()
	if !ok || sni != "cdn.example.com" {
		t.Errorf("CamouflageSNI = %q,%v want cdn.example.com,true", sni, ok)
	}
}

func TestParseVmessGrpcServiceName(t *testing.T) {
	// For grpc the v2rayN `path` is the serviceName.
	link := vmessLinkURL(t, map[string]any{
		"add":  "h",
		"port": 2096,
		"id":   "u",
		"net":  "grpc",
		"path": "mysvc",
		"tls":  "tls",
	})
	p, err := ParseURL(link)
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*VmessConfig)
	if c.Transport.Kind != TransportGrpc || c.Transport.Grpc.ServiceName != "mysvc" {
		t.Errorf("grpc params wrong: %+v", c.Transport)
	}
}

func TestParseVmessRawBase64NoPadding(t *testing.T) {
	// Raw-std base64 (no '=' padding) must decode too.
	body, _ := json.Marshal(map[string]any{
		"add": "h", "port": 443, "id": "u", "net": "tcp",
	})
	link := "vmess://" + base64.RawStdEncoding.EncodeToString(body)
	if _, err := ParseURL(link); err != nil {
		t.Fatalf("ParseURL raw base64: %v", err)
	}
}

func TestParseVmessDefaults(t *testing.T) {
	// No port/aid/scy/net/tls → port 443, cipher auto, tcp, none.
	link := vmessLinkURL(t, map[string]any{"add": "h", "id": "u"})
	p, err := ParseURL(link)
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*VmessConfig)
	if c.Port != 443 || c.cipher() != "auto" || c.ServerName != "New Profile" {
		t.Errorf("defaults wrong: %+v", c)
	}
	if c.Transport.Kind != TransportTCP || c.Security.Kind != SecurityNone {
		t.Errorf("default transport/security wrong: %+v", c)
	}
}

func TestParseVmessMissingHostIsError(t *testing.T) {
	link := vmessLinkURL(t, map[string]any{"id": "u", "port": 443})
	if _, err := ParseURL(link); err == nil {
		t.Error("expected error for missing add")
	}
}

func TestParseVmessBadBase64IsError(t *testing.T) {
	if _, err := ParseURL("vmess://!!!not-base64!!!"); err == nil {
		t.Error("expected error for bad base64")
	}
}

func TestVmessSingBoxOutbound(t *testing.T) {
	link := vmessLinkURL(t, map[string]any{
		"add": "1.2.3.4", "port": 443, "id": "the-uuid", "aid": 4,
		"scy": "aes-128-gcm", "net": "ws", "host": "h", "path": "/p", "tls": "tls", "sni": "h",
	})
	p, _ := ParseURL(link)
	ob, err := p.SingBoxOutbound("vmess-out")
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if ob["type"] != "vmess" {
		t.Errorf("type = %v, want vmess", ob["type"])
	}
	if ob["uuid"] != "the-uuid" {
		t.Errorf("uuid = %v", ob["uuid"])
	}
	if ob["alter_id"] != 4 {
		t.Errorf("alter_id = %v, want 4", ob["alter_id"])
	}
	if ob["security"] != "aes-128-gcm" {
		t.Errorf("security = %v, want aes-128-gcm", ob["security"])
	}
	tr, ok := ob["transport"].(map[string]any)
	if !ok || tr["type"] != "ws" {
		t.Errorf("transport block wrong: %v", ob["transport"])
	}
	if _, ok := ob["tls"]; !ok {
		t.Error("expected tls block")
	}
}

func TestVmessXrayOutbound(t *testing.T) {
	link := vmessLinkURL(t, map[string]any{
		"add": "1.2.3.4", "port": 443, "id": "the-uuid", "aid": 0, "scy": "auto", "net": "tcp",
	})
	p, _ := ParseURL(link)
	ob, ok, err := p.XrayOutbound()
	if err != nil {
		t.Fatalf("XrayOutbound: %v", err)
	}
	if !ok {
		t.Fatal("XrayOutbound ok = false, want true")
	}
	if ob["protocol"] != "vmess" {
		t.Errorf("protocol = %v, want vmess", ob["protocol"])
	}
	if _, ok := ob["streamSettings"]; !ok {
		t.Error("missing streamSettings")
	}
	settings := ob["settings"].(map[string]any)
	vnext := settings["vnext"].([]any)
	first := vnext[0].(map[string]any)
	if first["address"] != "1.2.3.4" {
		t.Errorf("vnext address = %v", first["address"])
	}
	users := first["users"].([]any)
	user := users[0].(map[string]any)
	if user["id"] != "the-uuid" {
		t.Errorf("user id = %v, want the-uuid", user["id"])
	}
	if user["alterId"] != 0 {
		t.Errorf("user alterId = %v, want 0", user["alterId"])
	}
	if user["security"] != "auto" {
		t.Errorf("user security = %v, want auto", user["security"])
	}
}

func TestVmessDialableByBothCores(t *testing.T) {
	// tcp/tls is dialable by both cores.
	link := vmessLinkURL(t, map[string]any{
		"add": "h", "port": 443, "id": "u", "net": "tcp", "tls": "tls",
	})
	p, _ := ParseURL(link)
	if !p.dialableBy(api.CoreSingBox) {
		t.Error("tcp/tls should be dialable by sing-box")
	}
	if !p.dialableBy(api.CoreXray) {
		t.Error("tcp/tls should be dialable by xray")
	}
}

func TestVmessProfileDocRoundTrip(t *testing.T) {
	link := vmessLinkURL(t, map[string]any{
		"add": "1.2.3.4", "port": 8443, "id": "the-uuid", "aid": 16,
		"scy": "chacha20-poly1305", "net": "ws", "host": "h", "path": "/p", "tls": "tls", "sni": "h", "fp": "chrome",
	})
	p, err := ParseURL(link)
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}

	doc, err := EncodeProfileDoc(p)
	if err != nil {
		t.Fatalf("EncodeProfileDoc: %v", err)
	}

	// The doc must be the single-key {"vmess": …} tagged union.
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(doc, &keyed); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	body, ok := keyed["vmess"]
	if !ok {
		t.Fatalf("doc missing \"vmess\" key: %s", doc)
	}

	got, err := DecodeProfile(ProtocolVmess, body)
	if err != nil {
		t.Fatalf("DecodeProfile: %v", err)
	}
	g := got.(*VmessConfig)
	o := p.(*VmessConfig)
	if g.UUID != o.UUID || g.Address != o.Address || g.Port != o.Port || g.AlterID != o.AlterID || g.Cipher != o.Cipher {
		t.Errorf("round-trip scalar mismatch:\n got %+v\nwant %+v", g, o)
	}
	if g.Transport.Kind != o.Transport.Kind || g.Transport.Ws.Path != o.Transport.Ws.Path {
		t.Errorf("round-trip transport mismatch: %+v vs %+v", g.Transport, o.Transport)
	}
	if g.Security.Kind != o.Security.Kind || g.Security.TLS.SNI != o.Security.TLS.SNI {
		t.Errorf("round-trip security mismatch: %+v vs %+v", g.Security, o.Security)
	}
}
