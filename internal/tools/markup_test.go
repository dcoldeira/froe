package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shape Bonsai produced on eval task 07: a correct replacement followed by
// the tail of its own tool call. The edit must be refused and the file untouched.
func TestEditRefusesLeakedToolCallMarkup(t *testing.T) {
	dir := t.TempDir()
	src := `HEADERS = ("Process", "Causal\nOrder", "P_win")` + "\n"
	path := filepath.Join(dir, "t.py")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{
		"path":       "t.py",
		"old_string": `HEADERS = ("Process", "Causal\nOrder", "P_win")`,
		"new_string": `HEADERS = ("Process", "P_win")` + "\n</parameter>\n</function>\n<tool_call>\n<function=bash>",
	})
	_, err := Edit{}.Run(context.Background(), args, Env{Root: dir})
	if err == nil {
		t.Fatal("edit carrying tool-call markup was accepted")
	}
	if !strings.Contains(err.Error(), `"</parameter>"`) || !strings.Contains(err.Error(), `P_win\")`) {
		t.Fatalf("error does not name the markup and where to stop: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != src {
		t.Fatalf("file changed despite refusal: %s", got)
	}
}

// A file that already holds a token - a chat-template parser, say - stays editable.
func TestEditAllowsMarkupTheFileAlreadyHolds(t *testing.T) {
	dir := t.TempDir()
	src := "END = \"</tool_call>\"\n"
	if err := os.WriteFile(filepath.Join(dir, "p.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{
		"path": "p.py", "old_string": `END = "</tool_call>"`, "new_string": `CLOSE = "</tool_call>"`,
	})
	if _, err := (Edit{}).Run(context.Background(), args, Env{Root: dir}); err != nil {
		t.Fatalf("edit of existing markup refused: %v", err)
	}
}

func TestWriteRefusesLeakedToolCallMarkup(t *testing.T) {
	dir := t.TempDir()
	args, _ := json.Marshal(map[string]string{
		"path": "new.py", "content": "x = 1\n</parameter></function>",
	})
	if _, err := (Write{}).Run(context.Background(), args, Env{Root: dir}); err == nil {
		t.Fatal("write carrying tool-call markup was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "new.py")); !os.IsNotExist(err) {
		t.Fatal("file created despite refusal")
	}
}

func TestWriteAcceptsPlainContent(t *testing.T) {
	dir := t.TempDir()
	args, _ := json.Marshal(map[string]string{"path": "ok.py", "content": "x = 1\n"})
	if _, err := (Write{}).Run(context.Background(), args, Env{Root: dir}); err != nil {
		t.Fatalf("plain write refused: %v", err)
	}
}
