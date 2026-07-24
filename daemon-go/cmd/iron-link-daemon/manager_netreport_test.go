package main

import (
	"testing"

	"ironlink/daemon/internal/engine"
)

func TestProviderCheck(t *testing.T) {
	cases := []struct {
		name       string
		r          engine.ProviderResult
		wantStatus string
	}{
		{"dns blocked", engine.ProviderResult{Name: "Hetzner", Host: "h", Err: "dial tcp: lookup h: no such host"}, "fail"},
		{"tcp blackholed", engine.ProviderResult{Name: "Vultr", Host: "v", Err: "i/o timeout"}, "fail"},
		{"tls interfered", engine.ProviderResult{Name: "GCP", Host: "g", TCPOK: true, Err: "record layer failure"}, "warn"},
		{"frozen", engine.ProviderResult{Name: "OVH", Host: "o", TCPOK: true, TLSOK: true, BytesRead: 16384, Frozen: true}, "warn"},
		{"healthy big", engine.ProviderResult{Name: "Linode", Host: "l", TCPOK: true, TLSOK: true, HTTPStatus: 200, BytesRead: 49152}, "ok"},
		{"healthy small", engine.ProviderResult{Name: "AWS", Host: "a", TCPOK: true, TLSOK: true, HTTPStatus: 200, BytesRead: 512}, "ok"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := providerCheck(c.r)
			if got.Status != c.wantStatus {
				t.Errorf("status=%q, want %q", got.Status, c.wantStatus)
			}
		})
	}
}

func TestDNSIntegrityCheck(t *testing.T) {
	if got := dnsIntegrityCheck(engine.DNSIntegrityReport{DoHWorks: false, DoHErr: "x"}); got.Status != "warn" {
		t.Errorf("DoH blocked: status=%q, want warn", got.Status)
	}
	if got := dnsIntegrityCheck(engine.DNSIntegrityReport{DoHWorks: true, ControlOK: true}); got.Status != "ok" {
		t.Errorf("clean: status=%q, want ok", got.Status)
	}
	if got := dnsIntegrityCheck(engine.DNSIntegrityReport{DoHWorks: true, Poisoned: []string{"www.facebook.com"}}); got.Status != "warn" {
		t.Errorf("poisoned: status=%q, want warn", got.Status)
	}
	if got := dnsIntegrityCheck(engine.DNSIntegrityReport{DoHWorks: true, UDPHijacked: true}); got.Status != "warn" {
		t.Errorf("udp hijack: status=%q, want warn", got.Status)
	}
	if got := dnsIntegrityCheck(engine.DNSIntegrityReport{DoHWorks: true, StubIPs: []string{"127.0.0.1"}}); got.Status != "warn" {
		t.Errorf("stub ip: status=%q, want warn", got.Status)
	}
}

func TestProtocolsCheck(t *testing.T) {
	allOK := protocolsCheck([]engine.ProtocolResult{
		{Name: "HTTPS (TCP 443)", OK: true}, {Name: "SSH (TCP 22)", OK: true},
	})
	if allOK.Status != "ok" {
		t.Errorf("all ok: status=%q, want ok", allOK.Status)
	}
	someBlocked := protocolsCheck([]engine.ProtocolResult{
		{Name: "HTTPS (TCP 443)", OK: true}, {Name: "DoT (TCP 853)", OK: false},
	})
	if someBlocked.Status != "warn" {
		t.Errorf("some blocked: status=%q, want warn", someBlocked.Status)
	}
}
