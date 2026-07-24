package main

import (
	"strings"
	"testing"

	"ironlink/daemon/internal/engine"
)

func TestForwardingCheck(t *testing.T) {
	t.Run("ufw deny + bridges -> warn with text remedy, no Remedy struct", func(t *testing.T) {
		c, ok := forwardingCheck(engine.ForwardingReport{
			Linux: true, UFWActive: true, InputDeny: true,
			PolicyDetail: "DROP", Bridges: []string{"docker0"},
		})
		if !ok {
			t.Fatal("expected the check to be included on Linux")
		}
		if c.Status != "warn" {
			t.Fatalf("status = %q, want warn", c.Status)
		}
		if !strings.Contains(c.Summary, "docker0") {
			t.Errorf("summary should name the bridge: %q", c.Summary)
		}
		joined := strings.Join(c.Details, "\n")
		if !strings.Contains(joined, "--network=host") {
			t.Errorf("details should offer the --network=host fix:\n%s", joined)
		}
		if !strings.Contains(joined, `DEFAULT_INPUT_POLICY="DROP"`) {
			t.Errorf("details should cite the raw policy:\n%s", joined)
		}
		if c.Remedy != nil {
			t.Error("firewall fixes are never auto-applied; Remedy must stay nil")
		}
	})

	t.Run("ufw deny but no bridges -> ok", func(t *testing.T) {
		c, ok := forwardingCheck(engine.ForwardingReport{
			Linux: true, UFWActive: true, InputDeny: true,
		})
		if !ok || c.Status != "ok" {
			t.Fatalf("ok=%v status=%q, want true/ok", ok, c.Status)
		}
	})

	t.Run("bridges but ufw inactive -> ok", func(t *testing.T) {
		c, ok := forwardingCheck(engine.ForwardingReport{
			Linux: true, Bridges: []string{"docker0", "br-1a2b"},
		})
		if !ok || c.Status != "ok" {
			t.Fatalf("ok=%v status=%q, want true/ok", ok, c.Status)
		}
	})

	t.Run("ufw active but INPUT not default-deny -> ok", func(t *testing.T) {
		c, ok := forwardingCheck(engine.ForwardingReport{
			Linux: true, UFWActive: true, InputDeny: false, Bridges: []string{"docker0"},
		})
		if !ok || c.Status != "ok" {
			t.Fatalf("ok=%v status=%q, want true/ok", ok, c.Status)
		}
	})

	t.Run("non-linux -> excluded", func(t *testing.T) {
		if _, ok := forwardingCheck(engine.ForwardingReport{Linux: false}); ok {
			t.Error("the check must be omitted off Linux")
		}
	})
}
