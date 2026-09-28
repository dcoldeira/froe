package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The 07-multi-site shape: one column spelled three ways, plus a lookalike.
const spellingFixture = `REGISTERED = ("Process", "Causal Order", "P_win")
HEADERS = ("Process", "Causal\nOrder", "P_win")
def row():
    return [causal_order(), p_win()]
LOOKALIKE = ("Causal Ordering",)
`

func spellingTree(t *testing.T) Env {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "switch_table.py"), []byte(spellingFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return Env{Root: dir}
}

func grepRun(t *testing.T, env Env, a map[string]any) (string, error) {
	t.Helper()
	b, _ := json.Marshal(a)
	return Grep{}.Run(context.Background(), b, env)
}

func TestSplitGlobPath(t *testing.T) {
	for _, c := range []struct{ path, glob, wantPath, wantGlob string }{
		{"src", "", "src", ""},
		{"**/switch_table.py", "", "", "switch_table.py"},
		{"pkg/*.py", "", "pkg", "*.py"},
		{"pkg/**/x.py", "", "pkg", "x.py"},
		{"**/x.py", "*.go", "", "*.go"}, // an explicit glob wins
	} {
		p, g := splitGlobPath(c.path, c.glob)
		if p != c.wantPath || g != c.wantGlob {
			t.Errorf("splitGlobPath(%q, %q) = %q, %q; want %q, %q", c.path, c.glob, p, g, c.wantPath, c.wantGlob)
		}
	}
}

// A glob in path used to be an rg IO error.
func TestGrepAcceptsAGlobInPath(t *testing.T) {
	out, err := grepRun(t, spellingTree(t), map[string]any{"pattern": "P_win", "path": "**/switch_table.py"})
	if err != nil {
		t.Fatalf("glob in path failed: %v", err)
	}
	if !strings.Contains(out, "switch_table.py:1:") {
		t.Errorf("no match:\n%s", out)
	}
}

// A phrase search also shows the other spellings, and not the lookalike.
func TestGrepShowsOtherSpellingsOfAPhrase(t *testing.T) {
	out, err := grepRun(t, spellingTree(t), map[string]any{"pattern": "Causal Order"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"switch_table.py:1:", `Causal\nOrder`, "causal_order()"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	// The exact search itself matches "Causal Ordering" - that is ripgrep's
	// answer to what was asked. The added spellings must not.
	if _, extra, _ := strings.Cut(out, "another way"); strings.Contains(extra, "Ordering") {
		t.Errorf("lookalike listed as another spelling:\n%s", out)
	}
	if strings.Count(out, "switch_table.py:1:") != 1 {
		t.Errorf("exact match listed twice:\n%s", out)
	}
}

// A deliberate regex is the model's own; nothing is added to it.
func TestGrepLeavesARegexAlone(t *testing.T) {
	out, err := grepRun(t, spellingTree(t), map[string]any{"pattern": "Causal (Order)"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "another way") {
		t.Errorf("regex search got other spellings:\n%s", out)
	}
}

// No exact match but other spellings exist: a find, not a dead end, so the
// fruitless-search detector must not count it.
func TestGrepOtherSpellingsAreNotANoMatch(t *testing.T) {
	out, err := grepRun(t, spellingTree(t), map[string]any{"pattern": "causal-order"})
	if err != nil {
		t.Fatal(err)
	}
	if IsNoMatch(out) {
		t.Errorf("counted as no match:\n%s", out)
	}
	if !strings.Contains(out, "causal_order()") {
		t.Errorf("other spelling missing:\n%s", out)
	}
}

// An old_string with an invented line names that line.
func TestEditNamesTheFirstWrongLine(t *testing.T) {
	dir := t.TempDir()
	src := "REGISTERED = (\n    \"Process\", \"Causal Order\", \"P_win\",\n)\n"
	if err := os.WriteFile(filepath.Join(dir, "t.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{
		"path":       "t.py",
		"old_string": "    \"Process\", \"Causal Order\", \"P_win\",\n    \"P_loss\", \"Q_win\",\n)",
		"new_string": "",
	})
	_, err := Edit{}.Run(context.Background(), args, Env{Root: dir})
	if err == nil {
		t.Fatal("edit with an invented line should fail")
	}
	for _, want := range []string{"Line 2", `"P_loss", "Q_win",`, "not anywhere in the file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}
