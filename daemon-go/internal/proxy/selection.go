// Per-node core selection.
//
// A node is dialed by a core chosen from those whose capabilities cover its
// (protocol, transport, security, flow). Hard constraints win (a transport
// only one core speaks forces that core, e.g. xhttp → xray), otherwise the
// active TUN engine is the default, and a user override always wins within
// the eligible set.

package proxy

import (
	"fmt"
	"slices"

	"ironlink/daemon/internal/api"
)

// corePriority is the deterministic core order for the fallback case (rule 5
// of SelectCore) and the stable ordering of EligibleCores. sing-box is
// preferred over xray because it is the TUN-capable core with free
// selector/urltest; xray is the specialist used when only it can dial the
// node (xhttp).
var corePriority = []api.CoreType{api.CoreSingBox, api.CoreXray}

// EligibleCores returns all cores that can dial p, in corePriority order. The
// per-protocol verdict lives in each Profile's dialableBy, so this is
// protocol-agnostic — adding a protocol never edits selection.
func EligibleCores(p Profile) []api.CoreType {
	if p == nil { // a group node has no proxy profile — no core dials it
		return nil
	}
	var eligible []api.CoreType
	for _, c := range corePriority {
		if p.dialableBy(c) {
			eligible = append(eligible, c)
		}
	}
	return eligible
}

// SelectCore picks the core that will dial p. override == nil means "let
// selection decide". Rule order (exact):
//
//  1. compute eligible; if empty → error (no installed core can dial it).
//  2. if override is set: return it if eligible, else error.
//  3. if exactly one core is eligible → return it (the forced case, e.g.
//     xhttp → xray).
//  4. if tunEngine is eligible → return it (default to the TUN core, which
//     yields a unified plan + free selector/urltest).
//  5. otherwise → deterministic fallback: the first eligible core in
//     corePriority order.
func SelectCore(p Profile, tunEngine api.CoreType, override *api.CoreType) (api.CoreType, error) {
	if p == nil {
		return "", fmt.Errorf("a group node has no proxy profile to select a core for")
	}
	core, err := selectFromEligible(EligibleCores(p), tunEngine, override)
	if err != nil {
		return "", fmt.Errorf("%w (%s)", err, p.DisplayName())
	}
	return core, nil
}

// selectFromEligible is the pure selection rule over a precomputed eligible
// set (in corePriority order). Separated from SelectCore so every branch —
// including the empty-eligible error — is unit-testable without constructing
// an (otherwise impossible) undialable node.
func selectFromEligible(eligible []api.CoreType, tunEngine api.CoreType, override *api.CoreType) (api.CoreType, error) {
	// 1. Nothing can dial this node.
	if len(eligible) == 0 {
		return "", fmt.Errorf("no installed core can dial this node")
	}

	// 2. User override wins, but only within the eligible set.
	if override != nil {
		if slices.Contains(eligible, *override) {
			return *override, nil
		}
		return "", fmt.Errorf("requested core %s cannot dial this node", *override)
	}

	// 3. Hard constraint: a single eligible core is forced.
	if len(eligible) == 1 {
		return eligible[0], nil
	}

	// 4. Default to the active TUN engine when it is eligible.
	if slices.Contains(eligible, tunEngine) {
		return tunEngine, nil
	}

	// 5. Deterministic fallback: first eligible core by priority. eligible is
	//    already in corePriority order and non-empty. (Unreachable with only
	//    two core variants — documented safety net for a third core.)
	return eligible[0], nil
}
