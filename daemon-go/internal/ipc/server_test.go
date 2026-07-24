package ipc

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"ironlink/daemon/internal/api"
)

func dialUnix(t *testing.T, path string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	return conn
}

func startServer(t *testing.T, h Handler, hub *Hub) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := NewServer(h, hub)
	go srv.Serve(l)
	t.Cleanup(func() { l.Close() })
	return path
}

// TestServerDispatch: a Status request is dispatched to the Handler and its
// reply comes back over the framing.
func TestServerDispatch(t *testing.T) {
	h := HandlerFunc(func(req api.Request) api.Response {
		if req.Command != api.CmdStatus {
			return api.Response{Status: api.StatusError, Message: "unexpected verb"}
		}
		return api.Response{Status: api.StatusIdle}
	})
	path := startServer(t, h, NewHub(func() api.Event { return api.Event{Event: api.EventState} }))

	conn := dialUnix(t, path)
	defer conn.Close()
	if err := WriteFrame(conn, api.Request{Command: api.CmdStatus}); err != nil {
		t.Fatal(err)
	}
	var resp api.Response
	if err := ReadFrame(conn, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != api.StatusIdle {
		t.Fatalf("want idle, got %+v", resp)
	}
}

// TestServerSubscribe: Subscribe gets the State snapshot first, then live
// broadcast events.
func TestServerSubscribe(t *testing.T) {
	entries := []api.CoreEntry{{Role: api.RoleTun, State: api.StateRunning, UptimeSecs: 3}}
	hub := NewHub(func() api.Event { return api.Event{Event: api.EventState, Entries: entries} })
	path := startServer(t, HandlerFunc(func(api.Request) api.Response { return api.Response{Status: api.StatusOk} }), hub)

	conn := dialUnix(t, path)
	defer conn.Close()
	if err := WriteFrame(conn, api.Request{Command: api.CmdSubscribe}); err != nil {
		t.Fatal(err)
	}

	// first frame: the State snapshot
	var snap api.Event
	if err := ReadFrame(conn, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Event != api.EventState || len(snap.Entries) != 1 {
		t.Fatalf("want state snapshot, got %+v", snap)
	}

	// the subscriber is now registered; a broadcast must reach it
	waitFor(t, func() bool { return hub.SubscriberCount() == 1 })
	up, down := uint64(10), uint64(20)
	hub.Broadcast(api.Event{Event: api.EventTraffic, Up: &up, Down: &down})

	var ev api.Event
	if err := ReadFrame(conn, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Event != api.EventTraffic || ev.Up == nil || *ev.Up != 10 {
		t.Fatalf("want traffic event, got %+v", ev)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}
