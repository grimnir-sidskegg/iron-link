// Connection-quality decomposition (ROADMAP "Connection doctor"). Where
// ProbeInternet answers "is there a path" and DiagnoseNode answers "where does
// THIS node break", this answers "the internet works but feels slow — WHICH step
// is the bottleneck". It times a full HTTPS fetch to a reference target broken
// into stages — DNS resolve (through the CONFIGURED resolver, so a throttled
// DoT/DoH channel is measured), TCP connect, TLS handshake, time-to-first-byte,
// and a short throughput sample — and flags each stage that runs slower than its
// healthy baseline. The motivating case: a censor that throttled DoT to Google
// DNS, so pages loaded slowly with the cost hidden entirely in the resolver step.
//
// The staged shape mirrors diagnose.go (ordered stages, stop at the first hard
// failure); the difference is that every stage is TIMED and independently graded
// (ok / slow / failed), so a stage that merely *completes* can still be named the
// bottleneck. All dials are TUN-exempt (the shared markControl / probe dialer),
// so an active session does not distort the measurement. READ-ONLY.

package engine

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// QualityStage names one decomposition stage.
type QualityStage string

const (
	QStageDNS        QualityStage = "dns"
	QStageTCP        QualityStage = "tcp"
	QStageTLS        QualityStage = "tls"
	QStageTTFB       QualityStage = "ttfb"
	QStageThroughput QualityStage = "throughput"
)

// Healthy baselines: a stage that completes but exceeds its threshold is
// "anomalous" (graded slow, not failed). They are deliberately loose — the goal
// is to surface a stage that is DRAMATICALLY slow (a throttled resolver, DPI
// stalling a handshake), not to nitpick a normal RTT — and are tunable as the
// probe earns field data.
const (
	// qualityDNSSlow: a healthy UDP/DoT/DoH resolve is well under 300 ms; past
	// this the resolver step (commonly a throttled DoT/DoH channel) is the story.
	qualityDNSSlow  = 900 * time.Millisecond
	qualityTCPSlow  = 1200 * time.Millisecond
	qualityTLSSlow  = 1500 * time.Millisecond
	qualityTTFBSlow = 2500 * time.Millisecond
	// qualityThroughputFloorKBps: below this the path is degraded (deep shaping,
	// a mid-stream freeze, or a saturated link).
	qualityThroughputFloorKBps = 150

	// qualityThroughputWindow bounds the throughput sample so a slow path cannot
	// stall the doctor; KB/s is bytes-read / elapsed within the window.
	qualityThroughputWindow = 3 * time.Second
	// qualityTTFBBudget bounds the wait for the first response byte.
	qualityTTFBBudget = 6 * time.Second
)

// The reference target: a Cloudflare speed endpoint that serves an arbitrarily
// large body (so throughput can be sampled) behind a real, cert-verifiable front
// (so the TLS stage is meaningful). Resolving its hostname is what exercises the
// DNS stage.
const (
	qualityTargetHost = "speed.cloudflare.com"
	qualityTargetPath = "/__down?bytes=10000000"
)

// QualityStageResult is one stage's timing and grade.
type QualityStageResult struct {
	Stage      QualityStage
	DurationMs int64
	OK         bool // the stage completed (did not error)
	// Anomalous is true when the stage completed but ran slower / worse than the
	// healthy baseline — the "works but slow" signal.
	Anomalous bool
	// Detail is the stage note: the DNS transport ("DoT 8.8.8.8"), the HTTP
	// status, the throughput ("620 KB/s (…)"), or the error when !OK.
	Detail string
}

// QualityReport is the decomposed full-cycle fetch: the ordered stage timings
// plus the attribution. OK is true only when every stage completed; FailedStage
// names the first stage that errored; Bottleneck names the worst anomalous stage
// of a cycle that completed (empty when healthy).
type QualityReport struct {
	Target      string
	Stages      []QualityStageResult
	OK          bool
	FailedStage QualityStage
	Bottleneck  QualityStage
}

// ProbeConnectionQuality runs the staged full-cycle probe against the reference
// target, resolving through the configured primary upstream so the DNS stage
// reflects the resolver the session actually uses. target/path default to the
// reference endpoint when empty. Stages run in order and stop at the first hard
// failure (a later stage cannot run without its predecessor). READ-ONLY.
func ProbeConnectionQuality(ctx context.Context, primary DNSUpstream, target, path string) QualityReport {
	if target == "" {
		target = qualityTargetHost
	}
	if path == "" {
		path = qualityTargetPath
	}
	r := QualityReport{Target: target}

	// Stage 1 — DNS: resolve the target through the CONFIGURED resolver, timing
	// the whole round trip. A throttled DoT/DoH channel surfaces HERE.
	ips, dns := measureDNS(ctx, primary, target)
	r.Stages = append(r.Stages, dns)
	if !dns.OK {
		r.FailedStage = QStageDNS
		return r
	}
	addr := net.JoinHostPort(ips[0].String(), "443")

	// Stage 2 — TCP connect to the resolved endpoint.
	raw, tcp := measureTCP(ctx, addr)
	r.Stages = append(r.Stages, tcp)
	if !tcp.OK {
		r.FailedStage = QStageTCP
		return r
	}
	defer raw.Close()

	// Stage 3 — TLS handshake (cert-verified against the target host).
	conn, tlsRes := measureTLS(ctx, raw, target)
	r.Stages = append(r.Stages, tlsRes)
	if !tlsRes.OK {
		r.FailedStage = QStageTLS
		return r
	}

	// Stage 4 — TTFB: send the GET and time the first response byte.
	br, ttfb := measureTTFB(conn, target, path)
	r.Stages = append(r.Stages, ttfb)
	if !ttfb.OK {
		r.FailedStage = QStageTTFB
		return r
	}

	// Stage 5 — throughput: read a bounded sample of the body and report KB/s.
	tput := measureThroughput(conn, br)
	r.Stages = append(r.Stages, tput)
	if !tput.OK {
		r.FailedStage = QStageThroughput
		return r
	}

	r.OK = true
	r.Bottleneck = worstAnomaly(r.Stages)
	return r
}

// measureDNS resolves host through the configured upstream over its own transport
// (udp / DoT / DoH), timing the full query — the number that spikes when the
// encrypted-DNS channel is throttled.
func measureDNS(ctx context.Context, up DNSUpstream, host string) ([]net.IP, QualityStageResult) {
	res := QualityStageResult{Stage: QStageDNS, Detail: dnsUpstreamLabel(up)}
	start := time.Now()
	ips, err := timedResolve(ctx, up, host)
	res.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Detail = dnsUpstreamLabel(up) + ": " + err.Error()
		return nil, res
	}
	if len(ips) == 0 {
		res.Detail = dnsUpstreamLabel(up) + ": no A records"
		return nil, res
	}
	res.OK = true
	res.Anomalous = res.DurationMs > qualityDNSSlow.Milliseconds()
	return ips, res
}

// timedResolve resolves host's A records through the configured upstream: a real
// query over UDP/53 or DoT/853 (hand-built via dnsmessage), or a DoH-JSON query
// for a recognized public HTTPS resolver. Every leg is TUN-exempt.
func timedResolve(ctx context.Context, up DNSUpstream, host string) ([]net.IP, error) {
	cctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	switch up.Type {
	case "https":
		return dohUpstreamQuery(cctx, up.Address, host)
	case "tls":
		return dotQuery(cctx, up.Address, host)
	default: // "udp"
		return udpQuery(cctx, up.Address, host)
	}
}

// udpQuery sends a real A query to ip:53 over UDP (TUN-exempt) and parses the
// answer.
func udpQuery(ctx context.Context, ip, host string) ([]net.IP, error) {
	d := net.Dialer{Timeout: doctorDialTimeout, Control: markControl}
	conn, err := d.DialContext(ctx, "udp", net.JoinHostPort(ip, "53"))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	q, err := buildDNSQuery(host)
	if err != nil {
		return nil, err
	}
	setConnDeadline(ctx, conn)
	if _, err := conn.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return parseDNSAnswer(buf[:n])
}

// dotQuery runs a real A query over DNS-over-TLS to ip:853 (TCP+TLS, cert-verified
// for a known public resolver), timing connect+handshake+query as one — a
// throttle that lets the handshake through but shapes the query bytes still shows.
func dotQuery(ctx context.Context, ip, host string) ([]net.IP, error) {
	d := net.Dialer{Timeout: doctorDialTimeout, Control: markControl}
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, "853"))
	if err != nil {
		return nil, err
	}
	defer raw.Close()
	cfg := &tls.Config{}
	if hn, ok := resolverHostnames[ip]; ok {
		cfg.ServerName = hn
	} else {
		cfg.InsecureSkipVerify = true // unknown DoT IP: reachability, not authenticity
	}
	conn := tls.Client(raw, cfg)
	if err := conn.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	q, err := buildDNSQuery(host)
	if err != nil {
		return nil, err
	}
	setConnDeadline(ctx, conn)
	// DoT frames the message with a 2-byte big-endian length prefix (RFC 7858).
	var lp [2]byte
	binary.BigEndian.PutUint16(lp[:], uint16(len(q)))
	if _, err := conn.Write(append(lp[:], q...)); err != nil {
		return nil, err
	}
	var rl [2]byte
	if _, err := io.ReadFull(conn, rl[:]); err != nil {
		return nil, err
	}
	resp := make([]byte, binary.BigEndian.Uint16(rl[:]))
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, err
	}
	return parseDNSAnswer(resp)
}

// dohUpstreamQuery resolves host over a recognized public DoH resolver (reusing
// the DoH-JSON helper), timing the HTTPS query. An unrecognized HTTPS upstream is
// rejected — the first cut only knows the curated public endpoints' URL shapes.
func dohUpstreamQuery(ctx context.Context, ip, host string) ([]net.IP, error) {
	hostname, ok := resolverHostnames[ip]
	if !ok {
		return nil, fmt.Errorf("DoH upstream %s is not a recognized public resolver", ip)
	}
	var url, accept string
	if hostname == "dns.google" {
		url = "https://dns.google/resolve?name=" + host + "&type=A"
	} else { // cloudflare-dns.com / dns.quad9.net speak the dns-json API
		url = "https://" + hostname + "/dns-query?name=" + host + "&type=A"
		accept = "application/dns-json"
	}
	strs, err := dohQuery(ctx, net.JoinHostPort(ip, "443"), hostname, url, accept)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(strs))
	for _, s := range strs {
		if p := net.ParseIP(s); p != nil {
			ips = append(ips, p)
		}
	}
	return ips, nil
}

// buildDNSQuery packs a recursive A-record query for host.
func buildDNSQuery(host string) ([]byte, error) {
	fqdn := host
	if !strings.HasSuffix(fqdn, ".") {
		fqdn += "."
	}
	name, err := dnsmessage.NewName(fqdn)
	if err != nil {
		return nil, err
	}
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{ID: uint16(time.Now().UnixNano()), RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  name,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
	}
	return msg.Pack()
}

// parseDNSAnswer extracts the A records from a packed DNS response.
func parseDNSAnswer(buf []byte) ([]net.IP, error) {
	var msg dnsmessage.Message
	if err := msg.Unpack(buf); err != nil {
		return nil, err
	}
	var ips []net.IP
	for _, a := range msg.Answers {
		if a.Header.Type != dnsmessage.TypeA {
			continue
		}
		if body, ok := a.Body.(*dnsmessage.AResource); ok {
			ip := make(net.IP, len(body.A))
			copy(ip, body.A[:])
			ips = append(ips, ip)
		}
	}
	return ips, nil
}

// measureTCP times a TUN-exempt TCP connect to addr.
func measureTCP(ctx context.Context, addr string) (net.Conn, QualityStageResult) {
	res := QualityStageResult{Stage: QStageTCP}
	d := &net.Dialer{Timeout: doctorDialTimeout, Control: markControl}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	res.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Detail = err.Error()
		return nil, res
	}
	res.OK = true
	res.Anomalous = res.DurationMs > qualityTCPSlow.Milliseconds()
	return conn, res
}

// measureTLS times the TLS handshake to the target host over an established conn.
func measureTLS(ctx context.Context, raw net.Conn, host string) (*tls.Conn, QualityStageResult) {
	res := QualityStageResult{Stage: QStageTLS}
	conn := tls.Client(raw, &tls.Config{ServerName: host})
	hctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	start := time.Now()
	err := conn.HandshakeContext(hctx)
	res.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Detail = err.Error()
		return nil, res
	}
	res.OK = true
	res.Anomalous = res.DurationMs > qualityTLSSlow.Milliseconds()
	return conn, res
}

// measureTTFB issues the GET and times the first response byte (the status line);
// it returns the buffered reader positioned right after that line so throughput
// can continue from the same stream.
func measureTTFB(conn net.Conn, host, path string) (*bufio.Reader, QualityStageResult) {
	res := QualityStageResult{Stage: QStageTTFB}
	req := "GET " + path + " HTTP/1.1\r\nHost: " + host +
		"\r\nUser-Agent: Mozilla/5.0\r\nAccept: */*\r\n\r\n"
	_ = conn.SetWriteDeadline(time.Now().Add(doctorDialTimeout))
	if _, err := conn.Write([]byte(req)); err != nil {
		res.Detail = "request: " + err.Error()
		return nil, res
	}
	br := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(qualityTTFBBudget))
	start := time.Now()
	line, err := br.ReadString('\n')
	res.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Detail = "response: " + err.Error()
		return nil, res
	}
	res.OK = true
	res.Detail = fmt.Sprintf("HTTP %d", parseHTTPStatus(line))
	res.Anomalous = res.DurationMs > qualityTTFBSlow.Milliseconds()
	return br, res
}

// measureThroughput drains the remaining response headers, then reads the body for
// a bounded window and reports KB/s. A window that flows no bytes (the stream
// stalled) is a failure; a low but nonzero rate is anomalous.
func measureThroughput(conn net.Conn, br *bufio.Reader) QualityStageResult {
	res := QualityStageResult{Stage: QStageThroughput}
	// Drain the rest of the response headers (tiny) so the sample times the BODY.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			res.Detail = "headers: " + err.Error()
			return res
		}
		if strings.TrimRight(line, "\r\n") == "" {
			break
		}
	}
	deadline := time.Now().Add(qualityThroughputWindow)
	_ = conn.SetReadDeadline(deadline)
	buf := make([]byte, 32*1024)
	var total int
	start := time.Now()
	for time.Now().Before(deadline) {
		n, err := br.Read(buf)
		total += n
		if err != nil {
			if errors.Is(err, io.EOF) {
				break // whole body arrived within the window
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				break // window elapsed — expected for a large body
			}
			res.Detail = "read: " + err.Error()
			if total == 0 {
				return res
			}
			break
		}
	}
	elapsed := time.Since(start)
	res.DurationMs = elapsed.Milliseconds()
	if total == 0 {
		res.Detail = "no data in the sample window (stalled)"
		return res
	}
	var kbps int
	if elapsed > 0 {
		kbps = int(float64(total) / 1024 / elapsed.Seconds())
	}
	res.OK = true
	res.Detail = fmt.Sprintf("%d KB/s (%d KB sampled)", kbps, total/1024)
	res.Anomalous = kbps < qualityThroughputFloorKBps
	return res
}

// worstAnomaly attributes the bottleneck: the slowest anomalous latency stage
// (dns/tcp/tls/ttfb by duration), or throughput when it is the only anomaly.
func worstAnomaly(stages []QualityStageResult) QualityStage {
	var worst QualityStage
	var worstMs int64 = -1
	for _, s := range stages {
		if !s.Anomalous {
			continue
		}
		if s.Stage == QStageThroughput {
			if worst == "" {
				worst = s.Stage
			}
			continue
		}
		if s.DurationMs > worstMs {
			worstMs = s.DurationMs
			worst = s.Stage
		}
	}
	return worst
}

// dnsUpstreamLabel renders an upstream as "DoT 8.8.8.8" / "DoH 1.1.1.1" /
// "plain DNS 9.9.9.9".
func dnsUpstreamLabel(up DNSUpstream) string {
	kind := up.Type
	switch up.Type {
	case "tls":
		kind = "DoT"
	case "https":
		kind = "DoH"
	case "udp":
		kind = "plain DNS"
	}
	return kind + " " + up.Address
}

// setConnDeadline pins conn to the context deadline (or the default dial budget).
func setConnDeadline(ctx context.Context, conn net.Conn) {
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
		return
	}
	_ = conn.SetDeadline(time.Now().Add(doctorDialTimeout))
}
