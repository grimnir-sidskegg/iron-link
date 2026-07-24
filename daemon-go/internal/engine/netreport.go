// Network report — heavier, read-only network-characterization probes for the
// doctor's "Network report" section. Where the per-connection checks answer
// "does THIS work", these answer "what is the SHAPE of blocking on my network":
//
//	providers  — which hosting/CDN networks are reachable vs RST / frozen / DNS-
//	             blocked, so a self-hoster knows WHERE to put a node.
//	dns         — is the OS resolver being tampered with (poisoning / UDP-53
//	             interception / DoH blocking).
//	protocols   — which transports survive (real handshakes, not bare SYNs, so a
//	             SYN-ACK-injecting middlebox can't fake "open").
//
// All probes dial direct (TUN-exempt) and change nothing.

package engine

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- A: provider / datacenter reachability ---------------------------------

// ProviderTarget is one hosting/CDN network's probe endpoint: a host that lives
// IN that network and serves HTTPS. Path points at a large object where one
// exists (so the 16KB-freeze can be exercised); "/" otherwise (reachability
// only). The list is curated + live-verified, not exhaustive.
type ProviderTarget struct {
	Name string
	Host string
	Path string
}

var doctorProviders = []ProviderTarget{
	{"Hetzner", "fsn1-speed.hetzner.com", "/100MB.bin"},
	{"Linode", "speedtest.newark.linode.com", "/100MB-newark.bin"},
	{"Vultr", "nj-us-ping.vultr.com", "/vultr.com.100MB.bin"},
	{"OVH", "proof.ovh.net", "/files/100Mb.dat"},
	{"Cloudflare", "speed.cloudflare.com", "/__down?bytes=65536"},
	{"DigitalOcean", "speedtest-nyc3.nyc3.digitaloceanspaces.com", "/"},
	{"AWS", "d1.awsstatic.com", "/"},
	{"Oracle Cloud", "objectstorage.us-ashburn-1.oraclecloud.com", "/"},
	{"Google Cloud", "storage.googleapis.com", "/"},
}

// ProviderResult is one provider's reachability verdict.
type ProviderResult struct {
	Name       string
	Host       string
	TCPOK      bool
	TLSOK      bool
	HTTPStatus int
	BytesRead  int
	Frozen     bool
	Err        string
}

// ProbeProviders probes every curated provider concurrently (cert-verified, so a
// DNS-poisoned IP fails on the cert rather than reading as reachable).
func ProbeProviders(ctx context.Context) []ProviderResult {
	out := make([]ProviderResult, len(doctorProviders))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i := range doctorProviders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = probeProvider(ctx, doctorProviders[i])
		}(i)
	}
	wg.Wait()
	return out
}

func probeProvider(ctx context.Context, t ProviderTarget) ProviderResult {
	r := ProviderResult{Name: t.Name, Host: t.Host}
	addr := net.JoinHostPort(t.Host, "443")
	r.TCPOK, r.TLSOK, r.HTTPStatus, r.BytesRead, r.Frozen, r.Err =
		dialVerifyFetch(ctx, addr, t.Host, t.Host, t.Path)
	return r
}

// ---- C: DNS integrity ------------------------------------------------------

// dnsReferenceDomains are commonly censored names used to detect DNS
// poisoning (the censor returns a stub IP instead of the real one); control is a
// name that must NOT be tampered with. Three names so the stub-IP frequency
// analysis (an IP repeated across ≥2 of them) is meaningful.
var dnsReferenceDomains = []string{"www.facebook.com", "twitter.com", "www.youtube.com"}

const dnsControlDomain = "cloudflare.com"

// DNSIntegrityReport is the DNS-tampering probe outcome.
type DNSIntegrityReport struct {
	DoHWorks bool   // the trusted DoH channel answered (false = DoH blocked)
	DoHErr   string // why DoH failed
	// Poisoned lists reference domains whose OS-resolver answer is DISJOINT from
	// the DoH truth (the poisoning signature).
	Poisoned []string
	// StubIPs are IPs the OS resolver returned for ≥2 different blocked domains
	// — the "one block-page stub for everything" signature (dpi-detector's
	// frequency analysis), a stronger IP-spoofing signal than per-domain disjoint.
	StubIPs []string
	// UDPHijacked: a query to a PUBLIC resolver (8.8.8.8) over plain UDP/53 came
	// back disjoint from the DoH truth — UDP DNS is intercepted/injected.
	UDPHijacked bool
	ControlOK   bool // the control domain matched between OS and DoH (sanity)
}

// ProbeDNSIntegrity compares the OS resolver (and plain UDP/53 to a public
// resolver) against a trusted DoH channel to detect poisoning / interception,
// and flags stub IPs shared across blocked domains.
func ProbeDNSIntegrity(ctx context.Context) DNSIntegrityReport {
	var r DNSIntegrityReport

	truth := func(name string) map[string]bool {
		ips, err := dohResolve(ctx, name)
		if err != nil {
			return nil
		}
		return toSet(ips)
	}

	// Control first — establishes the DoH channel works and the OS resolver
	// agrees with it for an unblocked name.
	ctrlTruth := truth(dnsControlDomain)
	if ctrlTruth == nil {
		r.DoHErr = "DoH query failed"
		return r // can't trust anything without the reference channel
	}
	r.DoHWorks = true
	if osSet := osResolve(ctx, dnsControlDomain); intersects(osSet, ctrlTruth) {
		r.ControlOK = true
	}

	ipFreq := map[string]int{}
	for _, name := range dnsReferenceDomains {
		osSet := osResolve(ctx, name)
		for ip := range osSet {
			ipFreq[ip]++
		}
		if t := truth(name); t != nil && len(osSet) > 0 && !intersects(osSet, t) {
			r.Poisoned = append(r.Poisoned, name)
		}
	}
	for ip, cnt := range ipFreq {
		if cnt >= 2 {
			r.StubIPs = append(r.StubIPs, ip)
		}
	}

	// UDP/53 interception: ask 8.8.8.8 over plain UDP; if its answer for a
	// reference name is disjoint from the DoH truth, UDP DNS is hijacked.
	if len(dnsReferenceDomains) > 0 {
		name := dnsReferenceDomains[0]
		if t := truth(name); t != nil {
			if udp := udpResolve(ctx, "8.8.8.8", name); len(udp) > 0 && !intersects(udp, t) {
				r.UDPHijacked = true
			}
		}
	}
	return r
}

// dohResolve resolves name's A records over the UNION of two independent DoH
// providers (Google + Cloudflare, each pinned + cert-verified) — a fuller truth
// set that survives one provider being blocked and resists CDN-rotation false
// positives. Errors only when BOTH fail.
func dohResolve(ctx context.Context, name string) ([]string, error) {
	g, gerr := dohQuery(ctx, "8.8.8.8:443", "dns.google",
		"https://dns.google/resolve?name="+name+"&type=A", "")
	c, cerr := dohQuery(ctx, "1.1.1.1:443", "cloudflare-dns.com",
		"https://cloudflare-dns.com/dns-query?name="+name+"&type=A", "application/dns-json")
	if gerr != nil && cerr != nil {
		return nil, gerr
	}
	return append(g, c...), nil
}

// dohQuery runs one DoH-JSON query against a pinned, cert-verified resolver. The
// accept header is set for resolvers that gate the JSON format on it (Cloudflare).
func dohQuery(ctx context.Context, pinAddr, serverName, url, accept string) ([]string, error) {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: doctorDialTimeout, Control: markControl}
			return d.DialContext(ctx, network, pinAddr)
		},
		TLSClientConfig: &tls.Config{ServerName: serverName},
	}
	client := &http.Client{Transport: tr, Timeout: doctorDialTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body); err != nil {
		return nil, err
	}
	var ips []string
	for _, a := range body.Answer {
		if a.Type == 1 { // A record
			ips = append(ips, a.Data)
		}
	}
	return ips, nil
}

func osResolve(ctx context.Context, name string) map[string]bool {
	cctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	ips, err := tunExemptResolver.LookupHost(cctx, name)
	if err != nil {
		return nil
	}
	return toSet(ips)
}

func udpResolve(ctx context.Context, server, name string) map[string]bool {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: doctorDialTimeout, Control: markControl}
			return d.DialContext(ctx, "udp", net.JoinHostPort(server, "53"))
		},
	}
	cctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	ips, err := r.LookupHost(cctx, name)
	if err != nil {
		return nil
	}
	return toSet(ips)
}

// ---- D: protocol / port reachability ---------------------------------------

// ProtocolResult is one transport's reachability — established by a REAL
// handshake/banner, not a bare TCP connect (a SYN-ACK-injecting middlebox would
// fake the latter).
type ProtocolResult struct {
	Name   string
	OK     bool
	Detail string
}

// ProbeProtocols checks the transports that matter for proxying: HTTPS/443 (where
// Reality lives), DoT/853, HTTP/80, and SSH/22 (often censor-exempt).
func ProbeProtocols(ctx context.Context) []ProtocolResult {
	checks := []func(context.Context) ProtocolResult{
		func(c context.Context) ProtocolResult {
			return probeTLSPort(c, "HTTPS (TCP 443)", "cloudflare.com", 443, "cloudflare.com")
		},
		func(c context.Context) ProtocolResult {
			return probeTLSPort(c, "DoT (TCP 853)", "8.8.8.8", 853, "dns.google")
		},
		func(c context.Context) ProtocolResult {
			return probeLine(c, "HTTP (TCP 80)", "example.com:80", "GET / HTTP/1.0\r\nHost: example.com\r\n\r\n", "HTTP/")
		},
		func(c context.Context) ProtocolResult {
			return probeLine(c, "SSH (TCP 22)", "github.com:22", "", "SSH-")
		},
	}
	out := make([]ProtocolResult, len(checks))
	var wg sync.WaitGroup
	for i := range checks {
		wg.Add(1)
		go func(i int) { defer wg.Done(); out[i] = checks[i](ctx) }(i)
	}
	wg.Wait()
	return out
}

// probeTLSPort completes a real TLS handshake (cert-verified) to host:port.
func probeTLSPort(ctx context.Context, name, host string, port int, serverName string) ProtocolResult {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := probeDialer(doctorDialTimeout)
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return ProtocolResult{name, false, "blocked: " + err.Error()}
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{ServerName: serverName})
	hctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	if err := conn.HandshakeContext(hctx); err != nil {
		return ProtocolResult{name, false, "TLS failed: " + err.Error()}
	}
	conn.Close()
	return ProtocolResult{name, true, "handshake ok"}
}

// probeLine connects, optionally writes req, and checks the first response line
// starts with want — a banner/response the middlebox can't fabricate.
func probeLine(ctx context.Context, name, addr, req, want string) ProtocolResult {
	dialer := probeDialer(doctorDialTimeout)
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return ProtocolResult{name, false, "blocked: " + err.Error()}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(doctorDialTimeout))
	if req != "" {
		if _, err := conn.Write([]byte(req)); err != nil {
			return ProtocolResult{name, false, "write failed: " + err.Error()}
		}
	}
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		return ProtocolResult{name, false, "no response: " + err.Error()}
	}
	if strings.HasPrefix(string(buf[:n]), want) {
		return ProtocolResult{name, true, "responded"}
	}
	return ProtocolResult{name, false, "unexpected response"}
}

// ---- helpers ---------------------------------------------------------------

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

func intersects(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}
