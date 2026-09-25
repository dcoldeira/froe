package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ── symbols ─────────────────────────────────────────────────────────────────

func TestExtractSymbolsPython(t *testing.T) {
	src := `"""Process matrices."""
# class Commented: not a symbol
class ProcessMatrix:
    def is_valid(self):
        pass

def causal_nonseparability_witness(d=2):
    pass

async def fetch_backend():
    pass
`
	got := ExtractSymbols("qrl/causal/process.py", src)
	want := []Symbol{
		{"class", "ProcessMatrix", 3},
		{"method", "is_valid", 4},
		{"func", "causal_nonseparability_witness", 7},
		{"func", "fetch_backend", 10},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("symbol %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestExtractSymbolsGo(t *testing.T) {
	src := "package qrl\n\n// func Commented() {}\ntype Witness struct{}\n\nfunc (w Witness) Value() float64 { return 0 }\n\nconst Dim = 2\nvar Omega = 0\n"
	var names []string
	for _, s := range ExtractSymbols("witness.go", src) {
		names = append(names, s.Kind+" "+s.Name)
	}
	if got := strings.Join(names, ", "); got != "type Witness, func Value, const Dim, var Omega" {
		t.Fatalf("symbols = %s", got)
	}
}

func TestExtractSymbolsIgnoresUnsupportedFiles(t *testing.T) {
	if s := ExtractSymbols("papers/quantum-causal.tex", `\section{Soundness}`); s != nil {
		t.Fatalf("symbols from .tex: %+v", s)
	}
	if Supported("notes.tex") || !Supported("switch.py") {
		t.Error("Supported disagrees with the language table")
	}
}

func TestExtractSymbolsCapsPerFile(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxSymbolsPerFile+10; i++ {
		b.WriteString("def stage_" + strings.Repeat("x", i+1) + "():\n    pass\n")
	}
	if n := len(ExtractSymbols("sampling_pipeline.py", b.String())); n != maxSymbolsPerFile {
		t.Fatalf("got %d symbols, want the cap %d", n, maxSymbolsPerFile)
	}
}

// ── budget ──────────────────────────────────────────────────────────────────

func TestBudgetSplitsAnEightKWindow(t *testing.T) {
	b := DefaultBudget(8192)
	if b.Reserve != 4096 {
		t.Errorf("reserve = %d, want half the window", b.Reserve)
	}
	// free = 8192 - 4096 - 700 = 3396; each part gets half of it.
	if b.InstructionAllowance() != 1698 || b.MapAllowance() != 1698 {
		t.Errorf("allowances = %d/%d, want 1698/1698", b.InstructionAllowance(), b.MapAllowance())
	}
}

func TestBudgetCapsLargeWindowsAndDropsTinyMaps(t *testing.T) {
	big := DefaultBudget(262144)
	if big.Reserve != 16384 || big.InstructionAllowance() != 4000 || big.MapAllowance() != 6000 {
		t.Errorf("big window: reserve %d, instructions %d, map %d",
			big.Reserve, big.InstructionAllowance(), big.MapAllowance())
	}
	if DefaultBudget(1024).MapAllowance() != 0 {
		t.Error("a map under a few hundred tokens should be skipped entirely")
	}
	if DefaultBudget(0).Total != 8192 {
		t.Error("unknown window should default to 8192")
	}
}

func TestEstimateTokens(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Error("empty string costs tokens")
	}
	if got := EstimateTokens("abcdef\n"); got != 3 { // 7/3 + 1 newline
		t.Errorf("EstimateTokens = %d, want 3", got)
	}
}

// ── instructions ────────────────────────────────────────────────────────────

// Nearer files come last so they override; FROE.md wins over CLAUDE.md in one
// directory.
func TestLoadInstructionsOrdersRootToLeaf(t *testing.T) {
	root := t.TempDir()
	write(t, root, "CLAUDE.md", "Focus: finish the paper.")
	write(t, root, "src/qrl/FROE.md", "Use ProcessMatrix, not raw arrays.")
	write(t, root, "src/qrl/CLAUDE.md", "ignored: FROE.md takes precedence")

	in, err := LoadInstructions(root, filepath.Join(root, "src/qrl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(in.Sources, ","); got != "CLAUDE.md,src/qrl/FROE.md" {
		t.Fatalf("sources = %s", got)
	}
	if strings.Index(in.Text, "finish the paper") > strings.Index(in.Text, "ProcessMatrix") {
		t.Error("root guidance must come before the nearer file")
	}
	if strings.Contains(in.Text, "ignored") {
		t.Error("CLAUDE.md loaded alongside FROE.md in the same directory")
	}
}

// Truncation is announced, never silent.
func TestInstructionsFitSaysWhenItCuts(t *testing.T) {
	in := &Instructions{Text: strings.Repeat("Always run the Araujo witness tests.\n", 50)}
	text, dropped := in.Fit(40)
	if !dropped || !strings.Contains(text, "truncated to fit") {
		t.Fatalf("cut without saying so: dropped=%v", dropped)
	}
	if text, dropped := in.Fit(100000); dropped || text != in.Text {
		t.Error("instructions that fit were changed")
	}
	if _, dropped := in.Fit(0); !dropped {
		t.Error("a zero allowance drops non-empty instructions and must say so")
	}
}

func TestFindRootStopsAtAMarker(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pyproject.toml", "[project]\nname = \"qrl\"\n")
	write(t, root, "src/qrl/causal/witness.py", "")
	got := FindRoot(filepath.Join(root, "src/qrl/causal"))
	want, _ := filepath.EvalSymlinks(root)
	if g, _ := filepath.EvalSymlinks(got); g != want {
		t.Fatalf("FindRoot = %s, want %s", got, root)
	}
}

// ── map, ranking ────────────────────────────────────────────────────────────

func qrlProject(t *testing.T) string {
	root := t.TempDir()
	write(t, root, "src/qrl/causal/witness.py", "def witness_value(W):\n    pass\n")
	write(t, root, "src/qrl/causal/switch.py", "from .witness import witness_value\n\ndef quantum_switch():\n    return witness_value(None)\n")
	write(t, root, "src/qrl/physics/bell.py", "def chsh(p):\n    pass\n")
	write(t, root, "node_modules/junk/index.py", "def junk():\n    pass\n")
	write(t, root, "__pycache__/witness.cpython.py", "def cached():\n    pass\n")
	return root
}

func TestBuildSkipsVendoredAndCacheDirs(t *testing.T) {
	m, err := Build(context.Background(), qrlProject(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range m.Files {
		if strings.Contains(f.Path, "node_modules") || strings.Contains(f.Path, "__pycache__") {
			t.Errorf("mapped %s", f.Path)
		}
	}
	if files, syms := m.Stats(); files != 3 || syms != 3 {
		t.Errorf("stats = %d files, %d symbols; want 3 and 3", files, syms)
	}
}

// A map that runs out of budget says how many files it left out.
func TestRenderReportsWhatDidNotFit(t *testing.T) {
	m, err := Build(context.Background(), qrlProject(t))
	if err != nil {
		t.Fatal(err)
	}
	full := m.Render(100000)
	if strings.Contains(full, "more files not listed") {
		t.Fatal("full budget still dropped files")
	}
	tight := m.Render(EstimateTokens("Project structure (symbols only - use read_file for detail):\n\n") + 12)
	if !strings.Contains(tight, "more files not listed") {
		t.Fatalf("tight budget dropped files silently:\n%s", tight)
	}
}

// A file defining the symbol a task names outranks one that only uses it.
func TestRankPutsTheDefiningFileFirst(t *testing.T) {
	m, err := Build(context.Background(), qrlProject(t))
	if err != nil {
		t.Fatal(err)
	}
	ranked := Rank(context.Background(), m, "Where is witness_value defined?")
	if ranked[0].Path != "src/qrl/causal/witness.py" {
		t.Fatalf("top = %s (%s)", ranked[0].Path, ranked[0].Why)
	}
	if !strings.Contains(ranked[0].Why, "defines witness_value") {
		t.Errorf("why = %q", ranked[0].Why)
	}
	if ranked[len(ranked)-1].Path != "src/qrl/physics/bell.py" {
		t.Errorf("unrelated file not last: %+v", ranked)
	}
}

func TestKeywordsDropStopWordsAndRepeats(t *testing.T) {
	got := strings.Join(Keywords("Fix the witness and the Witness robustness in the switch"), ",")
	if strings.Contains(got, "the") || strings.Count(got, "witness") != 1 {
		t.Fatalf("keywords = %s", got)
	}
	if !strings.Contains(got, "robustness") || !strings.Contains(got, "switch") {
		t.Fatalf("keywords lost content words: %s", got)
	}
}
