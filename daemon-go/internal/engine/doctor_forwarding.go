package engine

// ForwardingReport is the read-only state behind the "Docker / LAN forwarding"
// doctor row. On Linux the TUN runs with auto_redirect, which redirects
// forwarded TCP (from docker0 / br-* bridges) to a LOCAL port — so it enters the
// host's INPUT chain. A default-deny INPUT firewall (UFW) then drops it, and
// container/LAN builds lose internet over TCP while DNS/ICMP still work (they
// take other paths). Full mechanism + proof: docs/docker-ufw-redirect.md.
//
// This report is just observations (UFW state + which bridges exist); the
// verdict is the pure forwardingCheck in the manager. Linux is false on other
// platforms — auto_redirect is Linux-only, so the check is not applicable.
type ForwardingReport struct {
	Linux        bool     // the probe ran on Linux
	UFWActive    bool     // UFW is enabled (/etc/ufw/ufw.conf ENABLED=yes)
	InputDeny    bool     // UFW DEFAULT_INPUT_POLICY is DROP or REJECT
	PolicyDetail string   // the raw DEFAULT_INPUT_POLICY value ("" when unknown)
	Bridges      []string // bridge interfaces present (docker0, br-*)
}
