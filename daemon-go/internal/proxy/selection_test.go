package proxy

import (
	"slices"
	"strings"
	"testing"

	"ironlink/daemon/internal/api"
)

func corePtr(c api.CoreType) *api.CoreType { return &c }

func TestXhttpNodeIsForcedToXrayEvenWithSingboxTunEngine(t *testing.T) {
	n := vlessNode(xhttp(), reality(), "")
	// No override, tun_engine = sing-box: still forced to xray because it is
	// the only eligible core.
	core, err := SelectCore(n, api.CoreSingBox, nil)
	if err != nil {
		t.Fatalf("SelectCore: %v", err)
	}
	if core != api.CoreXray {
		t.Errorf("core = %s, want Xray", core)
	}
	if got := EligibleCores(n); !slices.Equal(got, []api.CoreType{api.CoreXray}) {
		t.Errorf("EligibleCores = %v, want [Xray]", got)
	}
}

func TestGrpcRealityNodeDefaultsToTunEngine(t *testing.T) {
	n := vlessNode(grpc(), reality(), "")
	// Both cores eligible; default to the TUN engine.
	core, err := SelectCore(n, api.CoreSingBox, nil)
	if err != nil {
		t.Fatalf("SelectCore: %v", err)
	}
	if core != api.CoreSingBox {
		t.Errorf("core = %s, want SingBox", core)
	}
}

func TestGrpcRealityNodeOverrideToXrayWins(t *testing.T) {
	n := vlessNode(grpc(), reality(), "")
	core, err := SelectCore(n, api.CoreSingBox, corePtr(api.CoreXray))
	if err != nil {
		t.Fatalf("SelectCore: %v", err)
	}
	if core != api.CoreXray {
		t.Errorf("core = %s, want Xray", core)
	}
}

func TestOverrideToCoreThatCannotDialIsRejected(t *testing.T) {
	// xhttp node, override to sing-box (which cannot dial xhttp) → error.
	n := vlessNode(xhttp(), reality(), "")
	_, err := SelectCore(n, api.CoreSingBox, corePtr(api.CoreSingBox))
	if err == nil || !strings.Contains(err.Error(), "cannot dial") {
		t.Errorf("err = %v, want 'cannot dial'", err)
	}
}

func TestSelectErrorCarriesDisplayName(t *testing.T) {
	n := vlessNode(xhttp(), reality(), "")
	_, err := SelectCore(n, api.CoreSingBox, corePtr(api.CoreSingBox))
	if err == nil || !strings.Contains(err.Error(), n.DisplayName()) {
		t.Errorf("err = %v, want the node display name in the message", err)
	}
}

func TestEmptyEligibleSetIsAnError(t *testing.T) {
	// No installed core can dial the node → error. Tested via the pure rule
	// fn with an empty eligible set, because the current model can't express
	// an undialable node.
	_, err := selectFromEligible(nil, api.CoreSingBox, nil)
	if err == nil || !strings.Contains(err.Error(), "no installed core") {
		t.Errorf("err = %v, want 'no installed core'", err)
	}
}

func TestRule4DefaultsToXrayWhenItIsTheTunEngine(t *testing.T) {
	// Both cores eligible, no override, tun_engine = xray → rule 4 returns
	// xray (the active TUN engine), distinguishing rule 4 from the rule-5
	// fallback which would return sing-box (first by priority).
	eligible := EligibleCores(vlessNode(grpc(), reality(), ""))
	if !slices.Equal(eligible, []api.CoreType{api.CoreSingBox, api.CoreXray}) {
		t.Fatalf("EligibleCores = %v, want [SingBox, Xray]", eligible)
	}
	core, err := selectFromEligible(eligible, api.CoreXray, nil)
	if err != nil {
		t.Fatalf("selectFromEligible: %v", err)
	}
	if core != api.CoreXray {
		t.Errorf("core = %s, want Xray", core)
	}
}
