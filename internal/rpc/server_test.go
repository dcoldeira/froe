package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dcoldeira/froe/internal/perms"
)

// fakeHandler drives the server without a model.
type fakeHandler struct {
	runFn func(ctx context.Context, p RunParams, emit func(EventParams)) (*RunResult, error)
}

func (f *fakeHandler) Initialize(ctx context.Context, p InitializeParams) (*InitializeResult, error) {
	return &InitializeResult{Version: Version, Model: "test", Root: p.Root, Tools: []string{"read_file"}}, nil
}

func (f *fakeHandler) Run(ctx context.Context, p RunParams, emit func(EventParams)) (*RunResult, error) {
	if f.runFn != nil {
		return f.runFn(ctx, p, emit)
	}
	emit(EventParams{Kind: "text", Text: "hello"})
	return &RunResult{Answer: "hello", Turns: 1}, nil
}

// harness wires a server to pipes and collects its output lines.
type harness struct {
	toServer   *io.PipeWriter
	lines      chan Message
	serverDone chan struct{}
	srv        *Server
}

func newHarness(t *testing.T, h Handler) *harness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	hs := &harness{toServer: inW, lines: make(chan Message, 64), serverDone: make(chan struct{})}
	hs.srv = NewServer(inR, outW, h)

	go func() {
		defer close(hs.serverDone)
		hs.srv.Serve(context.Background())
		outW.Close()
	}()
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			var m Message
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				hs.lines <- m
			}
		}
		close(hs.lines)
	}()
	t.Cleanup(func() { inW.Close() })
	return hs
}

func (h *harness) send(t *testing.T, raw string) {
	t.Helper()
	if _, err := io.WriteString(h.toServer, raw+"\n"); err != nil {
		t.Fatal(err)
	}
}

// next returns the next message, failing on timeout.
func (h *harness) next(t *testing.T) Message {
	t.Helper()
	select {
	case m, ok := <-h.lines:
		if !ok {
			t.Fatal("server closed its output")
		}
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a message")
		return Message{}
	}
}

func TestInitializeRoundTrip(t *testing.T) {
	h := newHarness(t, &fakeHandler{})
	h.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"root":"/tmp/x"}}`)

	msg := h.next(t)
	if msg.ID == nil || *msg.ID != 1 {
		t.Fatalf("wrong id: %+v", msg)
	}
	var res InitializeResult
	if err := json.Unmarshal(msg.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Version != Version || res.Root != "/tmp/x" {
		t.Errorf("result = %+v", res)
	}
}

// Events must arrive as notifications BEFORE the reply, or the editor renders
// a finished run with no visible progress.
func TestRunStreamsEventsBeforeResult(t *testing.T) {
	h := newHarness(t, &fakeHandler{})
	h.send(t, `{"jsonrpc":"2.0","id":7,"method":"run","params":{"task":"hi"}}`)

	first := h.next(t)
	if first.Method != "froe/event" {
		t.Fatalf("expected an event first, got %+v", first)
	}
	second := h.next(t)
	if second.ID == nil || *second.ID != 7 {
		t.Fatalf("expected the run reply, got %+v", second)
	}
}

func TestUnknownMethodIsReported(t *testing.T) {
	h := newHarness(t, &fakeHandler{})
	h.send(t, `{"jsonrpc":"2.0","id":2,"method":"nope"}`)

	msg := h.next(t)
	if msg.Error == nil || msg.Error.Code != CodeMethodNotFound {
		t.Fatalf("expected method-not-found, got %+v", msg)
	}
}

func TestMalformedLineDoesNotKillTheServer(t *testing.T) {
	h := newHarness(t, &fakeHandler{})
	h.send(t, `{ this is not json`)

	if msg := h.next(t); msg.Error == nil || msg.Error.Code != CodeParse {
		t.Fatalf("expected a parse error, got %+v", msg)
	}
	// The connection must survive it.
	h.send(t, `{"jsonrpc":"2.0","id":3,"method":"initialize","params":{}}`)
	if msg := h.next(t); msg.ID == nil || *msg.ID != 3 {
		t.Fatalf("server did not recover: %+v", msg)
	}
}

// Approval is a server→client REQUEST: the run genuinely blocks on the answer.
func TestApproveBlocksUntilTheEditorReplies(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	var decision perms.Decision
	h := newHarness(t, &fakeHandler{})
	go func() {
		defer wg.Done()
		decision = h.srv.Ask(perms.Request{Tool: "edit_file", Summary: "edit x.go"})
	}()

	req := h.next(t)
	if req.Method != "froe/approve" || req.ID == nil {
		t.Fatalf("expected an approve request, got %+v", req)
	}
	var p ApproveParams
	json.Unmarshal(req.Params, &p)
	if p.Tool != "edit_file" {
		t.Errorf("params = %+v", p)
	}

	h.send(t, `{"jsonrpc":"2.0","id":`+itoa(*req.ID)+`,"result":{"decision":"allow"}}`)
	wg.Wait()

	if decision != perms.Allow {
		t.Errorf("decision = %v, want Allow", decision)
	}
}

// An editor that answers with nonsense must not be read as consent.
func TestApproveDefaultsToDeny(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	var decision perms.Decision

	h := newHarness(t, &fakeHandler{})
	go func() {
		defer wg.Done()
		decision = h.srv.Ask(perms.Request{Tool: "bash", Summary: "rm things"})
	}()

	req := h.next(t)
	h.send(t, `{"jsonrpc":"2.0","id":`+itoa(*req.ID)+`,"result":{"decision":"whatever"}}`)
	wg.Wait()

	if decision != perms.Deny {
		t.Errorf("an unrecognised decision must deny, got %v", decision)
	}
}

func TestCancelStopsARunningTask(t *testing.T) {
	started := make(chan struct{})
	h := newHarness(t, &fakeHandler{
		runFn: func(ctx context.Context, p RunParams, emit func(EventParams)) (*RunResult, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})

	h.send(t, `{"jsonrpc":"2.0","id":1,"method":"run","params":{"task":"long"}}`)
	<-started
	h.send(t, `{"jsonrpc":"2.0","id":2,"method":"cancel"}`)

	// The cancel ack and the cancelled run reply may arrive in either order.
	var sawCancelled bool
	for i := 0; i < 2; i++ {
		m := h.next(t)
		if m.Error != nil && m.Error.Code == CodeCancelled {
			sawCancelled = true
		}
	}
	if !sawCancelled {
		t.Error("the run was not reported as cancelled")
	}
}

// Selections can be large; the scanner's default 64KB limit would truncate them.
func TestLargeMessageIsAccepted(t *testing.T) {
	var gotTask string
	h := newHarness(t, &fakeHandler{
		runFn: func(ctx context.Context, p RunParams, emit func(EventParams)) (*RunResult, error) {
			gotTask = p.Selection
			return &RunResult{Answer: "ok"}, nil
		},
	})

	big := strings.Repeat("x", 200_000)
	payload, _ := json.Marshal(Message{
		JSONRPC: "2.0", ID: ptrInt64(1), Method: "run",
		Params: mustJSON(RunParams{Task: "t", Selection: big}),
	})
	h.send(t, string(payload))
	h.next(t)

	if len(gotTask) != len(big) {
		t.Errorf("selection truncated: got %d bytes, want %d", len(gotTask), len(big))
	}
}

func itoa(i int64) string            { b, _ := json.Marshal(i); return string(b) }
func ptrInt64(i int64) *int64        { return &i }
func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
