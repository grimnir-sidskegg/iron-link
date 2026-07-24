package ipc

import (
	"errors"
	"net"

	"ironlink/daemon/internal/api"
)

// Handler processes one control request and returns its reply.
//
// It never sees CmdSubscribe: the server handles that verb itself by streaming
// events from the Hub, because a subscription is a different protocol (one
// request, many server-pushed frames) from request/response.
type Handler interface {
	Handle(req api.Request) api.Response
}

// HandlerFunc adapts a plain func to Handler.
type HandlerFunc func(api.Request) api.Response

func (f HandlerFunc) Handle(req api.Request) api.Response { return f(req) }

// Server serves the control protocol over a stream listener (a UDS on
// Linux/macOS, a named pipe on Windows). Peer authentication wraps the listener
// (added in the next G1 step); the Server itself is transport-agnostic.
type Server struct {
	handler Handler
	hub     *Hub
}

// NewServer builds a Server dispatching control verbs to handler and streaming
// events from hub to Subscribe connections.
func NewServer(handler Handler, hub *Hub) *Server {
	return &Server{handler: handler, hub: hub}
}

// Serve accepts connections until the listener is closed. Each connection is one
// request/response, except Subscribe which streams events until the client
// disconnects. Returns nil on a clean listener close.
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	var req api.Request
	if err := ReadFrame(conn, &req); err != nil {
		return // peer closed or sent garbage — nothing to reply to
	}

	if req.Command == api.CmdSubscribe {
		s.stream(conn)
		return
	}

	_ = WriteFrame(conn, s.handler.Handle(req))
}

// stream sends the initial State snapshot, then every Hub event, until the
// client disconnects.
func (s *Server) stream(conn net.Conn) {
	ch, cancel := s.hub.subscribe()
	defer cancel()

	if err := WriteFrame(conn, s.hub.Snapshot()); err != nil {
		return
	}

	// A subscribe connection is one-way (daemon -> client) after the request, so
	// a read error means the client went away — use it as the disconnect signal.
	done := make(chan struct{})
	go func() {
		var scratch [1]byte
		for {
			if _, err := conn.Read(scratch[:]); err != nil {
				break
			}
		}
		close(done)
	}()

	for {
		select {
		case ev := <-ch:
			if err := WriteFrame(conn, ev); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}
