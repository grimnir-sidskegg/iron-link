package ipc

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"ironlink/daemon/internal/api"
)

func strptr(s string) *string { return &s }
func u16ptr(v uint16) *uint16 { return &v }

// TestFrameRoundTrip pushes representative messages through WriteFrame/ReadFrame
// and asserts they come back identical — the codec is the load-bearing seam
// every client and the daemon share.
func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer

	tun := true
	reqIn := api.Request{Command: api.CmdActivate, Profile: strptr("home"), Node: strptr("de-1"), Tun: &tun}
	respIn := api.Response{
		Status:         api.StatusRunning,
		Entries:        []api.CoreEntry{{Role: api.RoleTun, State: api.StateRunning, UptimeSecs: 42}},
		Active:         &api.PersistedEntry{Profile: strptr("home"), Node: strptr("de-1"), Tun: true},
		ActiveNodeLive: strptr("de-1"),
	}
	evtIn := api.Event{
		Event:   api.EventState,
		Entries: []api.CoreEntry{{Role: api.RoleProxy, State: api.StateRunning, UptimeSecs: 1}},
	}

	for _, v := range []any{reqIn, respIn, evtIn} {
		if err := WriteFrame(&buf, v); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}

	var reqOut api.Request
	if err := ReadFrame(&buf, &reqOut); err != nil {
		t.Fatalf("ReadFrame req: %v", err)
	}
	if reqOut.Command != api.CmdActivate || reqOut.Node == nil || *reqOut.Node != "de-1" || reqOut.Tun == nil || !*reqOut.Tun {
		t.Fatalf("request round-trip mismatch: %+v", reqOut)
	}

	var respOut api.Response
	if err := ReadFrame(&buf, &respOut); err != nil {
		t.Fatalf("ReadFrame resp: %v", err)
	}
	if respOut.Status != api.StatusRunning || len(respOut.Entries) != 1 || respOut.Entries[0].Role != api.RoleTun {
		t.Fatalf("response round-trip mismatch: %+v", respOut)
	}
	if respOut.ActiveNodeLive == nil || *respOut.ActiveNodeLive != "de-1" {
		t.Fatalf("active_node_live lost: %+v", respOut)
	}

	var evtOut api.Event
	if err := ReadFrame(&buf, &evtOut); err != nil {
		t.Fatalf("ReadFrame evt: %v", err)
	}
	if evtOut.Event != api.EventState || len(evtOut.Entries) != 1 {
		t.Fatalf("event round-trip mismatch: %+v", evtOut)
	}
}

// TestLatencyResultWireShape pins the intentional object encoding (vs a Rust
// tuple): {"node":…,"latency_ms":…|null}. A nil LatencyMs must serialize null.
func TestLatencyResultWireShape(t *testing.T) {
	got, err := json.Marshal(api.LatencyResult{Node: "de-1", LatencyMs: u16ptr(73)})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"node":"de-1","latency_ms":73}` {
		t.Fatalf("unexpected ok shape: %s", got)
	}
	got, err = json.Marshal(api.LatencyResult{Node: "de-2", LatencyMs: nil})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"node":"de-2","latency_ms":null}` {
		t.Fatalf("unexpected timeout shape: %s", got)
	}
}

// TestReadFrameCleanEOF: a clean close between frames surfaces as io.EOF.
func TestReadFrameCleanEOF(t *testing.T) {
	var empty bytes.Buffer
	var v api.Request
	if err := ReadFrame(&empty, &v); err != io.EOF {
		t.Fatalf("want io.EOF on empty stream, got %v", err)
	}
}
