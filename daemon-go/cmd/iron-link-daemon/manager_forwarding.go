// The "forwarding_check" verb: the Linux-only host-firewall ↔ forwarded-traffic
// check behind its own button/section. On Linux the TUN's auto_redirect
// redirects forwarded TCP (from docker0 / br-* bridges) into the local INPUT
// chain, where a default-deny firewall (UFW) drops it — so container/LAN builds
// lose TCP internet while DNS/ICMP still work. Read-only; mechanism + proof in
// docs/docker-ufw-redirect.md.
package main

import (
	"strings"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/engine"
)

// forwardingReport is the "forwarding_check" handler. It is a local read (no
// network), so it returns immediately; off Linux the row is omitted and the
// report is empty. Same doctor_report shape as the other doctor verbs.
func (m *manager) forwardingReport() api.Response {
	checks := []api.DoctorCheck{}
	if c, ok := forwardingCheck(engine.ProbeForwarding()); ok {
		checks = append(checks, c)
	}
	return api.Response{Status: api.StatusDoctorReport, Checks: checks}
}

// forwardingCheck is the PURE verdict for the host-firewall ↔ forwarded-traffic
// interaction. warn when UFW default-denies INPUT AND bridges exist; ok
// otherwise. The fix is NOT auto-appliable (we never mutate the host firewall —
// the no-OS-stack-mutation principle), so guidance is text in Details, no
// Remedy. Returns (check, include) — omitted entirely off Linux.
func forwardingCheck(r engine.ForwardingReport) (api.DoctorCheck, bool) {
	if !r.Linux {
		return api.DoctorCheck{}, false // auto_redirect (and this conflict) is Linux-only
	}
	c := api.DoctorCheck{ID: "forwarded_traffic", Title: "Docker / LAN forwarding"}

	if r.UFWActive && r.InputDeny && len(r.Bridges) > 0 {
		bridges := strings.Join(r.Bridges, ", ")
		c.Status = "warn"
		c.Summary = "While the VPN is up, forwarded TCP from " + bridges +
			" (e.g. `docker build`) can't reach the internet — the host firewall drops it."
		c.Details = append(c.Details,
			"UFW is active with a default-deny INPUT policy"+policySuffix(r)+".",
			"On Linux the TUN uses auto_redirect: forwarded TCP is redirected to a local port, "+
				"entering the INPUT chain — where UFW drops it. DNS and ICMP take other paths, so only TCP breaks.",
			"Bridges affected: "+bridges+".",
			"Fix without changing the host: build with `docker build --network=host` (or run with `--network host`).",
			"Or allow it: `sudo ufw allow in on docker0` — note this opens host services to containers and "+
				"the redirect port is dynamic so it can't be scoped; revert with `ufw delete allow in on docker0`.",
		)
		return c, true
	}

	c.Status = "ok"
	switch {
	case len(r.Bridges) == 0:
		c.Summary = "No bridge interfaces present — no forwarded traffic to drop."
	case !r.UFWActive:
		c.Summary = "UFW is not active — forwarded traffic from bridges isn't firewalled."
	case !r.InputDeny:
		c.Summary = "Firewall INPUT policy isn't default-deny — forwarded traffic won't be dropped."
	default:
		c.Summary = "No host-firewall conflict for forwarded traffic detected."
	}
	if len(r.Bridges) > 0 {
		c.Details = append(c.Details, "Bridges present: "+strings.Join(r.Bridges, ", "))
	}
	return c, true
}

// policySuffix annotates the warn detail with the raw UFW policy value when known.
func policySuffix(r engine.ForwardingReport) string {
	if r.PolicyDetail == "" {
		return ""
	}
	return ` (DEFAULT_INPUT_POLICY="` + r.PolicyDetail + `")`
}
