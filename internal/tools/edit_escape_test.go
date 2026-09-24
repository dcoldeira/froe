package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A model that sends "Causal\nOrder" through JSON sends a line break; the file
// holds a backslash and an n. The error must say exactly that.
func TestEditNamesAnEscapedNewlineMismatch(t *testing.T) {
	dir := t.TempDir()
	src := `HEADERS = ("Process", "Causal\nOrder", "P_win")` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "t.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{
		"path": "t.py", "old_string": "\"Causal\nOrder\", ", "new_string": "",
	})
	_, err := Edit{}.Run(context.Background(), args, Env{Root: dir})
	if err == nil {
		t.Fatal("edit with a real line break should not match an escaped one")
	}
	if !strings.Contains(err.Error(), "backslash followed by n") {
		t.Fatalf("error does not name the escaped newline: %v", err)
	}
}

// The same edit written with the escape preserved goes through.
func TestEditMatchesAnEscapedNewlineWrittenCorrectly(t *testing.T) {
	dir := t.TempDir()
	src := `HEADERS = ("Process", "Causal\nOrder", "P_win")` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "t.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{
		"path": "t.py", "old_string": `"Causal\nOrder", `, "new_string": "",
	})
	if _, err := (Edit{}).Run(context.Background(), args, Env{Root: dir}); err != nil {
		t.Fatalf("correctly escaped edit failed: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "t.py"))
	if strings.Contains(string(got), "Causal") {
		t.Fatalf("header not removed: %s", got)
	}
}
