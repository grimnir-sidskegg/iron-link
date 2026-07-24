package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"ironlink/daemon/internal/engine"
	"ironlink/daemon/internal/store"
)

// TestXrayThroughput pulls a 10 MB file through STANDALONE xray (no sing-box,
// no bridge, no TUN) for each named node, to separate a broken/slow exit server
// from a broken in-process bridge: a node that crawls here is bad UPSTREAM of
// anything we build. MUST run with NO TUN active (else the upstream is captured
// and the numbers are meaningless). Env-driven so it works on any store:
//
//	IRON_LINK_XRAY_THROUGHPUT=1 IRON_LINK_CONFIG_DIR=$HOME/.config/iron-link \
//	  IRON_LINK_TP_PROFILE=main IRON_LINK_TP_NODES='Chicago,Chicago 2,Grimnir' \
//	  go test -tags "with_gvisor,with_utls,with_clash_api" \
//	  -run TestXrayThroughput -v ./cmd/iron-link-daemon/ -timeout 300s
func TestXrayThroughput(t *testing.T) {
	if os.Getenv("IRON_LINK_XRAY_THROUGHPUT") == "" {
		t.Skip("set IRON_LINK_XRAY_THROUGHPUT=1 (and stop any TUN) to run")
	}
	dir := os.Getenv("IRON_LINK_CONFIG_DIR")
	if dir == "" {
		t.Skip("set IRON_LINK_CONFIG_DIR")
	}
	profName := os.Getenv("IRON_LINK_TP_PROFILE")
	if profName == "" {
		profName = "main"
	}
	frags := strings.Split(os.Getenv("IRON_LINK_TP_NODES"), ",")

	prof, err := store.OpenAt(dir).LoadProfile(profName)
	if err != nil {
		t.Fatalf("load profile %q: %v", profName, err)
	}

	const url = "https://speed.cloudflare.com/__down?bytes=10000000" // 10 MB
	for _, frag := range frags {
		frag = strings.TrimSpace(frag)
		if frag == "" {
			continue
		}
		var node *store.Node
		for j := range prof.Nodes {
			if strings.Contains(prof.Nodes[j].DisplayName(), frag) {
				node = &prof.Nodes[j]
				break
			}
		}
		if node == nil {
			t.Logf("%-26s NOT FOUND", frag)
			continue
		}
		n, dur, err := engine.DownloadThroughXray(context.Background(), node.Vless(), "tp", url)
		kbps := float64(n) / 1024.0 / dur.Seconds()
		status := "OK"
		if err != nil {
			status = "ERR: " + err.Error()
		}
		t.Logf("%-26s %9d B in %6.1fs  (%7.0f KB/s)  %s", node.DisplayName(), n, dur.Seconds(), kbps, status)
	}
}
