//go:build with_clash_api

// The log-sink half of the §6 instrumentation: sing-box couples the
// PlatformLogWriter hook to the clash-api build tag (see logtap_clash.go), so
// this test runs under the canonical tag set only. The config pins the
// cache-file path (the hook forces the cache-file service) into the test dir.
package engine

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// TestLogSinkReceivesBoxLines: a session built with a LogSink gets the box's
// startup lines (debug level so there is guaranteed output).
func TestLogSinkReceivesBoxLines(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	sink := func(level, message string) {
		mu.Lock()
		lines = append(lines, level+": "+message)
		mu.Unlock()
	}

	sbCfg := []byte(fmt.Sprintf(`{
      "log": {"level":"debug"},
      "experimental": {"cache_file": {"enabled": true, "path": %q}},
      "inbounds": [{"type":"socks","tag":"socks-in","listen":"127.0.0.1","listen_port":%d}],
      "outbounds": [{"type":"direct","tag":"direct"}],
      "route": {"final":"direct"}
    }`, filepath.Join(t.TempDir(), "cache.db"), freePort(t)))
	xrayCfg := []byte(`{
      "log": {"loglevel":"warning"},
      "inbounds": [{"listen":"@il-logsink-stub","protocol":"socks","settings":{"udp":false}}],
      "outbounds": [{"protocol":"freedom","tag":"direct"}]
    }`)

	sess, err := StartWithOptions(sbCfg, xrayCfg, StartOptions{LogSink: sink})
	if err != nil {
		t.Fatalf("StartWithOptions: %v", err)
	}
	sess.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(lines) == 0 {
		t.Fatal("the log sink received no lines from a debug-level box")
	}
}
