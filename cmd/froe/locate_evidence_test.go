package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/tools"
)

// Before turn 1, froe searches the project for the phrases the report quotes
// and hands the model the lines - here the column is found in the real file
// although the report names a path that does not exist.
func TestGatherEvidenceFindsTheQuotedPhrase(t *testing.T) {
	root := reportTree(t)
	issue := `The "Causal Order" column in src/qrl/witness/switch_report.py is redundant.`
	out, searches := gatherEvidence(context.Background(), root, issue, 2000)

	if !strings.HasPrefix(out, "EVIDENCE") {
		t.Fatalf("no evidence block:\n%s", out)
	}
	if !strings.Contains(out, "src/qrl/reporting/switch_report.py") {
		t.Errorf("real file not named:\n%s", out)
	}
	if !strings.Contains(out, "3: HEADERS") {
		t.Errorf("header line not shown:\n%s", out)
	}
	// The literal search and its words-only variant both ran, and are handed
	// on so the sweep can replay them.
	if len(searches) != 2 {
		t.Errorf("searches = %+v", searches)
	}
}

func TestGatherEvidenceIsEmptyWithoutQuotedPhrases(t *testing.T) {
	out, searches := gatherEvidence(context.Background(), reportTree(t), "remove the column please", 2000)
	if out != "" || searches != nil {
		t.Fatalf("evidence from an unquoted report: %q %+v", out, searches)
	}
}

// A phrase that matches nothing produces no block at all, rather than an
// empty heading the model would read as "nothing exists".
func TestGatherEvidenceOmitsTermsThatMatchNothing(t *testing.T) {
	out, _ := gatherEvidence(context.Background(), reportTree(t), `Remove "Bell Violation" from the table.`, 2000)
	if out != "" {
		t.Fatalf("block for a phrase that matched nothing:\n%s", out)
	}
}

// The block respects its budget: a term that does not fit is left out.
func TestGatherEvidenceStaysInsideItsBudget(t *testing.T) {
	out, _ := gatherEvidence(context.Background(), reportTree(t), `The "Causal Order" column.`, 10)
	if out != "" {
		t.Fatalf("exceeded a 10-token budget:\n%s", out)
	}
}

// The file that mentions a term most is listed first, and long lists are
// capped with a count of what was left out.
func TestSearchTermRanksFilesAndRenderCaps(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write("switch.py", strings.Repeat("quantum_switch()\n", 9))
	for _, f := range []string{"a.py", "b.py", "c.py", "d.py"} {
		write(f, "quantum switch\n")
	}
	term := tools.Search{Pattern: "quantum switch", Literal: true, IgnoreCase: true}
	hits, _ := searchTerm(context.Background(), root, term)
	if len(hits) != 5 || hits[0].Path != "switch.py" {
		t.Fatalf("hits = %+v", hits)
	}
	out := renderTerm(term.Pattern, hits)
	for _, want := range []string{"5 files", "(9 matches)", "... 3 more in this file", "... 1 more files"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendering lacks %q:\n%s", want, out)
		}
	}
}

func TestPlural(t *testing.T) {
	if plural(1, "file", "files") != "1 file" || plural(3, "file", "files") != "3 files" {
		t.Error("plural")
	}
}
