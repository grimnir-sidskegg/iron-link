package engine

import (
	"net"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// TestDNSQueryRoundTrip exercises the wire-format helpers offline: a query packs,
// and a hand-built answer parses back to the A records (the DoT/UDP query path
// depends on both, but needs no network to verify).
func TestDNSQueryRoundTrip(t *testing.T) {
	if _, err := buildDNSQuery("speed.cloudflare.com"); err != nil {
		t.Fatalf("buildDNSQuery: %v", err)
	}
	// Build a synthetic response carrying two A records and parse it.
	name := dnsmessage.MustNewName("speed.cloudflare.com.")
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{Response: true},
		Questions: []dnsmessage.Question{
			{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
		},
		Answers: []dnsmessage.Resource{
			{
				Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
				Body:   &dnsmessage.AResource{A: [4]byte{1, 2, 3, 4}},
			},
			{
				Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
				Body:   &dnsmessage.AResource{A: [4]byte{5, 6, 7, 8}},
			},
		},
	}
	packed, err := msg.Pack()
	if err != nil {
		t.Fatalf("pack answer: %v", err)
	}
	ips, err := parseDNSAnswer(packed)
	if err != nil {
		t.Fatalf("parseDNSAnswer: %v", err)
	}
	if len(ips) != 2 || !ips[0].Equal(net.IPv4(1, 2, 3, 4)) || !ips[1].Equal(net.IPv4(5, 6, 7, 8)) {
		t.Errorf("parsed IPs = %v, want [1.2.3.4 5.6.7.8]", ips)
	}
}

// TestWorstAnomaly pins the bottleneck attribution: the slowest anomalous
// latency stage wins; throughput is the fallback when nothing else is anomalous.
func TestWorstAnomaly(t *testing.T) {
	cases := []struct {
		name   string
		stages []QualityStageResult
		want   QualityStage
	}{
		{
			name: "no anomaly → none",
			stages: []QualityStageResult{
				{Stage: QStageDNS, DurationMs: 40, OK: true},
				{Stage: QStageTTFB, DurationMs: 120, OK: true},
			},
			want: "",
		},
		{
			name: "slowest anomalous latency stage wins",
			stages: []QualityStageResult{
				{Stage: QStageDNS, DurationMs: 1000, OK: true, Anomalous: true},
				{Stage: QStageTTFB, DurationMs: 3000, OK: true, Anomalous: true},
			},
			want: QStageTTFB,
		},
		{
			name: "throughput is the fallback bottleneck",
			stages: []QualityStageResult{
				{Stage: QStageDNS, DurationMs: 40, OK: true},
				{Stage: QStageThroughput, DurationMs: 3000, OK: true, Anomalous: true},
			},
			want: QStageThroughput,
		},
	}
	for _, c := range cases {
		if got := worstAnomaly(c.stages); got != c.want {
			t.Errorf("%s: worstAnomaly = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestDNSUpstreamLabel pins the human labels used in the stage detail line.
func TestDNSUpstreamLabel(t *testing.T) {
	cases := map[string]DNSUpstream{
		"DoT 8.8.8.8":       {Type: "tls", Address: "8.8.8.8"},
		"DoH 1.1.1.1":       {Type: "https", Address: "1.1.1.1"},
		"plain DNS 9.9.9.9": {Type: "udp", Address: "9.9.9.9"},
	}
	for want, up := range cases {
		if got := dnsUpstreamLabel(up); got != want {
			t.Errorf("dnsUpstreamLabel(%+v) = %q, want %q", up, got, want)
		}
	}
}
