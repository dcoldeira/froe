package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/dcoldeira/froe/internal/perms"
)

// Handler performs the work behind a method. It lives outside this package so
// rpc stays transport-only.
type Handler interface {
	Initialize(ctx context.Context, p InitializeParams) (*InitializeResult, error)
	Run(ctx context.Context, p RunParams, emit func(EventParams)) (*RunResult, error)
}

// Server speaks the protocol over a reader/writer pair.
type Server struct {
	in      *bufio.Scanner
	out     io.Writer
	handler Handler

	writeMu sync.Mutex

	// cancel stops the run in flight. Guarded because it is set by the run
	// goroutine and read by the reader goroutine handling cancel.
	runMu  sync.Mutex
	cancel context.CancelFunc

	// pending maps an outbound request id to the channel awaiting its reply.
	pendMu  sync.Mutex
	pending map[int64]chan *Message
	nextID  int64
}

// NewServer builds a server. in is typically stdin, out stdout.
func NewServer(in io.Reader, out io.Writer, h Handler) *Server {
	sc := bufio.NewScanner(in)
	// Selections and tool results can be large; the 64KB default is not enough.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return &Server{in: sc, out: out, handler: h, pending: map[int64]chan *Message{}}
}

// Serve reads messages until the stream ends.
func (s *Server) Serve(ctx context.Context) error {
	for s.in.Scan() {
		line := s.in.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			s.writeError(nil, CodeParse, "invalid JSON: "+err.Error())
			continue
		}

		// A reply to something we asked the editor.
		if msg.Method == "" && msg.ID != nil {
			s.deliver(&msg)
			continue
		}
		s.dispatch(ctx, &msg)
	}
	return s.in.Err()
}

func (s *Server) dispatch(ctx context.Context, msg *Message) {
	switch msg.Method {
	case "initialize":
		var p InitializeParams
		if err := s.decode(msg, &p); err != nil {
			return
		}
		res, err := s.handler.Initialize(ctx, p)
		s.reply(msg.ID, res, err)

	case "run":
		var p RunParams
		if err := s.decode(msg, &p); err != nil {
			return
		}
		// Run in its own goroutine so cancel and approval replies can still be
		// read while it is in flight.
		go s.runTask(ctx, msg.ID, p)

	case "cancel":
		s.runMu.Lock()
		if s.cancel != nil {
			s.cancel()
		}
		s.runMu.Unlock()
		s.reply(msg.ID, map[string]bool{"cancelled": true}, nil)

	case "shutdown":
		s.reply(msg.ID, map[string]bool{"ok": true}, nil)

	default:
		if msg.ID != nil {
			s.writeError(msg.ID, CodeMethodNotFound, "unknown method "+msg.Method)
		}
	}
}

func (s *Server) runTask(ctx context.Context, id *int64, p RunParams) {
	runCtx, cancel := context.WithCancel(ctx)
	s.runMu.Lock()
	s.cancel = cancel
	s.runMu.Unlock()
	defer func() {
		cancel()
		s.runMu.Lock()
		s.cancel = nil
		s.runMu.Unlock()
	}()

	emit := func(e EventParams) { s.notify("froe/event", e) }

	res, err := s.handler.Run(runCtx, p, emit)
	if err != nil && runCtx.Err() != nil {
		s.writeError(id, CodeCancelled, "cancelled")
		return
	}
	s.reply(id, res, err)
}

// Approve asks the editor to approve a mutating tool call and blocks for the
// answer. It satisfies perms.Asker.
func (s *Server) Approve(req perms.Request) perms.Decision {
	reply, err := s.request("froe/approve", ApproveParams{
		Tool: req.Tool, Summary: req.Summary, Detail: req.Detail,
	}, 10*time.Minute)
	if err != nil || reply == nil || reply.Error != nil {
		// An editor that cannot answer must not be treated as consent.
		return perms.Deny
	}
	var res ApproveResult
	if err := json.Unmarshal(reply.Result, &res); err != nil {
		return perms.Deny
	}
	switch res.Decision {
	case "allow":
		return perms.Allow
	case "always":
		return perms.AlwaysAllow
	default:
		return perms.Deny
	}
}

// Ask adapts Approve to the perms.Asker interface.
func (s *Server) Ask(req perms.Request) perms.Decision { return s.Approve(req) }

// request sends a server→client request and waits for its reply.
func (s *Server) request(method string, params any, timeout time.Duration) (*Message, error) {
	s.pendMu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan *Message, 1)
	s.pending[id] = ch
	s.pendMu.Unlock()

	defer func() {
		s.pendMu.Lock()
		delete(s.pending, id)
		s.pendMu.Unlock()
	}()

	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if err := s.write(Message{JSONRPC: "2.0", ID: &id, Method: method, Params: raw}); err != nil {
		return nil, err
	}

	select {
	case m := <-ch:
		return m, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("editor did not answer %s within %s", method, timeout)
	}
}

// deliver routes a reply to whoever is waiting for it.
func (s *Server) deliver(msg *Message) {
	s.pendMu.Lock()
	ch, ok := s.pending[*msg.ID]
	s.pendMu.Unlock()
	if ok {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (s *Server) decode(msg *Message, v any) error {
	if len(msg.Params) == 0 {
		return nil
	}
	if err := json.Unmarshal(msg.Params, v); err != nil {
		s.writeError(msg.ID, CodeInvalidParams, err.Error())
		return err
	}
	return nil
}

func (s *Server) reply(id *int64, result any, err error) {
	if id == nil {
		return // it was a notification; nothing is waiting
	}
	if err != nil {
		s.writeError(id, CodeInternal, err.Error())
		return
	}
	raw, mErr := json.Marshal(result)
	if mErr != nil {
		s.writeError(id, CodeInternal, mErr.Error())
		return
	}
	s.write(Message{JSONRPC: "2.0", ID: id, Result: raw})
}

func (s *Server) writeError(id *int64, code int, message string) {
	s.write(Message{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: message}})
}

func (s *Server) notify(method string, params any) {
	raw, err := json.Marshal(params)
	if err != nil {
		return
	}
	s.write(Message{JSONRPC: "2.0", Method: method, Params: raw})
}

// write emits one message as a single line. Serialised, because events are
// produced from the run goroutine while replies come from the reader.
func (s *Server) write(m Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.out.Write(append(b, '\n')); err != nil {
		return err
	}
	if f, ok := s.out.(interface{ Sync() error }); ok {
		_ = f.Sync()
	}
	return nil
}
