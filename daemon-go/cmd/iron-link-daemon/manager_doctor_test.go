package main

import (
	"strings"
	"testing"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/engine"
)

// TestInternetCheck pins the verdict + dns.strategy recommendation the doctor
// derives from a connectivity probe — the pure mapping, independent of the
// network probe itself.
func TestInternetCheck(t *testing.T) {
	cases := []struct {
		name       string
		r          engine.InternetReport
		ipVersion  string
		strategy   string
		wantStatus string
		wantRemedy string // expected remedy.DNSStrategy ("" = no remedy)
	}{
		{
			name:       "dual stack, default strategy → ok",
			r:          engine.InternetReport{V4: true, V6: true},
			ipVersion:  "both",
			strategy:   "prefer_ipv4",
			wantStatus: "ok",
		},
		{
			name:       "v4 only, default strategy → ok",
			r:          engine.InternetReport{V4: true},
			ipVersion:  "both",
			strategy:   "prefer_ipv4",
			wantStatus: "ok",
		},
		{
			// The reported dual-stack break: v6-only network, v4-leaning DNS.
			name:       "v6 only, prefer_ipv4 → warn + prefer_ipv6",
			r:          engine.InternetReport{V6: true},
			ipVersion:  "both",
			strategy:   "prefer_ipv4",
			wantStatus: "warn",
			wantRemedy: "prefer_ipv6",
		},
		{
			name:       "v6 only, ipv4_only → warn + prefer_ipv6",
			r:          engine.InternetReport{V6: true},
			ipVersion:  "both",
			strategy:   "ipv4_only",
			wantStatus: "warn",
			wantRemedy: "prefer_ipv6",
		},
		{
			name:       "v6 only, already prefer_ipv6 → ok",
			r:          engine.InternetReport{V6: true},
			ipVersion:  "both",
			strategy:   "prefer_ipv6",
			wantStatus: "ok",
		},
		{
			name:       "v4 only, ipv6_only → warn + prefer_ipv4",
			r:          engine.InternetReport{V4: true},
			ipVersion:  "both",
			strategy:   "ipv6_only",
			wantStatus: "warn",
			wantRemedy: "prefer_ipv4",
		},
		{
			name:       "no internet → fail, no remedy",
			r:          engine.InternetReport{},
			ipVersion:  "both",
			strategy:   "prefer_ipv4",
			wantStatus: "fail",
		},
		{
			// Strategy is consistent, but the TUN is pinned to a dead family.
			name:       "v6 only, ip_version=v4, prefer_ipv6 → warn (tun mismatch)",
			r:          engine.InternetReport{V6: true},
			ipVersion:  "v4",
			strategy:   "prefer_ipv6",
			wantStatus: "warn",
		},
		{
			name:       "empty strategy normalizes to prefer_ipv4 → v6 only warns",
			r:          engine.InternetReport{V6: true},
			ipVersion:  "both",
			strategy:   "",
			wantStatus: "warn",
			wantRemedy: "prefer_ipv6",
		},
		{
			// The Task 4b scenario: IPv6 is DELIBERATELY off (ipv4_only DNS), and
			// the network is v4-only. A missing IPv6 is expected, NOT a warning.
			name:       "v4 only, ipv4_only → ok (missing v6 is intended)",
			r:          engine.InternetReport{V4: true},
			ipVersion:  "both",
			strategy:   "ipv4_only",
			wantStatus: "ok",
		},
		{
			// Same principle via the TUN pin: ip_version=v4 disables IPv6, so its
			// being unreachable must not warn.
			name:       "v4 only, ip_version=v4 → ok (missing v6 is intended)",
			r:          engine.InternetReport{V4: true},
			ipVersion:  "v4",
			strategy:   "prefer_ipv4",
			wantStatus: "ok",
		},
		{
			// Mirror image: IPv4 deliberately off (ipv6_only) on a v6-only network.
			name:       "v6 only, ipv6_only → ok (missing v4 is intended)",
			r:          engine.InternetReport{V6: true},
			ipVersion:  "both",
			strategy:   "ipv6_only",
			wantStatus: "ok",
		},
		{
			// The reported case: dual-stack host, ip_version=v4, default strategy.
			// No contradiction (prefer_ipv4 leans v4), but ip_version=v4 gives up
			// the network's working IPv6 end-to-end (IPv6-only sites die) — a silent
			// capability loss, so it warns (no one-click remedy: fix is ip_version).
			name:       "dual stack, ip_version=v4, prefer_ipv4 → warn (working v6 given up)",
			r:          engine.InternetReport{V4: true, V6: true},
			ipVersion:  "v4",
			strategy:   "prefer_ipv4",
			wantStatus: "warn",
		},
		{
			// The prefer_v6 + ip_version=v4 CONTRADICTION on a dual-stack host: the
			// strategy asks for IPv6 the v4-only TUN forbids. Warn + a remedy that
			// aligns the stored setting to the tunnel (its own v4 family works).
			name:       "dual stack, ip_version=v4, prefer_ipv6 → warn + ipv4_only (contradiction)",
			r:          engine.InternetReport{V4: true, V6: true},
			ipVersion:  "v4",
			strategy:   "prefer_ipv6",
			wantStatus: "warn",
			wantRemedy: "ipv4_only",
		},
		{
			// Mirror contradiction: ip_version=v6 + a v4-leaning strategy.
			name:       "dual stack, ip_version=v6, prefer_ipv4 → warn + ipv6_only (contradiction)",
			r:          engine.InternetReport{V4: true, V6: true},
			ipVersion:  "v6",
			strategy:   "prefer_ipv4",
			wantStatus: "warn",
			wantRemedy: "ipv6_only",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := internetCheck(c.r, c.ipVersion, c.strategy)
			if got.ID != "internet" {
				t.Errorf("ID = %q, want internet", got.ID)
			}
			if got.Status != c.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, c.wantStatus)
			}
			gotRemedy := ""
			if got.Remedy != nil {
				gotRemedy = got.Remedy.DNSStrategy
			}
			if gotRemedy != c.wantRemedy {
				t.Errorf("remedy DNSStrategy = %q, want %q", gotRemedy, c.wantRemedy)
			}
			if len(got.Details) < 3 {
				t.Errorf("expected at least the 3 family/resolver detail lines, got %d", len(got.Details))
			}
		})
	}
}

// TestInternetCheckAnnotatesDisabledFamily locks the config-aware detail line:
// a family the user deliberately disabled reads "expected (disabled by config)"
// when unreachable, so the row is never confusing (ok verdict, but a bare
// "unreachable" line would look like a fault).
func TestInternetCheckAnnotatesDisabledFamily(t *testing.T) {
	// ipv4_only + v4-only network: the IPv6 detail must be annotated as expected.
	got := internetCheck(engine.InternetReport{V4: true, V4Detail: "", V6Detail: "network is unreachable"},
		"both", "ipv4_only")
	if got.Status != "ok" {
		t.Fatalf("status = %q, want ok", got.Status)
	}
	var v6line string
	for _, d := range got.Details {
		if strings.HasPrefix(d, "IPv6:") {
			v6line = d
		}
	}
	if !strings.Contains(v6line, "expected") {
		t.Errorf("IPv6 detail = %q, want it annotated as expected/disabled", v6line)
	}
	// A family that is NOT disabled must keep the raw failure detail (no spurious
	// "expected" that would hide a real problem) — the fixture's v6-only case.
	got = internetCheck(engine.InternetReport{V6: true, V4Detail: "network is unreachable"},
		"both", "prefer_ipv4")
	for _, d := range got.Details {
		if strings.HasPrefix(d, "IPv4:") && strings.Contains(d, "expected") {
			t.Errorf("IPv4 detail = %q wrongly annotated expected under prefer_ipv4", d)
		}
	}
}

// TestInternetCheckIPVersionPinDetails locks the wording the doctor emits for a
// single-family ip_version on a dual-stack host: an informational "IPv6 is
// disabled" line when the strategy is consistent, and a warn line naming the
// contradiction when the stored strategy asks for the forbidden family.
func TestInternetCheckIPVersionPinDetails(t *testing.T) {
	dual := engine.InternetReport{V4: true, V6: true}

	// Consistent (prefer_ipv4): warns — the working IPv6 is given up — with no
	// one-click remedy (the fix is ip_version=both, not a DNS lever).
	got := internetCheck(dual, "v4", "prefer_ipv4")
	if got.Status != "warn" {
		t.Fatalf("consistent v4 pin on a dual-stack host: status = %q, want warn", got.Status)
	}
	if got.Remedy != nil {
		t.Errorf("consistent v4 pin: want no DNS remedy (fix is ip_version), got %+v", got.Remedy)
	}
	if !hasDetailContaining(got.Details, "IPv6") || !hasDetailContaining(got.Details, "ip_version=both") {
		t.Errorf("consistent v4 pin: want an 'IPv6 disabled / set ip_version=both' detail, got %v", got.Details)
	}

	// Contradiction (prefer_ipv6): warn, names the pinned-to ipv4_only, remedy set.
	got = internetCheck(dual, "v4", "prefer_ipv6")
	if got.Status != "warn" {
		t.Fatalf("contradiction: status = %q, want warn", got.Status)
	}
	if got.Remedy == nil || got.Remedy.DNSStrategy != "ipv4_only" {
		t.Errorf("contradiction: want remedy dns.strategy=ipv4_only, got %+v", got.Remedy)
	}
	if !hasDetailContaining(got.Details, "forbids") || !hasDetailContaining(got.Details, "ipv4_only") {
		t.Errorf("contradiction: want a detail explaining the pin to ipv4_only, got %v", got.Details)
	}
}

func hasDetailContaining(details []string, sub string) bool {
	for _, d := range details {
		if strings.Contains(d, sub) {
			return true
		}
	}
	return false
}

// TestFamilyDisabled pins which config combinations turn a family off.
func TestFamilyDisabled(t *testing.T) {
	cases := []struct {
		family, ipVersion, strategy string
		want                        bool
	}{
		{"v6", "both", "ipv4_only", true},
		{"v6", "v4", "prefer_ipv4", true},
		{"v6", "both", "prefer_ipv4", false},
		{"v4", "both", "ipv6_only", true},
		{"v4", "v6", "prefer_ipv6", true},
		{"v4", "both", "prefer_ipv4", false},
	}
	for _, c := range cases {
		if got := familyDisabled(c.family, c.ipVersion, c.strategy); got != c.want {
			t.Errorf("familyDisabled(%q,%q,%q) = %v, want %v",
				c.family, c.ipVersion, c.strategy, got, c.want)
		}
	}
}

// TestConnectionQualityCheck pins the decomposition verdict + attribution the
// doctor derives from a staged full-cycle probe (Task 5) — the pure mapping,
// independent of the network probe itself.
func TestConnectionQualityCheck(t *testing.T) {
	okStages := []engine.QualityStageResult{
		{Stage: engine.QStageDNS, DurationMs: 40, OK: true, Detail: "DoT 8.8.8.8"},
		{Stage: engine.QStageTCP, DurationMs: 30, OK: true},
		{Stage: engine.QStageTLS, DurationMs: 60, OK: true},
		{Stage: engine.QStageTTFB, DurationMs: 120, OK: true, Detail: "HTTP 200"},
		{Stage: engine.QStageThroughput, DurationMs: 3000, OK: true, Detail: "800 KB/s (2400 KB sampled)"},
	}

	t.Run("healthy → ok", func(t *testing.T) {
		got := connectionQualityCheck(engine.QualityReport{Target: "speed.example", Stages: okStages, OK: true})
		if got.ID != "connection_quality" || got.Status != "ok" {
			t.Fatalf("got id=%q status=%q, want connection_quality/ok", got.ID, got.Status)
		}
		if len(got.Details) != 5 {
			t.Errorf("want 5 stage detail lines, got %d", len(got.Details))
		}
	})

	t.Run("throttled DoT → warn, DNS bottleneck", func(t *testing.T) {
		stages := append([]engine.QualityStageResult(nil), okStages...)
		stages[0] = engine.QualityStageResult{Stage: engine.QStageDNS, DurationMs: 3200, OK: true, Anomalous: true, Detail: "DoT 8.8.8.8"}
		got := connectionQualityCheck(engine.QualityReport{Stages: stages, OK: true, Bottleneck: engine.QStageDNS})
		if got.Status != "warn" {
			t.Fatalf("status = %q, want warn", got.Status)
		}
		if !strings.Contains(got.Summary, "DNS") || !strings.Contains(got.Summary, "3200 ms") {
			t.Errorf("summary = %q, want it to name DNS as the 3200 ms bottleneck", got.Summary)
		}
	})

	t.Run("dns failed → fail", func(t *testing.T) {
		got := connectionQualityCheck(engine.QualityReport{
			Stages:      []engine.QualityStageResult{{Stage: engine.QStageDNS, DurationMs: 4000, OK: false, Detail: "i/o timeout"}},
			FailedStage: engine.QStageDNS,
		})
		if got.Status != "fail" {
			t.Fatalf("status = %q, want fail", got.Status)
		}
		if !strings.Contains(got.Summary, "DNS resolution failed") {
			t.Errorf("summary = %q, want DNS failure text", got.Summary)
		}
		if !strings.Contains(got.Details[0], "failed") {
			t.Errorf("detail = %q, want it to mark the failed stage", got.Details[0])
		}
	})

	t.Run("low throughput → warn", func(t *testing.T) {
		stages := append([]engine.QualityStageResult(nil), okStages...)
		stages[4] = engine.QualityStageResult{Stage: engine.QStageThroughput, DurationMs: 3000, OK: true, Anomalous: true, Detail: "40 KB/s (120 KB sampled)"}
		got := connectionQualityCheck(engine.QualityReport{Stages: stages, OK: true, Bottleneck: engine.QStageThroughput})
		if got.Status != "warn" || !strings.Contains(got.Summary, "Throughput is low") {
			t.Errorf("got status=%q summary=%q, want warn + low-throughput text", got.Status, got.Summary)
		}
	})
}

// TestPrimaryUpstream pins the resolver the quality DNS stage times.
func TestPrimaryUpstream(t *testing.T) {
	s := api.Settings{DNS: api.DNSSettings{Servers: []api.DNSServer{{Type: "https", Address: "1.1.1.1"}}}}
	if up := primaryUpstream(s); up.Type != "https" || up.Address != "1.1.1.1" {
		t.Errorf("primaryUpstream = %+v, want DoH 1.1.1.1", up)
	}
	if up := primaryUpstream(api.Settings{}); up.Type != "tls" || up.Address != "8.8.8.8" {
		t.Errorf("primaryUpstream(empty) = %+v, want DoT 8.8.8.8 fallback", up)
	}
}

// probe is a tiny constructor for a synthetic upstream verdict.
func probe(typ, addr string, reachable bool) engine.DNSUpstreamProbe {
	return engine.DNSUpstreamProbe{Type: typ, Address: addr, Reachable: reachable}
}

// TestResolverCheckResult pins the resolver-blocked verdict + remedy selection
// (the pure builder, independent of the network probes that feed it).
func TestResolverCheckResult(t *testing.T) {
	srv := func(addr string) api.DNSServer { return api.DNSServer{Type: "tls", Address: addr} }

	t.Run("primary reachable → ok, no remedy", func(t *testing.T) {
		c := resolverCheckResult(
			[]api.DNSServer{srv("8.8.8.8")},
			[]engine.DNSUpstreamProbe{probe("tls", "8.8.8.8", true)},
			false, nil)
		if c.Status != "ok" || c.Remedy != nil {
			t.Fatalf("got status=%q remedy=%v, want ok/nil", c.Status, c.Remedy)
		}
	})

	t.Run("via_tunnel on → ok regardless of reachability", func(t *testing.T) {
		c := resolverCheckResult(
			[]api.DNSServer{srv("8.8.8.8")},
			[]engine.DNSUpstreamProbe{probe("tls", "8.8.8.8", false)},
			true, nil)
		if c.Status != "ok" || c.Remedy != nil {
			t.Fatalf("got status=%q remedy=%v, want ok/nil", c.Status, c.Remedy)
		}
	})

	t.Run("primary blocked, reachable secondary → promote it", func(t *testing.T) {
		c := resolverCheckResult(
			[]api.DNSServer{srv("8.8.8.8"), srv("1.1.1.1")},
			[]engine.DNSUpstreamProbe{probe("tls", "8.8.8.8", false), probe("tls", "1.1.1.1", true)},
			false, nil)
		if c.Status != "warn" || c.Remedy == nil {
			t.Fatalf("got status=%q remedy=%v, want warn + remedy", c.Status, c.Remedy)
		}
		want := []api.DNSServer{srv("1.1.1.1"), srv("8.8.8.8")}
		if !equalServers(c.Remedy.DNSServers, want) {
			t.Errorf("promoted list = %v, want %v", c.Remedy.DNSServers, want)
		}
	})

	t.Run("primary blocked, candidate found → prepend candidate", func(t *testing.T) {
		c := resolverCheckResult(
			[]api.DNSServer{srv("8.8.8.8")},
			[]engine.DNSUpstreamProbe{probe("tls", "8.8.8.8", false)},
			false, &engine.DNSUpstream{Type: "tls", Address: "9.9.9.9"})
		if c.Status != "warn" || c.Remedy == nil {
			t.Fatalf("got status=%q remedy=%v, want warn + remedy", c.Status, c.Remedy)
		}
		want := []api.DNSServer{srv("9.9.9.9"), srv("8.8.8.8")}
		if !equalServers(c.Remedy.DNSServers, want) {
			t.Errorf("candidate list = %v, want %v", c.Remedy.DNSServers, want)
		}
	})

	t.Run("primary blocked, nothing direct → route via tunnel", func(t *testing.T) {
		c := resolverCheckResult(
			[]api.DNSServer{srv("8.8.8.8")},
			[]engine.DNSUpstreamProbe{probe("tls", "8.8.8.8", false)},
			false, nil)
		if c.Status != "warn" || c.Remedy == nil {
			t.Fatalf("got status=%q remedy=%v, want warn + remedy", c.Status, c.Remedy)
		}
		if c.Remedy.DNSViaTunnel == nil || !*c.Remedy.DNSViaTunnel {
			t.Errorf("expected dns_via_tunnel=true remedy, got %+v", c.Remedy)
		}
		if c.Remedy.DNSServers != nil {
			t.Errorf("via-tunnel remedy should not carry servers, got %v", c.Remedy.DNSServers)
		}
	})

	t.Run("no upstream configured → warn + default", func(t *testing.T) {
		c := resolverCheckResult(nil, nil, false, nil)
		if c.Status != "warn" || c.Remedy == nil || len(c.Remedy.DNSServers) != 1 {
			t.Fatalf("got status=%q remedy=%v, want warn + a default server", c.Status, c.Remedy)
		}
	})
}

// TestCamouflageCheck pins the camouflage verdict (the pure builder, fed
// synthetic probe outcomes).
func TestCamouflageCheck(t *testing.T) {
	t.Run("tcp unreachable → fail", func(t *testing.T) {
		c := camouflageCheck("Tokyo", engine.CamouflageReport{
			Address: "1.2.3.4:443", SNI: "www.cloudflare.com", Err: "i/o timeout"})
		if c.Status != "fail" {
			t.Fatalf("status=%q, want fail", c.Status)
		}
	})

	t.Run("tls/cert broken → fail", func(t *testing.T) {
		c := camouflageCheck("Tokyo", engine.CamouflageReport{
			Address: "1.2.3.4:443", SNI: "www.cloudflare.com", TCPOK: true,
			Err: "x509: certificate is not valid for www.cloudflare.com"})
		if c.Status != "fail" {
			t.Fatalf("status=%q, want fail", c.Status)
		}
	})

	t.Run("16KB freeze → warn", func(t *testing.T) {
		c := camouflageCheck("Tokyo", engine.CamouflageReport{
			Address: "1.2.3.4:443", SNI: "www.cloudflare.com", TCPOK: true, TLSOK: true,
			HTTPStatus: 200, BytesRead: 16384, Frozen: true})
		if c.Status != "warn" {
			t.Fatalf("status=%q, want warn", c.Status)
		}
		if c.Remedy != nil {
			t.Errorf("camouflage is advisory — expected no remedy, got %+v", c.Remedy)
		}
	})

	t.Run("healthy, crossed threshold → ok", func(t *testing.T) {
		c := camouflageCheck("Tokyo", engine.CamouflageReport{
			Address: "1.2.3.4:443", SNI: "www.cloudflare.com", TCPOK: true, TLSOK: true,
			HTTPStatus: 200, BytesRead: 32768})
		if c.Status != "ok" {
			t.Fatalf("status=%q, want ok", c.Status)
		}
	})

	t.Run("healthy but small decoy → ok with inconclusive note", func(t *testing.T) {
		c := camouflageCheck("Tokyo", engine.CamouflageReport{
			Address: "1.2.3.4:443", SNI: "www.cloudflare.com", TCPOK: true, TLSOK: true,
			HTTPStatus: 200, BytesRead: 4096})
		if c.Status != "ok" {
			t.Fatalf("status=%q, want ok", c.Status)
		}
		var noted bool
		for _, d := range c.Details {
			if strings.Contains(d, "<16 KB") {
				noted = true
			}
		}
		if !noted {
			t.Errorf("expected a small-decoy / freeze-not-exercised note in details: %v", c.Details)
		}
	})
}

func equalServers(a, b []api.DNSServer) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestNodeHealthCheck pins the universal node-connectivity row's verdict (the
// row that gives SS / anytls / QUIC a doctor check despite having no camouflage
// front): ok when every stage passed, fail naming unreachable (tcp) vs
// tunnel-did-not-open (proxy).
func TestNodeHealthCheck(t *testing.T) {
	ok := nodeHealthCheck("ss-tokyo", engine.Diagnosis{
		OK: true,
		Stages: []engine.StageResult{
			{Stage: engine.StageTCP, OK: true, DurationMs: 12},
			{Stage: engine.StageProxy, OK: true, DurationMs: 88},
		},
	})
	if ok.ID != "node_health" || ok.Status != "ok" || !strings.Contains(ok.Summary, "88 ms") {
		t.Errorf("healthy: id=%q status=%q summary=%q", ok.ID, ok.Status, ok.Summary)
	}

	tcpFail := nodeHealthCheck("dead", engine.Diagnosis{
		FailedStage: engine.StageTCP,
		Stages:      []engine.StageResult{{Stage: engine.StageTCP, Err: "connection refused"}},
	})
	if tcpFail.Status != "fail" || !strings.Contains(tcpFail.Summary, "unreachable") {
		t.Errorf("tcp-fail: status=%q summary=%q", tcpFail.Status, tcpFail.Summary)
	}

	proxyFail := nodeHealthCheck("misconfigured", engine.Diagnosis{
		FailedStage: engine.StageProxy,
		Stages:      []engine.StageResult{{Stage: engine.StageProxy, Err: "handshake timeout"}},
	})
	if proxyFail.Status != "fail" || !strings.Contains(proxyFail.Summary, "Tunnel did not open") {
		t.Errorf("proxy-fail: status=%q summary=%q", proxyFail.Status, proxyFail.Summary)
	}
}
