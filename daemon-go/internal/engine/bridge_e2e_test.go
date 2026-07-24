package engine

import (
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

// TestBridgeViaSocks proves the in-memory bridge carries REAL traffic + DNS with
// NO TUN and NO root: a sing-box SOCKS inbound -> route -> xray-reality
// (core.Dial) -> xray freedom -> the real internet. This is the daemon-go-native
// proof of the spike's 1b/1c claim (the cross-core hop dispatches live traffic),
// runnable anywhere with network egress.
func TestBridgeViaSocks(t *testing.T) {
	port := freePort(t)
	sbCfg, xrayCfg := SocksValidationConfigs("127.0.0.1", port)

	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatalf("Start (socks inbound, no root): %v", err)
	}
	defer sess.Close()

	d, err := proxy.SOCKS5("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil, proxy.Direct)
	if err != nil {
		t.Fatalf("socks5 dialer: %v", err)
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		t.Fatal("socks dialer does not implement ContextDialer")
	}
	client := &http.Client{
		Transport: &http.Transport{DialContext: cd.DialContext},
		Timeout:   15 * time.Second,
	}

	resp, err := client.Get("https://www.gstatic.com/generate_204")
	if err != nil {
		t.Fatalf("204 probe through bridge failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want HTTP 204 through sing-box -> core.Dial -> xray, got %d", resp.StatusCode)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
