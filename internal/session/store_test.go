package session

import (
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSessionRoundTrip(t *testing.T) {
	s := testStore(t)

	sess, err := s.NewSession("/proj", "fix the parser", "bonsai")
	if err != nil {
		t.Fatal(err)
	}

	msgs := []StoredMessage{
		{Role: "user", Content: "fix the parser"},
		{Role: "assistant", Content: "", ToolCalls: `[{"ID":"1","Name":"read_file","Arguments":"{}"}]`},
		{Role: "tool", Content: "file contents", ToolCallID: "1"},
		{Role: "assistant", Content: "done"},
	}
	if err := s.AppendMessages(sess.ID, msgs); err != nil {
		t.Fatal(err)
	}

	got, err := s.Messages(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(msgs) {
		t.Fatalf("got %d messages, want %d", len(got), len(msgs))
	}
	// Order must survive: a tool result before its call is meaningless.
	for i := range msgs {
		if got[i].Role != msgs[i].Role || got[i].Content != msgs[i].Content {
			t.Errorf("message %d = %+v, want %+v", i, got[i], msgs[i])
		}
	}
	if got[2].ToolCallID != "1" {
		t.Errorf("tool_call_id lost: %+v", got[2])
	}
}

// Appending twice must continue the sequence, not restart it.
func TestAppendContinuesSequence(t *testing.T) {
	s := testStore(t)
	sess, _ := s.NewSession("/proj", "t", "m")

	s.AppendMessages(sess.ID, []StoredMessage{{Role: "user", Content: "one"}})
	s.AppendMessages(sess.ID, []StoredMessage{{Role: "user", Content: "two"}})

	got, _ := s.Messages(sess.ID)
	if len(got) != 2 || got[0].Content != "one" || got[1].Content != "two" {
		t.Fatalf("ordering broken across appends: %+v", got)
	}
}

func TestLatestIsPerProject(t *testing.T) {
	s := testStore(t)
	a, _ := s.NewSession("/proj-a", "a", "m")
	b, _ := s.NewSession("/proj-b", "b", "m")
	// Touch A so it becomes the most recent overall.
	s.AppendMessages(a.ID, []StoredMessage{{Role: "user", Content: "x"}})

	got, err := s.Latest("/proj-b")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != b.ID {
		t.Errorf("Latest returned another project's session: %+v", got)
	}
}

func TestLatestOnEmptyProjectIsNotAnError(t *testing.T) {
	s := testStore(t)
	got, err := s.Latest("/nothing-here")
	if err != nil {
		t.Fatalf("expected nil session, got error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

// The agent writes memories itself, so re-learning the same thing each session
// would otherwise fill the prompt with duplicates.
func TestRememberDeduplicates(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 3; i++ {
		if err := s.Remember("/proj", "uses tabs", "agent"); err != nil {
			t.Fatal(err)
		}
	}
	mems, _ := s.Memories("/proj", 0)
	if len(mems) != 1 {
		t.Fatalf("got %d memories, want 1 after deduplication", len(mems))
	}
}

func TestMemoriesArePerProject(t *testing.T) {
	s := testStore(t)
	s.Remember("/a", "fact about a", "user")
	s.Remember("/b", "fact about b", "user")

	mems, _ := s.Memories("/a", 0)
	if len(mems) != 1 || mems[0].Text != "fact about a" {
		t.Errorf("project isolation broken: %+v", mems)
	}
}

func TestForget(t *testing.T) {
	s := testStore(t)
	s.Remember("/proj", "temporary", "user")
	mems, _ := s.Memories("/proj", 0)

	if err := s.Forget(mems[0].ID); err != nil {
		t.Fatal(err)
	}
	if after, _ := s.Memories("/proj", 0); len(after) != 0 {
		t.Errorf("memory survived Forget: %+v", after)
	}
	if err := s.Forget(99999); err == nil {
		t.Error("forgetting a missing id should report an error")
	}
}

func TestSetTitleOnlyFillsBlanks(t *testing.T) {
	s := testStore(t)
	sess, _ := s.NewSession("/proj", "", "m")

	s.SetTitle(sess.ID, "first thing said")
	got, _ := s.Get(sess.ID)
	if got.Title != "first thing said" {
		t.Errorf("title = %q", got.Title)
	}

	// A later turn must not rename the session.
	s.SetTitle(sess.ID, "second thing")
	got, _ = s.Get(sess.ID)
	if got.Title != "first thing said" {
		t.Errorf("title was overwritten: %q", got.Title)
	}
}

func TestSaveRunRecordsMetrics(t *testing.T) {
	s := testStore(t)
	sess, _ := s.NewSession("/proj", "t", "bonsai")
	if err := s.SaveRun(RunRecord{
		SessionID: sess.ID, Model: "bonsai", Turns: 3, ToolCalls: 2,
		PromptTokens: 100, CompletionTokens: 50,
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE session_id = ?`, sess.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("got %d run records, want 1", n)
	}
}
