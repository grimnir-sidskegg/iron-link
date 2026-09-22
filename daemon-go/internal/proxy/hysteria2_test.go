package proxy

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"ironlink/daemon/internal/api"
)

// hy2TestPin is a synthetic certificate pin in the colon-hex form panels
// emit: 32 bytes, so the value that parses is the value xray accepts.
const hy2TestPin = "0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A"

// A hysteria2:// share link with auth, sni, salamander obfs, insecure and a
// certificate pin parses field-by-field into the expected config; the pin is
// stored verbatim and the panel fm= / the ecosystem mport are ignored.
func TestParseHysteria2Full(t *testing.T) {
	const link = "hysteria2://pass@host:443?sni=ex.com&obfs=salamander&obfs-password=xyz&insecure=1&pinSHA256=" +
		hy2TestPin + "&fm=%7B%22udp%22%3A%5B%5D%7D&mport=5000-6000#x"

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
		PinSHA256:    hy2TestPin,
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

// Defaults: no port → 443, no fragment → "New Hysteria2", no obfs / pin → empty.
func TestParseHysteria2Defaults(t *testing.T) {
	p, err := ParseURL("hysteria2://pw@example.com")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*Hysteria2Config)
	if c.Port != 443 || c.ServerName != "New Hysteria2" {
		t.Errorf("defaults wrong: %+v", c)
	}
	if c.Obfs != "" || c.Insecure || c.PinSHA256 != "" {
		t.Errorf("expected no obfs / not insecure / no pin: %+v", c)
	}
}

// A user:pass userinfo is the whole auth string, joined back with the ':'.
func TestParseHysteria2UserPass(t *testing.T) {
	p, err := ParseURL("hysteria2://user:pass@example.com")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	if c := p.(*Hysteria2Config); c.Password != "user:pass" {
		t.Errorf("Password = %q, want user:pass", c.Password)
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

// A pinned node (Insecure false) is dialed insecure by sing-box: it has no
// certificate-hash pin, so the pin maps to tls.insecure.
func TestHysteria2SingBoxOutboundPinned(t *testing.T) {
	c := &Hysteria2Config{Address: "host", Port: 443, Password: "pass", PinSHA256: hy2TestPin}
	ob, err := c.SingBoxOutbound("proxy")
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if tls := ob["tls"].(map[string]any); tls["insecure"] != true {
		t.Errorf("pinned node must dial insecure on sing-box: %+v", tls)
	}
}

// XrayOutbound: the native hysteria (v2) outbound for a pinned salamander node,
// the public-CA shape (serverName falls back to the address, no pin, no mask),
// and ok=false for a node that is insecure without a pin.
func TestHysteria2XrayOutbound(t *testing.T) {
	c := &Hysteria2Config{
		Address: "host", Port: 443, Password: "pass", SNI: "ex.com", Insecure: true,
		Obfs: "salamander", ObfsPassword: "xyz", PinSHA256: hy2TestPin,
	}
	ob, ok, err := c.XrayOutbound()
	if err != nil {
		t.Fatalf("XrayOutbound: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true for a pinned node")
	}
	if ob["protocol"] != "hysteria" {
		t.Errorf("protocol = %v, want hysteria", ob["protocol"])
	}
	settings := ob["settings"].(map[string]any)
	if settings["address"] != "host" || settings["port"] != uint16(443) || settings["version"] != 2 {
		t.Errorf("settings wrong: %+v", settings)
	}
	stream := ob["streamSettings"].(map[string]any)
	if stream["network"] != "hysteria" || stream["security"] != "tls" {
		t.Errorf("streamSettings wrong: %+v", stream)
	}
	tls := stream["tlsSettings"].(map[string]any)
	if tls["serverName"] != "ex.com" || tls["pinnedPeerCertSha256"] != hy2TestPin {
		t.Errorf("tlsSettings wrong: %+v", tls)
	}
	if _, has := tls["allowInsecure"]; has {
		t.Errorf("allowInsecure must never be emitted (xray rejects it): %+v", tls)
	}
	hy := stream["hysteriaSettings"].(map[string]any)
	if hy["version"] != 2 || hy["auth"] != "pass" {
		t.Errorf("hysteriaSettings wrong: %+v", hy)
	}
	mask := stream["finalmask"].(map[string]any)["udp"].([]any)[0].(map[string]any)
	if mask["type"] != "salamander" || mask["settings"].(map[string]any)["password"] != "xyz" {
		t.Errorf("finalmask udp mask wrong: %+v", mask)
	}

	// Public CA, no SNI: serverName is the address; no pin, no mask.
	c = &Hysteria2Config{Address: "203.0.113.10", Port: 443, Password: "pass"}
	ob, ok, err = c.XrayOutbound()
	if err != nil || !ok {
		t.Fatalf("XrayOutbound(public) = ok %v, err %v; want ok", ok, err)
	}
	stream = ob["streamSettings"].(map[string]any)
	tls = stream["tlsSettings"].(map[string]any)
	if tls["serverName"] != "203.0.113.10" {
		t.Errorf("serverName = %v, want the address", tls["serverName"])
	}
	if _, has := tls["pinnedPeerCertSha256"]; has {
		t.Errorf("no pin must emit no pinnedPeerCertSha256: %+v", tls)
	}
	if _, has := stream["finalmask"]; has {
		t.Errorf("no obfs must emit no finalmask: %+v", stream)
	}

	// Insecure without a pin: xray cannot dial it.
	c = &Hysteria2Config{Address: "host", Port: 443, Password: "pass", Insecure: true}
	ob, ok, err = c.XrayOutbound()
	if ok || ob != nil || err != nil {
		t.Errorf("XrayOutbound(insecure, no pin) = %v, %v, %v; want nil, false, nil", ob, ok, err)
	}
}

// dialableBy: sing-box always; xray unless insecure without a pin. The
// selection contract on a pinned node: both cores eligible, sing-box by
// default, xray by override; the override is rejected on the insecure node.
// A pre-pin store body decodes with no pin and keeps today's semantics.
func TestHysteria2DialableBy(t *testing.T) {
	public := &Hysteria2Config{Address: "host", Port: 443, Password: "pass"}
	pinned := &Hysteria2Config{Address: "host", Port: 443, Password: "pass", Insecure: true, PinSHA256: hy2TestPin}
	insecure := &Hysteria2Config{Address: "host", Port: 443, Password: "pass", Insecure: true}
	for name, c := range map[string]*Hysteria2Config{"public": public, "pinned": pinned, "insecure": insecure} {
		if !c.dialableBy(api.CoreSingBox) {
			t.Errorf("%s: sing-box must dial it", name)
		}
	}
	if !public.dialableBy(api.CoreXray) || !pinned.dialableBy(api.CoreXray) {
		t.Error("xray must dial a public-CA or pinned node")
	}
	if insecure.dialableBy(api.CoreXray) {
		t.Error("xray must NOT dial an insecure node without a pin")
	}

	if got := EligibleCores(pinned); !slices.Contains(got, api.CoreSingBox) || !slices.Contains(got, api.CoreXray) {
		t.Errorf("EligibleCores(pinned) = %v, want both cores", got)
	}
	if core, err := SelectCore(pinned, api.CoreSingBox, nil); err != nil || core != api.CoreSingBox {
		t.Errorf("SelectCore(pinned, default) = %s, %v; want SingBox", core, err)
	}
	if core, err := SelectCore(pinned, api.CoreSingBox, corePtr(api.CoreXray)); err != nil || core != api.CoreXray {
		t.Errorf("SelectCore(pinned, override xray) = %s, %v; want Xray", core, err)
	}
	if _, err := SelectCore(insecure, api.CoreSingBox, corePtr(api.CoreXray)); err == nil {
		t.Error("SelectCore(insecure, override xray) must error")
	}

	p, err := DecodeProfile(ProtocolHysteria2, json.RawMessage(`{"address":"h","port":443,"password":"p","insecure":true}`))
	if err != nil {
		t.Fatalf("DecodeProfile: %v", err)
	}
	if c := p.(*Hysteria2Config); c.PinSHA256 != "" || c.dialableBy(api.CoreXray) {
		t.Errorf("pre-pin store body must decode pin-less and sing-box-only: %+v", c)
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
		PinSHA256:    hy2TestPin,
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
