package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// qrlTree writes a small QRL-shaped project and returns its root.
func qrlTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"src/qrl/causal/witness.py":      "def witness_value(W):\n\treturn trace(OMEGA @ W)\n",
		"src/qrl/causal/switch.py":       "from .witness import witness_value\n",
		"src/qrl/physics/bell.py":        "def chsh(p):\n    return 2 * 2 ** 0.5 * p\n",
		"tests/test_witness.py":          "from qrl.causal.witness import witness_value\n",
		"papers/quantum-causal.tex":      "\\section{Soundness}\n",
		"src/qrl/causal/__init__.py":     "",
		"src/qrl/domains/sensing/qfi.py": "def quantum_fisher_information(rho):\n    pass\n",
	}
	for p, body := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func run(t *testing.T, tool Tool, env Env, args map[string]any) (string, error) {
	t.Helper()
	b, _ := json.Marshal(args)
	return tool.Run(context.Background(), b, env)
}

// ── the project-root boundary ────────────────────────────────────────────────

func TestResolveKeepsPathsInsideTheRoot(t *testing.T) {
	dir := qrlTree(t)
	env := Env{Root: dir}
	for _, p := range []string{"src/qrl/causal/witness.py", "./src/../src/qrl", "new/dir/not-yet.py"} {
		if _, err := resolve(env, p); err != nil {
			t.Errorf("resolve(%q) = %v, want inside the root", p, err)
		}
	}
}

func TestResolveRefusesEscapes(t *testing.T) {
	dir := qrlTree(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link-out")); err != nil {
		t.Fatal(err)
	}
	env := Env{Root: dir}
	for _, p := range []string{"../escape.py", "/etc/passwd", "src/../../x", "link-out/secret.py", ""} {
		if _, err := resolve(env, p); err == nil {
			t.Errorf("resolve(%q) succeeded, want refusal", p)
		} else if p != "" && !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("resolve(%q) = %v, want ErrOutsideRoot", p, err)
		}
	}
}

// Every file tool goes through the same boundary.
func TestFileToolsRefuseToLeaveTheRoot(t *testing.T) {
	env := Env{Root: qrlTree(t)}
	for _, tc := range []struct {
		tool Tool
		args map[string]any
	}{
		{Read{}, map[string]any{"path": "../x"}},
		{Write{}, map[string]any{"path": "../x", "content": "y"}},
		{Edit{}, map[string]any{"path": "../x", "old_string": "a", "new_string": "b"}},
	} {
		if _, err := run(t, tc.tool, env, tc.args); !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("%s outside the root: %v, want ErrOutsideRoot", tc.tool.Name(), err)
		}
	}
}

// ── read_file ───────────────────────────────────────────────────────────────

// The gutter is a bar, never a tab, so a tab-indented line is unambiguous.
func TestReadUsesABarGutterSoTabsStayVisible(t *testing.T) {
	env := Env{Root: qrlTree(t)}
	out, err := run(t, Read{}, env, map[string]any{"path": "src/qrl/causal/witness.py"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "     2|\treturn trace(OMEGA @ W)\n") {
		t.Fatalf("gutter or tab lost:\n%s", out)
	}
}

func TestReadOffsetAndLimitSayWhereTheyStopped(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		b.WriteString("stage\n")
	}
	os.WriteFile(filepath.Join(dir, "pipeline.py"), []byte(b.String()), 0o644)
	out, err := run(t, Read{}, Env{Root: dir}, map[string]any{"path": "pipeline.py", "offset": 5, "limit": 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "     5|stage\n") || strings.Contains(out, "     8|") {
		t.Fatalf("wrong window:\n%s", out)
	}
	if !strings.Contains(out, "call read_file again with offset=8") {
		t.Fatalf("no continuation hint:\n%s", out)
	}
}

func TestReadExplainsDirectoriesAndEmptyWindows(t *testing.T) {
	env := Env{Root: qrlTree(t)}
	if _, err := run(t, Read{}, env, map[string]any{"path": "src/qrl"}); err == nil ||
		!strings.Contains(err.Error(), "use glob") {
		t.Errorf("directory read: %v", err)
	}
	out, err := run(t, Read{}, env, map[string]any{"path": "src/qrl/causal/witness.py", "offset": 99})
	if err != nil || !strings.Contains(out, "past the end") {
		t.Errorf("offset past end: %q, %v", out, err)
	}
}

// ── glob ────────────────────────────────────────────────────────────────────

func TestMatchGlob(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"**/*.py", "src/qrl/causal/witness.py", true},
		{"**/*.py", "top.py", true}, // ** matches zero directories
		{"src/**/witness.py", "src/qrl/causal/witness.py", true},
		{"src/**/sensing/**/*.py", "src/qrl/domains/sensing/qfi.py", true},
		{"witness.py", "src/qrl/causal/witness.py", true}, // bare name matches the base
		{"causal/*.py", "src/qrl/causal/witness.py", false},
		{"**/*.tex", "src/qrl/causal/witness.py", false},
	} {
		if got := matchGlob(tc.pattern, tc.name); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestGlobReportsNoMatchInTheSharedWording(t *testing.T) {
	env := Env{Root: qrlTree(t)}
	out, err := run(t, Glob{}, env, map[string]any{"pattern": "**/*.rs"})
	if err != nil {
		t.Fatal(err)
	}
	if !IsNoMatch(out) {
		t.Fatalf("no-match result %q is not recognised by IsNoMatch", out)
	}
	out, _ = run(t, Glob{}, env, map[string]any{"pattern": "**/*witness*.py"})
	if out != "(2 files)\nsrc/qrl/causal/witness.py\ntests/test_witness.py" {
		t.Fatalf("glob = %q", out)
	}
}

// The total is stated by the tool, first, so the model copies it instead of
// counting. 2026-10-01: mistral-large listed nine files and answered 8.
func TestGrepStatesItsCountsFirst(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
	env := Env{Root: qrlTree(t)}
	out, err := run(t, Grep{}, env, map[string]any{"pattern": "witness"})
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(out, "\n")
	if !strings.HasPrefix(first, "(") || !strings.HasSuffix(first, " files)") || !strings.Contains(first, "matching line") {
		t.Fatalf("first line %q does not state the counts", first)
	}

	// One file: rg prints "12:text" with no path, which must not count as files.
	out, err = run(t, Grep{}, env, map[string]any{"pattern": "def", "path": "src/qrl/causal/witness.py"})
	if err != nil {
		t.Fatal(err)
	}
	if first, _, _ := strings.Cut(out, "\n"); !strings.HasSuffix(first, " in 1 file)") {
		t.Fatalf("single-file search counted %q", first)
	}
}

func TestCountLine(t *testing.T) {
	if got := countLine(1, "file", "files"); got != "1 file" {
		t.Errorf("countLine(1) = %q", got)
	}
	if got := countLine(9, "file", "files"); got != "9 files" {
		t.Errorf("countLine(9) = %q", got)
	}
}

func TestIsNoMatch(t *testing.T) {
	if !IsNoMatch(`(no matches for "Causal Order")`) || !IsNoMatch(`(no files match "**/*.rs")`) {
		t.Error("no-match wordings not recognised")
	}
	if IsNoMatch("src/qrl/causal/witness.py:1: def witness_value(W):") {
		t.Error("a real match was read as no-match")
	}
}

// ── git_log ─────────────────────────────────────────────────────────────────

func TestGitLogShowsHistoryAndFiltersByMessage(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := qrlTree(t)
	gitDo := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=qrl", "GIT_AUTHOR_EMAIL=qrl@example.com",
			"GIT_COMMITTER_NAME=qrl", "GIT_COMMITTER_EMAIL=qrl@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitDo("init", "-q")
	gitDo("add", ".")
	gitDo("commit", "-q", "-m", "add causal witness")
	os.WriteFile(filepath.Join(dir, "src/qrl/physics/bell.py"), []byte("def chsh(p):\n    return 2.61\n"), 0o644)
	gitDo("commit", "-q", "-am", "fix witness value rounding")

	env := Env{Root: dir}
	out, err := run(t, GitLog{}, env, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "qrl: fix witness value rounding") {
		t.Fatalf("log = %q", out)
	}
	out, _ = run(t, GitLog{}, env, map[string]any{"grep": "causal"})
	if !strings.Contains(out, "add causal witness") || strings.Contains(out, "rounding") {
		t.Fatalf("grep filter = %q", out)
	}
	if _, err := run(t, GitLog{}, env, map[string]any{"path": "../elsewhere"}); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("path outside the root: %v", err)
	}
}

// ── registry ────────────────────────────────────────────────────────────────

// The read-only registry is what `froe locate` runs with: nothing that writes.
func TestReadOnlyRegistryHasNoMutatingTool(t *testing.T) {
	r := NewReadOnlyRegistry()
	for _, n := range r.Names() {
		if tool, _ := r.Get(n); tool.Mutating() {
			t.Errorf("read-only registry holds mutating tool %s", n)
		}
	}
	if _, ok := r.Get("read_file"); !ok {
		t.Error("read-only registry cannot read")
	}
}

// Defs come out in name order, so the prompt prefix is stable for caching.
func TestDefsAreInStableOrder(t *testing.T) {
	defs := NewRegistry().Defs()
	for i := 1; i < len(defs); i++ {
		if defs[i-1].Name > defs[i].Name {
			t.Fatalf("defs out of order: %s before %s", defs[i-1].Name, defs[i].Name)
		}
	}
}
