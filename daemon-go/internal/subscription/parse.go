// Body-format detection and parsing. Providers serve subscriptions in several
// dialects with NO in-band declaration (only SIP008 even carries a version
// field), and the User-Agent negotiation is advisory — panels are free to
// serve one fixed dialect to everyone. So the daemon detects by content, with
// two guards that keep detection honest:
//
//   - a parser "matches" only when the body is structurally its dialect AND
//     at least one node survives extraction — a mere JSON/YAML decode is no
//     signal (encoding/json ignores unknown fields, so an xray body decodes
//     "successfully" into sing-box shapes with everything zero);
//   - a structural match with zero usable nodes is a refresh ERROR, never an
//     empty success, and never a fall-through to a laxer parser.
//
// The per-subscription Format pin is the user's escape hatch when detection
// misreads a provider.
//
// Every format parser reduces its dialect to standard share links and the
// chain funnels them through proxy.ParseURL — one battle-tested field parser
// per protocol, whatever the source dialect.
package subscription

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"ironlink/daemon/internal/proxy"
	"ironlink/daemon/internal/store"
)

// durationSeconds parses a Go/xray-style duration string ("3m", "90s", "1h30m")
// into whole seconds for a GroupProbe. An empty or unparseable value yields 0
// (the group then falls back to the sing-box default interval). Shared by the
// group extractors that carry their probe interval as a duration string.
func durationSeconds(s string) uint32 {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return uint32(d.Seconds())
}

// Format identifies a subscription body dialect. FormatAuto walks the chain;
// the rest force exactly one parser.
type Format string

const (
	FormatAuto    Format = "auto"
	FormatLinks   Format = "links"
	FormatXray    Format = "xray"
	FormatSingBox Format = "sing-box"
	FormatClash   Format = "clash"
	FormatSIP008  Format = "sip008"
)

// ParseFormat validates a wire/store format string; "" is auto (a stored
// subscription that predates the field).
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case "", FormatAuto:
		return FormatAuto, nil
	case FormatLinks, FormatXray, FormatSingBox, FormatClash, FormatSIP008:
		return Format(s), nil
	}
	return "", fmt.Errorf("unknown subscription format %q (auto / links / xray / sing-box / clash / sip008)", s)
}

// rawEntry is one foreign document entry reduced to candidate share links —
// the common currency every format parser emits. A multi-outbound entry (an
// xray balancer config) yields several links; the converter has already put
// each node's display name into its link fragment. An entry whose protocol we
// cannot express yields zero links and is counted unrecognized.
//
// fromBalancer marks an entry whose links are a balancer's member expansion
// (even a single-member one). Such members dedup in the SECOND pass so a
// dedicated per-node entry always wins the display name, and they are never
// mistaken for a standalone carrier-host placeholder.
type rawEntry struct {
	links        []string
	fromBalancer bool
}

// rawGroup is one foreign auto-select group (an xray balancer / clash url-test /
// sing-box urltest) reduced to its member share links plus the probe tuning.
// The links point at member nodes that also appear in the document's entries;
// collect freezes them to the deduped survivors. name is the group's label.
type rawGroup struct {
	name  string
	links []string
	probe store.GroupProbe
}

// formatParser converts a body into entries AND groups. ok=false means the body
// is not this dialect at all (wrong syntax or shape) and the chain moves on. A
// dialect with no group concept (links, SIP008) returns nil groups.
type formatParser func(body string) (entries []rawEntry, groups []rawGroup, ok bool)

// GroupOut is one extracted group after collect has frozen its membership: the
// name, the refresh-identity keys of its members (resolved to the deduped
// survivors), and the probe tuning. The refresh materializes it into a group
// store.Node once the member nodes have their stable UUIDs.
type GroupOut struct {
	Name       string
	MemberKeys []prefsKey
	Probe      store.GroupProbe
}

// Outcome is one body's parse result: the dialect that matched plus the
// accounting the refresh report surfaces (so "provider sent 50, we understood
// 3" is visible, not silent).
type Outcome struct {
	Format       Format
	Nodes        []store.Node
	Groups       []GroupOut
	Entries      int // entries in the document
	Duplicates   int // links dropped by the (protocol, identity, endpoint) dedup
	Unrecognized int // entries no parser understood
}

// chain is the auto-detection order. The JSON dialects run before clash
// (YAML is a superset of JSON, so a clash attempt on a JSON body must never
// come first) and links run last (their base64 branch accepts almost
// anything).
var chain = []struct {
	format Format
	parse  formatParser
}{
	{FormatSingBox, parseSingBox},
	{FormatXray, parseXray},
	{FormatSIP008, parseSIP008},
	{FormatClash, parseClash},
	{FormatLinks, parseLinks},
}

// Parse decodes a subscription body into nodes owned by subID. On error
// (nothing matched, or the matching dialect yielded zero usable nodes) the
// caller must leave its stored nodes untouched — a failed parse is a failed
// refresh, never an empty success.
func Parse(body, subID string, format Format) (*Outcome, error) {
	body = strings.TrimPrefix(body, "\uFEFF")
	for _, c := range chain {
		if format != FormatAuto && format != c.format {
			continue
		}
		entries, groups, ok := c.parse(body)
		if !ok {
			if format == c.format {
				return nil, fmt.Errorf("subscription body is not %s", c.format)
			}
			continue
		}
		o := collect(c.format, entries, groups, subID)
		if len(o.Nodes) == 0 {
			return nil, fmt.Errorf("%s subscription: no usable nodes in %d entries (%d unrecognized)",
				c.format, o.Entries, o.Unrecognized)
		}
		return o, nil
	}
	return nil, fmt.Errorf("unrecognized subscription format (not share links, base64, xray/sing-box JSON, SIP008 or clash YAML)")
}

// collect runs every candidate link through the protocol registry, dedups by
// the refresh identity key and wraps the survivors into store nodes.
//
// Dedup is two-pass: single-link entries first, then multi-link (balancer)
// expansions. Providers list the same server both as a dedicated entry and as
// a balancer member, and first-seen wins the dedup — one pass in document
// order would let a leading balancer claim the identity and leave the node
// named "<balancer> · <ip>" instead of the dedicated entry's proper name.
// Which links survive changes, the duplicate COUNT does not.
func collect(format Format, entries []rawEntry, groups []rawGroup, subID string) *Outcome {
	o := &Outcome{Format: format, Entries: len(entries)}
	groupNames := map[string]bool{}
	for _, g := range groups {
		groupNames[g.name] = true
	}
	parsed := make([][]proxy.Profile, len(entries))
	for i, e := range entries {
		for _, link := range e.links {
			p, err := proxy.ParseURL(link)
			if err != nil {
				continue
			}
			parsed[i] = append(parsed[i], p)
		}
		if len(parsed[i]) == 0 {
			o.Unrecognized++
		}
	}
	seen := map[prefsKey]bool{}
	add := func(p proxy.Profile) {
		key := profileKey(p)
		if seen[key] {
			o.Duplicates++
			return
		}
		seen[key] = true
		o.Nodes = append(o.Nodes, store.NewNode(p, &subID))
	}
	// Pass 1, dedicated single-link entries (NOT balancer expansions). A dedicated
	// entry whose node name equals a group name is the group's CARRIER HOST (the
	// placeholder a provider needs so a balancer config has a host to attach to)
	// — not a real node. Drop it from the node stream AND from `seen`, so it can
	// never be frozen as a group member either. Balancer members are handled in
	// pass 2, so a single-member balancer is never mistaken for a carrier and
	// always dedups UNDER a dedicated entry's clean name.
	for i, profiles := range parsed {
		if entries[i].fromBalancer || len(profiles) != 1 {
			continue
		}
		if groupNames[profiles[0].DisplayName()] {
			continue
		}
		add(profiles[0])
	}
	for i, profiles := range parsed {
		if !entries[i].fromBalancer && len(profiles) <= 1 {
			continue
		}
		for _, p := range profiles {
			add(p)
		}
	}
	// Freeze each group's membership to the deduped survivors: re-parse its
	// member links, keep only the keys that actually became nodes (seen), dedup
	// within the group. A group with no resolvable members is dropped; on a
	// duplicate group name the first wins.
	names := map[string]bool{}
	for _, g := range groups {
		if names[g.name] {
			continue
		}
		var keys []prefsKey
		gseen := map[prefsKey]bool{}
		for _, link := range g.links {
			p, err := proxy.ParseURL(link)
			if err != nil {
				continue
			}
			key := profileKey(p)
			if !seen[key] || gseen[key] {
				continue
			}
			gseen[key] = true
			keys = append(keys, key)
		}
		if len(keys) == 0 {
			continue
		}
		names[g.name] = true
		o.Groups = append(o.Groups, GroupOut{Name: g.name, MemberKeys: keys, Probe: g.probe})
	}
	return o
}

// parseLinks handles the classic pair: a plain newline list of share links,
// optionally wrapped in whole-body base64 (any of the four alphabets —
// providers use all of them, padded and not). Lines without a scheme are
// comments/blanks, not entries.
func parseLinks(body string) ([]rawEntry, []rawGroup, bool) {
	text := body
	if decoded, ok := decodeBase64Body(body); ok {
		text = decoded
	}
	var entries []rawEntry
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\uFEFF"))
		if line == "" || !strings.Contains(line, "://") {
			continue
		}
		entries = append(entries, rawEntry{links: []string{line}})
	}
	// A flat link list carries no group concept.
	return entries, nil, len(entries) > 0
}

// decodeBase64Body tries the four base64 alphabets over the whole body,
// accepting a decode that looks like a link list. Go's decoder skips \r\n on
// its own, so 76-column-wrapped bodies decode as-is.
func decodeBase64Body(body string) (string, bool) {
	trimmed := strings.TrimSpace(body)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		decoded, err := enc.DecodeString(trimmed)
		if err != nil {
			continue
		}
		if s := string(decoded); strings.Contains(s, "://") {
			return s, true
		}
	}
	return "", false
}
