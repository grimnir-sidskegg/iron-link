// Connection doctor — read-only network probes (ROADMAP "Connection doctor").
// Where the staged DiagnoseNode answers "where does THIS node break", the
// doctor answers "is the local network itself healthy, and do my settings
// match it". Every probe changes nothing; remediation is a suggestion the
// client surfaces for the user to accept.
//
// The first check is connectivity + address family: direct, TUN-exempt dials
// (markControl, the same own-output fwmark the diagnosis tcp stage uses) to
// LITERAL anycast IPs over tcp4 and tcp6 separately, plus an observation of
// which families the OS resolver answers with. Literal IPs keep the
// reachability legs independent of DNS — a v6-only network with a broken
// resolver is a different verdict from one with a working one.

package engine

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"ironlink/daemon/internal/proxy"
)

// Doctor probe targets: well-known anycast resolvers on 443 (near-universally
// reachable, TLS port rarely filtered). Two per family so a single dead
// endpoint does not read as "no connectivity".
var (
	doctorV4Targets = []string{"1.1.1.1:443", "8.8.8.8:443"}
	doctorV6Targets = []string{"[2606:4700:4700::1111]:443", "[2001:4860:4860::8888]:443"}
)

// doctorDialTimeout bounds one family's reachability probe. A blackholed family
// hits this; a routed-but-refused one returns immediately.
const doctorDialTimeout = 4 * time.Second

// doctorResolveHost is a stable dual-stack name used ONLY to observe which
// address families the OS resolver returns (A vs AAAA) — never dialed.
const doctorResolveHost = "cloudflare.com"

// InternetReport is the connectivity probe outcome: per-family reachability via
// direct dials, plus which families the OS resolver answered with. All fields
// are observations — the verdict and any setting recommendation are derived
// from this by the caller (it owns the current settings to compare against).
type InternetReport struct {
	V4       bool   // a direct TCP path over IPv4 reached a public endpoint
	V6       bool   // ditto over IPv6
	V4Detail string // failure reason for the v4 leg ("" on success)
	V6Detail string // failure reason for the v6 leg ("" on success)

	ResolverV4 bool   // the OS resolver returned A records for the probe host
	ResolverV6 bool   // ... and/or AAAA records
	ResolveErr string // resolver failure ("" on success)
}

// ProbeInternet runs the connectivity + address-family probe. The dials are
// TUN-exempt (markControl) so on Linux they measure the REAL underlying network
// even under an active session. READ-ONLY.
func ProbeInternet(ctx context.Context) InternetReport {
	var r InternetReport
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); r.V4, r.V4Detail = dialFamily(ctx, "tcp4", doctorV4Targets) }()
	go func() { defer wg.Done(); r.V6, r.V6Detail = dialFamily(ctx, "tcp6", doctorV6Targets) }()
	go func() {
		defer wg.Done()
		r.ResolverV4, r.ResolverV6, r.ResolveErr = resolverFamilies(ctx, doctorResolveHost)
	}()
	wg.Wait()
	return r
}

// dialFamily tries the targets in order over the given network ("tcp4"/"tcp6")
// and reports whether ANY connected, with the last error as the detail. A
// successful TCP handshake = that family has a working path to the internet.
func dialFamily(ctx context.Context, network string, targets []string) (bool, string) {
	dialer := &net.Dialer{Timeout: doctorDialTimeout, Control: markControl}
	var lastErr error
	for _, addr := range targets {
		conn, err := dialer.DialContext(ctx, network, addr)
		if err == nil {
			conn.Close()
			return true, ""
		}
		lastErr = err
	}
	if lastErr != nil {
		return false, lastErr.Error()
	}
	return false, "no probe targets"
}

// resolverFamilies asks the system's configured resolver (via tunExemptResolver
// so an active TUN's hijack-dns is bypassed and we see the REAL path) for the
// probe host and reports which address families it returned — the signal for
// whether the resolver can answer over IPv4, IPv6, or both.
func resolverFamilies(ctx context.Context, host string) (v4, v6 bool, errMsg string) {
	ips, err := tunExemptResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return false, false, err.Error()
	}
	for _, ip := range ips {
		if ip.IP.To4() != nil {
			v4 = true
		} else {
			v6 = true
		}
	}
	return v4, v6, ""
}

// ---- resolver-blocked check ------------------------------------------------

// resolverHostnames maps well-known public resolver IPs to the TLS hostname
// their DoT/DoH endpoint presents, so a probe can VERIFY the certificate — a
// transparent middlebox that intercepts 853/443 and completes a handshake
// won't have a valid cert for these names. An IP absent here is reached
// handshake-only (reachability, not authenticity).
var resolverHostnames = map[string]string{
	"8.8.8.8":              "dns.google",
	"8.8.4.4":              "dns.google",
	"2001:4860:4860::8888": "dns.google",
	"2001:4860:4860::8844": "dns.google",
	"1.1.1.1":              "cloudflare-dns.com",
	"1.0.0.1":              "cloudflare-dns.com",
	"2606:4700:4700::1111": "cloudflare-dns.com",
	"2606:4700:4700::1001": "cloudflare-dns.com",
	"9.9.9.9":              "dns.quad9.net",
	"149.112.112.112":      "dns.quad9.net",
}

// doctorResolverCandidates is the curated fallback set the resolver check
// tries when a configured upstream is blocked — well-known DoT resolvers, in
// preference order. All have cert-verifiable hostnames above.
var doctorResolverCandidates = []DNSUpstream{
	{Type: "tls", Address: "1.1.1.1"},
	{Type: "tls", Address: "8.8.8.8"},
	{Type: "tls", Address: "9.9.9.9"},
}

// DNSUpstreamProbe is one configured resolver's reachability verdict.
type DNSUpstreamProbe struct {
	Type      string // "udp" / "tls" / "https"
	Address   string // the literal IP
	Reachable bool
	// Detail is the failure reason when blocked, or a note ("verified <host>")
	// on success.
	Detail string
}

// ProbeResolvers probes every configured upstream concurrently and returns the
// verdicts in the SAME order (so probes[0] is the primary).
func ProbeResolvers(ctx context.Context, servers []DNSUpstream) []DNSUpstreamProbe {
	probes := make([]DNSUpstreamProbe, len(servers))
	var wg sync.WaitGroup
	for i := range servers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			probes[i] = ProbeDNSUpstream(ctx, servers[i].Type, servers[i].Address)
		}(i)
	}
	wg.Wait()
	return probes
}

// ProbeDNSUpstream checks ONE upstream for reachability with a TUN-exempt dial.
// udp → a real DNS query pinned to that server (UDP/53 is the most
// hijack-prone); tls/https → a TCP+TLS handshake to 853/443, cert-verified
// against the known hostname for public resolvers (handshake-only otherwise).
func ProbeDNSUpstream(ctx context.Context, typ, address string) DNSUpstreamProbe {
	p := DNSUpstreamProbe{Type: typ, Address: address}
	switch typ {
	case "udp":
		p.Reachable, p.Detail = probeUDPResolver(ctx, address)
	case "tls":
		p.Reachable, p.Detail = probeTLSResolver(ctx, address, 853)
	case "https":
		p.Reachable, p.Detail = probeTLSResolver(ctx, address, 443)
	default:
		p.Detail = "unknown upstream type " + typ
	}
	return p
}

// probeUDPResolver sends a real DNS query pinned to ip:53 over UDP (the Go
// resolver may fall back to TCP for a truncated answer — both go to the same
// upstream). No answer in budget = blocked / hijacked-to-nowhere.
func probeUDPResolver(ctx context.Context, ip string) (bool, string) {
	addr := net.JoinHostPort(ip, "53")
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: doctorDialTimeout, Control: markControl}
			return d.DialContext(ctx, network, addr)
		},
	}
	cctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	if _, err := r.LookupHost(cctx, doctorResolveHost); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// probeTLSResolver TCP-connects then TLS-handshakes to ip:port. For a known
// public resolver it VERIFIES the certificate against the canonical hostname
// (catches a hijack that completes a handshake with the wrong cert); for an
// unknown IP it reports handshake reachability without verification.
func probeTLSResolver(ctx context.Context, ip string, port int) (bool, string) {
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: doctorDialTimeout, Control: markControl}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false, err.Error()
	}
	defer raw.Close()

	hostname, known := resolverHostnames[ip]
	cfg := &tls.Config{}
	if known {
		cfg.ServerName = hostname
	} else {
		cfg.InsecureSkipVerify = true // unknown IP: reachability only, can't verify
	}
	conn := tls.Client(raw, cfg)
	hctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	if err := conn.HandshakeContext(hctx); err != nil {
		return false, err.Error()
	}
	conn.Close()
	if known {
		return true, "verified " + hostname
	}
	return true, "reachable (cert not verified)"
}

// FirstReachableCandidate probes the curated fallback resolvers concurrently
// (skipping any address in exclude) and returns the first reachable one in
// preference order, or nil when none answer.
func FirstReachableCandidate(ctx context.Context, exclude map[string]bool) *DNSUpstream {
	cands := make([]DNSUpstream, 0, len(doctorResolverCandidates))
	for _, c := range doctorResolverCandidates {
		if !exclude[c.Address] {
			cands = append(cands, c)
		}
	}
	reachable := make([]bool, len(cands))
	var wg sync.WaitGroup
	for i := range cands {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reachable[i] = ProbeDNSUpstream(ctx, cands[i].Type, cands[i].Address).Reachable
		}(i)
	}
	wg.Wait()
	for i := range cands {
		if reachable[i] {
			c := cands[i]
			return &c
		}
	}
	return nil
}

// ---- node camouflage check -------------------------------------------------

const (
	// camouflageDownloadTarget is how much of the decoy we try to pull — past
	// the 15-20KB threshold of Russia's "16KB freeze" so we can observe it.
	camouflageDownloadTarget = 32 * 1024
	// camouflageReadStall: a read that blocks this long AFTER partial data, with
	// no EOF/RST, is the freeze signature (the censor silently stops forwarding).
	camouflageReadStall = 4 * time.Second
	// camouflageFreezeFloor: only call a mid-stream stall a "freeze" once at
	// least this much flowed — distinguishes the ~16KB block from an early stall.
	camouflageFreezeFloor = 8 * 1024
	// camouflageUploadPad: bytes of request padding (X-Pad headers) we push so
	// the connection carries >16KB even when the server's RESPONSE is tiny — the
	// censor's freeze counts bytes in EITHER direction (net4people#490), so
	// padding the upload exercises it on ANY target (dpi-detector's trick),
	// removing the "decoy too small to test the freeze" blind spot.
	camouflageUploadPad = 20 * 1024
)

// CamouflageReport is the raw outcome of a direct (TUN-exempt) probe to a node's
// front — the manager classifies it into a DoctorCheck verdict. The probe
// REPLICATES the censor's condition: it dials the node IP with the camouflage
// SNI, verifies the front's cert, then pulls the decoy past ~16KB watching for
// the silent freeze that kills real traffic even when the handshake looks fine.
type CamouflageReport struct {
	Address    string // server:port probed
	SNI        string // camouflage SNI ("" when the node carries no TLS layer)
	TCPOK      bool   // TCP connect to the node succeeded
	TLSOK      bool   // TLS handshake + cert verified for the SNI
	HTTPStatus int    // the decoy's HTTP status (0 = none parsed)
	BytesRead  int    // decoy bytes pulled before EOF / freeze / target
	Frozen     bool   // the transfer stalled mid-stream past the freeze floor
	Err        string // failure detail at the stage that broke ("" if fully ok)
}

// HasCamouflage reports whether the direct camouflage probe applies to a node:
// it must carry a TLS layer worth probing (Reality or TLS) AND speak TCP. The
// probe (ProbeCamouflage) is a TCP-connect + TLS handshake + decoy download that
// replicates the censor's TLS-over-TCP view; a QUIC node (hysteria2/tuic/
// hysteria, ServerNetwork=="udp") has a TLS front but no TCP socket to probe,
// so it is excluded rather than TCP-probed into a spurious "unreachable". A
// plain node (no SNI) is excluded too.
func HasCamouflage(p proxy.Profile) bool {
	if p == nil { // a group node has no server to probe
		return false
	}
	_, ok := p.CamouflageSNI()
	return ok && p.ServerNetwork() == "tcp"
}

// ProbeCamouflage runs the direct camouflage probe for one node. READ-ONLY; it
// authenticates nothing (an unkeyed client sees exactly what the censor sees).
func ProbeCamouflage(ctx context.Context, p proxy.Profile) CamouflageReport {
	sni, _ := p.CamouflageSNI()
	addr := net.JoinHostPort(p.ServerAddress(), strconv.Itoa(int(p.ServerPort())))
	r := CamouflageReport{Address: addr, SNI: sni}
	// Dial the node IP, TLS to the front with the camouflage SNI (cert-verified;
	// a torn front / DPI on the handshake fails here), then pull the decoy past
	// the freeze threshold. An SNI-less node is probed handshake-only.
	r.TCPOK, r.TLSOK, r.HTTPStatus, r.BytesRead, r.Frozen, r.Err =
		dialVerifyFetch(ctx, addr, sni, sni, "/")
	return r
}

// dialVerifyFetch is the shared probe primitive: TCP-connect to addr
// (TUN-exempt), TLS-handshake with serverName (cert-verified unless empty), then
// GET path and pull the body past the 16KB threshold watching for the freeze.
// Used by the camouflage and provider-reachability probes.
func dialVerifyFetch(ctx context.Context, addr, serverName, host, path string) (tcpOK, tlsOK bool, status, n int, frozen bool, errMsg string) {
	dialer := probeDialer(doctorDialTimeout)
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false, false, 0, 0, false, err.Error()
	}
	defer raw.Close()

	cfg := &tls.Config{ServerName: serverName}
	if serverName == "" {
		cfg.InsecureSkipVerify = true
	}
	conn := tls.Client(raw, cfg)
	hctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	err = conn.HandshakeContext(hctx)
	cancel()
	if err != nil {
		return true, false, 0, 0, false, err.Error()
	}
	status, n, frozen, errMsg = fetchHTTP(conn, host, path)
	return true, true, status, n, frozen, errMsg
}

// fetchHTTP issues a GET (padded so the connection carries >16KB regardless of
// the response size) and watches for the freeze. n counts the TOTAL bytes
// flowed (upload padding + downloaded body). Outcomes: a stall mid-stream with
// no EOF/RST (the freeze), a clean EOF / target reached (no freeze), or an error.
func fetchHTTP(conn net.Conn, host, path string) (status, n int, frozen bool, errMsg string) {
	if host == "" {
		host = "example.com"
	}
	// Build a request padded with X-Pad headers (each well under per-header size
	// limits) totalling ~camouflageUploadPad bytes in the client→server direction.
	var b strings.Builder
	b.WriteString("GET " + path + " HTTP/1.1\r\nHost: " + host +
		"\r\nUser-Agent: Mozilla/5.0\r\nAccept: */*\r\nConnection: close\r\n")
	const padChunk = 2000
	for sent := 0; sent < camouflageUploadPad; sent += padChunk {
		b.WriteString("X-Pad-" + strconv.Itoa(sent/padChunk) + ": " + strings.Repeat("A", padChunk) + "\r\n")
	}
	b.WriteString("\r\n")
	req := b.String()
	uploaded := len(req)

	_ = conn.SetWriteDeadline(time.Now().Add(doctorDialTimeout))
	if _, err := conn.Write([]byte(req)); err != nil {
		// An upload stall mid-request (no RST) IS the freeze; anything else is a
		// request failure.
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return 0, uploaded, true, ""
		}
		return 0, 0, false, "request: " + err.Error()
	}

	br := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(camouflageReadStall))
	line, err := br.ReadString('\n')
	if err != nil {
		// We pushed the padded request; no response line arriving (a timeout) =
		// the connection froze (the censor stopped forwarding past ~16KB).
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return 0, uploaded, true, ""
		}
		return 0, uploaded, false, "response: " + err.Error()
	}
	status = parseHTTPStatus(line)

	n = uploaded // the upload counts toward the connection's flowed bytes
	buf := make([]byte, 8*1024)
	for n < uploaded+camouflageDownloadTarget {
		_ = conn.SetReadDeadline(time.Now().Add(camouflageReadStall))
		m, rerr := br.Read(buf)
		n += m
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return status, n, false, "" // clean EOF — connection completed
			}
			var ne net.Error
			if errors.As(rerr, &ne) && ne.Timeout() && n >= camouflageFreezeFloor {
				return status, n, true, "" // flowed then stalled, no EOF/RST = freeze
			}
			return status, n, false, "read: " + rerr.Error()
		}
	}
	return status, n, false, "" // pulled past the target without a freeze
}

// parseHTTPStatus pulls the numeric code out of an HTTP status line
// ("HTTP/1.1 200 OK" → 200); 0 when it isn't a status line.
func parseHTTPStatus(line string) int {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0
	}
	return code
}
