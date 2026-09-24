package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The 09-locate-wrong-path fixture, line for line: headers on 5-8, widths on 10.
const witnessReport = `"""Witness Summary table for the causal-structure report."""

# Headers and widths are positional: the Nth header is drawn at the Nth
# width, so removing a column means removing its width too.
WITNESS_SUMMARY_HEADERS = (
    "Process", "Dimension", "Witness Value",
    "Robustness", "Causal Order", "P_win",
)

WITNESS_SUMMARY_WIDTHS = (24, 12, 20, 18, 18, 12)

# Import-time guard: a header with no width is a broken table, so a partial
# edit fails the moment the module loads rather than when a PDF is drawn.
assert len(WITNESS_SUMMARY_HEADERS) == len(WITNESS_SUMMARY_WIDTHS)
`

func relinesRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "src", "qrl", "reporting")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "witness_report.py"), []byte(witnessReport), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// Bonsai's answers from 2026-09-24, verbatim WHERE sections.
func TestRelinesCorrectsAQuotedCitationOnTheWrongLine(t *testing.T) {
	root := relinesRoot(t)
	for name, tc := range map[string]struct {
		answer string
		said   int
	}{
		"run 3 quotes the line": {"WHERE\n" +
			"  src/qrl/reporting/witness_report.py:7:    \"Robustness\", \"Causal Order\", \"P_win\",\n" +
			"  src/qrl/reporting/witness_report.py:8:WITNESS_SUMMARY_WIDTHS = (24, 12, 20, 18, 18, 12)\n", 8},
		// The second rerun, same day, after the first fix: a name that is
		// also read by the assert below it, and a fragment of three characters.
		"rerun 3 names a variable also used elsewhere": {"WHERE\n" +
			"  src/qrl/reporting/witness_report.py:7: \"Causal Order\" in WITNESS_SUMMARY_HEADERS tuple\n" +
			"  src/qrl/reporting/witness_report.py:8: Index 4 in WITNESS_SUMMARY_WIDTHS (corresponds to Causal Order)\n", 8},
		"rerun 2 quotes a short fragment": {"WHERE\n" +
			"  src/qrl/reporting/witness_report.py:7: \"Causal Order\"\n" +
			"  src/qrl/reporting/witness_report.py:8: 18,\n", 8},
		"run 2 names the identifier": {"WHERE\n" +
			"  src/qrl/reporting/witness_report.py:6  WITNESS_SUMMARY_HEADERS tuple containing \"Process\", \"Dimension\"\n" +
			"  src/qrl/reporting/witness_report.py:7  WITNESS_SUMMARY_WIDTHS tuple with 6 values\n", 7},
	} {
		rs := relines(root, tc.answer)
		if len(rs) != 1 {
			t.Fatalf("%s: want exactly one correction, got %+v", name, rs)
		}
		if r := rs[0]; r.Said != tc.said || r.Actual != 10 {
			t.Errorf("%s: want :%d corrected to :10, got :%d -> :%d", name, tc.said, r.Said, r.Actual)
		}
	}
}

// A citation whose quote IS on the cited line is left alone - including the
// headers, which run 2 cited as :6 on the strength of "Process" being there.
func TestRelinesLeavesARightCitationAlone(t *testing.T) {
	root := relinesRoot(t)
	answer := "WHERE\n" +
		"  src/qrl/reporting/witness_report.py:7  \"Causal Order\" in WITNESS_SUMMARY_HEADERS\n" +
		"  src/qrl/reporting/witness_report.py:10  WITNESS_SUMMARY_WIDTHS\n"
	if rs := relines(root, answer); len(rs) != 0 {
		t.Fatalf("corrected citations that were right: %+v", rs)
	}
}

// One match or nothing: "Witness" fits several lines, so it proves nothing
// about which one was meant.
func TestRelinesDoesNotGuessBetweenSeveralLines(t *testing.T) {
	root := relinesRoot(t)
	answer := "WHERE\n  src/qrl/reporting/witness_report.py:3  `Witness`\n"
	if rs := relines(root, answer); len(rs) != 0 {
		t.Fatalf("guessed a line from an ambiguous quote: %+v", rs)
	}
}

// Prose with nothing code-like in it gives nothing to check against.
func TestRelinesIgnoresPlainDescriptions(t *testing.T) {
	root := relinesRoot(t)
	answer := "WHERE\n  src/qrl/reporting/witness_report.py:2  the table definition\n"
	if rs := relines(root, answer); len(rs) != 0 {
		t.Fatalf("corrected from a plain description: %+v", rs)
	}
}

// A name defined on one line and read on another means the definition.
func TestDefinesName(t *testing.T) {
	for line, want := range map[string]bool{
		"WITNESS_SUMMARY_WIDTHS = (24, 12)":                                  true,
		"def register_tables(pdf):":                                          true,
		"func Average(nums []float64) float64 {":                             true,
		"assert len(WITNESS_SUMMARY_HEADERS) == len(WITNESS_SUMMARY_WIDTHS)": false,
		"if WITNESS_SUMMARY_WIDTHS == other:":                                false,
	} {
		name := "WITNESS_SUMMARY_WIDTHS"
		switch {
		case strings.Contains(line, "register_tables"):
			name = "register_tables"
		case strings.Contains(line, "Average"):
			name = "Average"
		}
		if got := definesName(line, name); got != want {
			t.Errorf("definesName(%q, %q) = %v, want %v", line, name, got, want)
		}
	}
}

// Downstream checks must see the corrected line, not the one the answer gave.
func TestApplyRelinesMovesTheSite(t *testing.T) {
	sites := []site{{Path: "a.py", Line: 7}, {Path: "a.py", Line: 8}}
	got := applyRelines(sites, []relined{{Path: "a.py", Said: 8, Actual: 10}})
	if got[0].Line != 7 || got[1].Line != 10 {
		t.Fatalf("want lines 7 and 10, got %+v", got)
	}
}
