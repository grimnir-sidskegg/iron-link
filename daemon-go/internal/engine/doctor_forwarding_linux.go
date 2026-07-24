//go:build linux

package engine

import (
	"os"
	"path/filepath"
	"strings"
)

// ProbeForwarding inspects, READ-ONLY, whether the host firewall would drop the
// traffic the TUN's auto_redirect forwards from local bridges. It reads UFW's
// config files and /sys — no shell-out, no privilege beyond file reads, no state
// change (consistent with the no-OS-stack-mutation principle). See
// docs/docker-ufw-redirect.md.
func ProbeForwarding() ForwardingReport {
	r := ForwardingReport{Linux: true}
	if v, ok := confValue("/etc/ufw/ufw.conf", "ENABLED"); ok {
		r.UFWActive = strings.EqualFold(v, "yes")
	}
	if v, ok := confValue("/etc/default/ufw", "DEFAULT_INPUT_POLICY"); ok {
		r.PolicyDetail = v
		u := strings.ToUpper(v)
		r.InputDeny = u == "DROP" || u == "REJECT"
	}
	r.Bridges = bridgeInterfaces()
	return r
}

// confValue reads a shell-style `KEY=value` config file and returns the value
// for key (last assignment wins), trimmed of surrounding quotes and space.
// ok is false when the file is unreadable or the key is absent.
func confValue(path, key string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var (
		val   string
		found bool
	)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		val = strings.Trim(strings.TrimSpace(v), `"'`)
		found = true
	}
	return val, found
}

// bridgeInterfaces lists interfaces that are Linux bridges — a bridge has a
// /sys/class/net/<name>/bridge directory. docker0 and docker user-network br-*
// live here; these are the source interfaces whose forwarded traffic the TUN
// redirects. A bridge that is currently DOWN still counts (a build brings it up).
func bridgeInterfaces() []string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join("/sys/class/net", e.Name(), "bridge")); err == nil {
			out = append(out, e.Name())
		}
	}
	return out
}
