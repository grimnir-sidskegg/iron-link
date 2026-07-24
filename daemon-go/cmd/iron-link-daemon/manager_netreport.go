// The "Network report" — the doctor's heavier, button-triggered network
// characterization: provider reachability, DNS integrity, and transport
// reachability. Each result becomes a DoctorCheck row (same reply shape as the
// quick checks). All read-only.
package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/engine"
)

// networkReportBudget bounds the whole sweep (providers + DNS + protocols run
// concurrently; the providers are themselves concurrency-capped internally).
const networkReportBudget = 30 * time.Second

// networkReport runs the three characterization probes concurrently and returns
// their rows: one per provider, then DNS integrity, then transports.
func (m *manager) networkReport() api.Response {
	ctx, cancel := context.WithTimeout(context.Background(), networkReportBudget)
	defer cancel()

	var (
		providers []engine.ProviderResult
		dns       engine.DNSIntegrityReport
		protos    []engine.ProtocolResult
		wg        sync.WaitGroup
	)
	wg.Add(3)
	go func() { defer wg.Done(); providers = engine.ProbeProviders(ctx) }()
	go func() { defer wg.Done(); dns = engine.ProbeDNSIntegrity(ctx) }()
	go func() { defer wg.Done(); protos = engine.ProbeProtocols(ctx) }()
	wg.Wait()

	checks := make([]api.DoctorCheck, 0, len(providers)+2)
	for _, p := range providers {
		checks = append(checks, providerCheck(p))
	}
	checks = append(checks, dnsIntegrityCheck(dns), protocolsCheck(protos))
	return api.Response{Status: api.StatusDoctorReport, Checks: checks}
}

// providerCheck maps one provider's reachability into a row: fail when
// unreachable (DNS-blocked vs TCP-blackholed), warn on TLS interference or the
// 16 KB freeze, ok when it carries traffic.
func providerCheck(r engine.ProviderResult) api.DoctorCheck {
	c := api.DoctorCheck{ID: "provider:" + r.Name, Title: "Provider — " + r.Name}
	c.Details = append(c.Details, "Endpoint: "+r.Host)

	if !r.TCPOK {
		c.Status = "fail"
		if strings.Contains(r.Err, "lookup ") || strings.Contains(r.Err, "no such host") ||
			strings.Contains(r.Err, "name resolution") {
			c.Summary = "Unreachable — the endpoint does not resolve (DNS-blocked or stale)."
		} else {
			c.Summary = "Unreachable — TCP blocked / blackholed."
		}
		c.Details = append(c.Details, r.Err)
		return c
	}
	if !r.TLSOK {
		c.Status = "warn"
		c.Summary = "TCP connects but TLS is interfered with (RST / MITM on the handshake)."
		c.Details = append(c.Details, "TLS: "+r.Err)
		return c
	}
	if r.Frozen {
		kb := r.BytesRead / 1024
		c.Status = "warn"
		c.Summary = fmt.Sprintf("Frozen at ~%d KB — the 16 KB block hits this provider's network.", kb)
		return c
	}
	c.Status = "ok"
	kb := r.BytesRead / 1024
	// BytesRead is the total flowed (upload padding + download), so the freeze is
	// exercised even on a tiny-response endpoint.
	c.Summary = fmt.Sprintf("Reachable — carried %d KB, no freeze.", kb)
	return c
}

// dnsIntegrityCheck maps the DNS-tamper probe into one row.
func dnsIntegrityCheck(r engine.DNSIntegrityReport) api.DoctorCheck {
	c := api.DoctorCheck{ID: "dns_integrity", Title: "DNS integrity"}
	if !r.DoHWorks {
		c.Status = "warn"
		c.Summary = "Encrypted DNS (DoH) is blocked — DNS integrity can't be verified."
		c.Details = append(c.Details, "The trusted DoH reference (dns.google) didn't answer: "+r.DoHErr)
		return c
	}
	c.Details = append(c.Details, "Trusted DoH channel (dns.google) reachable")

	var issues []string
	if len(r.Poisoned) > 0 {
		issues = append(issues, "poisoning")
		c.Details = append(c.Details,
			"OS resolver returns FORGED answers for: "+strings.Join(r.Poisoned, ", ")+" (disjoint from the DoH truth)")
	}
	if len(r.StubIPs) > 0 {
		issues = append(issues, "stub IPs")
		c.Details = append(c.Details,
			"Same IP returned for multiple blocked domains (a block-page stub): "+strings.Join(r.StubIPs, ", "))
	}
	if r.UDPHijacked {
		issues = append(issues, "UDP/53 hijack")
		c.Details = append(c.Details,
			"Plain DNS to 8.8.8.8 (UDP/53) is intercepted and answered by the censor.")
	}
	if len(issues) > 0 {
		c.Status = "warn"
		c.Summary = "DNS is being tampered with (" + strings.Join(issues, ", ") +
			") — prefer DoT/DoH (already the default)."
		return c
	}
	c.Status = "ok"
	c.Summary = "DNS answers match the trusted resolver — no poisoning detected."
	return c
}

// protocolsCheck maps the transport probes into one row.
func protocolsCheck(rs []engine.ProtocolResult) api.DoctorCheck {
	c := api.DoctorCheck{ID: "protocols", Title: "Transport reachability"}
	var blocked []string
	for _, p := range rs {
		mark := "ok"
		if !p.OK {
			mark = "BLOCKED"
			blocked = append(blocked, p.Name)
		}
		c.Details = append(c.Details, fmt.Sprintf("%s: %s (%s)", p.Name, mark, p.Detail))
	}
	if len(blocked) == 0 {
		c.Status = "ok"
		c.Summary = "All probed transports reachable."
	} else {
		c.Status = "warn"
		c.Summary = "Blocked: " + strings.Join(blocked, ", ") + "."
	}
	return c
}
