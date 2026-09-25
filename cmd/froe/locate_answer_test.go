package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/tools"
)

// switchReport is a QRL results table where one column, "Causal Order", is
// spelled three ways across coupled sites - the shape `locate` exists for.
const switchReport = `"""Quantum switch results table."""

HEADERS = ("Process", "Witness Value", "Causal Order", "P_win")
WIDTHS = (24, 18, 14, 10)
LABELS = ("Process", "Witness\nValue", "Causal\nOrder", "P_win")


def causal_order(process):
    return "indefinite" if process.is_switch else "definite"


def row(process):
    return (process.name, process.witness, causal_order(process), process.p_win)


def summary():
    return "Causal Ordering is discussed in section 3."
`

func reportTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	p := filepath.Join(root, "src/qrl/reporting/switch_report.py")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(switchReport), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// ── parsing the answer ──────────────────────────────────────────────────────

func TestParseSitesReadsTheWhereSection(t *testing.T) {
	answer := `WHERE
- src/qrl/reporting/switch_report.py:3 - the header
- ` + "`./src/qrl/reporting/switch_report.py:13`" + ` - the row value

WHAT IT IS
Step 2:3 of the plan mentions notes.py:99, which is outside WHERE.

WATCH OUT
The widths are positional.`
	got := parseSites(answer)
	if len(got) != 2 || got[0] != (site{"src/qrl/reporting/switch_report.py", 3}) ||
		got[1] != (site{"src/qrl/reporting/switch_report.py", 13}) {
		t.Fatalf("sites = %+v", got)
	}
}

// A model that ignores the format still usually cites a real path.
func TestParseSitesFallsBackToTheWholeAnswer(t *testing.T) {
	got := parseSites("It lives in src/qrl/causal/witness.py:12, see step 2:3.")
	if len(got) != 1 || got[0].Path != "src/qrl/causal/witness.py" || got[0].Line != 12 {
		t.Fatalf("sites = %+v", got)
	}
}

func TestWhereSectionToleratesMarkdownHeadings(t *testing.T) {
	answer := "## **WHERE**\na.py:1\n### WATCH OUT\nb.py:2"
	if got := strings.TrimSpace(whereSection(answer)); got != "a.py:1" {
		t.Fatalf("where = %q", got)
	}
	if whereSection("no headings at all") != "" {
		t.Error("found a WHERE section that is not there")
	}
}

// ── checking citations against the tree ─────────────────────────────────────

func TestSplitByExistence(t *testing.T) {
	root := reportTree(t)
	sites := []site{
		{"src/qrl/reporting/switch_report.py", 3},
		{"src/qrl/witness/switch_report.py", 3}, // the path the issue got wrong
		{"src/qrl/reporting", 1},                // a directory is not a citation
		{"../outside.py", 1},
	}
	real, missing := splitByExistence(root, sites)
	if len(real) != 1 || real[0].Path != "src/qrl/reporting/switch_report.py" {
		t.Errorf("real = %+v", real)
	}
	if len(missing) != 3 {
		t.Errorf("missing = %+v", missing)
	}
}

// A wrong directory with a right file name is resolved to candidates, and the
// candidate is searched only when it is the only one.
func TestResolveMissingOffersTheSameNamedFile(t *testing.T) {
	root := reportTree(t)
	searches := []tools.Search{{Pattern: "Causal Order", Literal: true, IgnoreCase: true}}
	rs := resolveMissing(context.Background(), root,
		[]site{{"src/qrl/witness/switch_report.py", 3}, {"src/qrl/nowhere.py", 1}}, searches)
	if len(rs) != 2 {
		t.Fatalf("resolutions = %+v", rs)
	}
	if len(rs[0].Candidates) != 1 || rs[0].Candidates[0] != "src/qrl/reporting/switch_report.py" || len(rs[0].Evidence) == 0 {
		t.Errorf("same-named file not offered with evidence: %+v", rs[0])
	}
	if len(rs[1].Candidates) != 0 {
		t.Errorf("invented a candidate: %+v", rs[1])
	}
	out := renderResolutions(rs)
	if !strings.Contains(out, "NOT IN THIS TREE") || !strings.Contains(out, "did NOT verify") ||
		!strings.Contains(out, "unfounded, not as a typo") {
		t.Errorf("rendering:\n%s", out)
	}
}

// ── the completeness sweep ──────────────────────────────────────────────────

// The answer cites the header; the sweep replays the search in that file and
// reports the other spellings - including one two lines below a cited line,
// which a line tolerance would have hidden. "Causal Ordering" is a longer word
// and is not a site.
func TestSweepFindsTheSitesTheAnswerStoppedShortOf(t *testing.T) {
	root := reportTree(t)
	searches := []tools.Search{{Pattern: "Causal Order", Literal: true, IgnoreCase: true}}
	cited := []site{{"src/qrl/reporting/switch_report.py", 3}}

	got := sweepSites(context.Background(), root, cited, searches)
	lines := map[int]bool{}
	for _, m := range got {
		lines[m.Line] = true
	}
	if lines[3] {
		t.Error("re-reported the cited line")
	}
	if !lines[5] {
		t.Errorf("missed the escaped-newline label two lines below the citation: %+v", got)
	}
	if !lines[8] || !lines[13] {
		t.Errorf("missed the causal_order sites: %+v", got)
	}
	if lines[17] {
		t.Error("reported the 'Causal Ordering' lookalike")
	}
	if out := renderMissed(got); !strings.Contains(out, "ALSO MATCHING") {
		t.Errorf("rendering:\n%s", out)
	}
}

func TestSearchFromGrepCall(t *testing.T) {
	if s, ok := searchFromGrepCall("grep", `{"pattern":"Causal Order"}`, "a.py:3: x"); !ok || s.Pattern != "Causal Order" || !s.IgnoreCase {
		t.Errorf("replayable grep not read: %+v %v", s, ok)
	}
	if s, _ := searchFromGrepCall("grep", `{"pattern":"P_win (x)"}`, "(matched as literal text)\na.py:1: y"); !s.Literal {
		t.Error("a literal fallback must be replayed literally")
	}
	for _, tc := range [][3]string{
		{"grep", `{"pattern":"x"}`, `(no matches for "x")`},
		{"glob", `{"pattern":"x"}`, "a.py"},
		{"grep", `not json`, "a.py:1: x"},
		{"grep", `{"pattern":"  "}`, "a.py:1: x"},
	} {
		if _, ok := searchFromGrepCall(tc[0], tc[1], tc[2]); ok {
			t.Errorf("replayed %v", tc)
		}
	}
}

func TestIssueSearchesUseQuotedPhrasesNotPaths(t *testing.T) {
	issue := "The \"Causal Order\" column in `src/qrl/witness/report.py` is redundant; " +
		"remove it from 'switch_report.py' and the 'Witness Summary' table. \"Robustness\" stays."
	var got []string
	for _, s := range issueSearches(issue) {
		got = append(got, s.Pattern)
	}
	if strings.Join(got, "|") != "Causal Order|Witness Summary" {
		t.Fatalf("searches = %v", got)
	}
}

// ── what the run actually read ──────────────────────────────────────────────

func TestReadRangeFromCall(t *testing.T) {
	path, r, ok := readRangeFromCall("read_file", `{"path":"a.py","offset":10,"limit":5}`, "")
	if !ok || path != "a.py" || r != (openRange{10, 14}) {
		t.Errorf("range = %s %+v %v", path, r, ok)
	}
	// Truncation bounds what was returned, whatever was asked for.
	_, r, _ = readRangeFromCall("read_file", `{"path":"a.py"}`, "...\n(truncated at line 120 - call read_file again with offset=120)")
	if r != (openRange{1, 120}) {
		t.Errorf("truncated range = %+v", r)
	}
	if _, _, ok := readRangeFromCall("grep", `{"path":"a.py"}`, ""); ok {
		t.Error("grep read as a read_file")
	}
}

// Only citations the run never opened get their surroundings shown, and a
// cited line is never echoed back.
func TestSurroundingsShowOnlyUnreadCitations(t *testing.T) {
	root := reportTree(t)
	f := "src/qrl/reporting/switch_report.py"
	sites := []site{{f, 3}, {f, 13}}
	opened := map[string][]openRange{f: {{10, 14}}}

	got := surroundings(root, sites, opened)
	for _, m := range got {
		if m.Line == 3 || m.Line == 13 {
			t.Errorf("echoed a cited line: %+v", m)
		}
		if m.Line > 6 {
			t.Errorf("showed context around a citation the run had read: %+v", m)
		}
	}
	if len(got) == 0 || got[0].Line != 1 {
		t.Fatalf("surroundings = %+v", got)
	}
	if !strings.Contains(renderSurroundings(got), "AROUND THOSE LINES") {
		t.Error("rendering lost its heading")
	}
}

// ── streaming ───────────────────────────────────────────────────────────────

func TestFenceStripperDropsFencesAndLeadingBlanks(t *testing.T) {
	var b strings.Builder
	f := newFenceStripper(&b)
	for _, chunk := range []string{"\n\n```mark", "down\nWHERE\n- a.py:3\n``", "`\nWATCH OUT"} {
		f.Write([]byte(chunk))
	}
	f.Close()
	if got := b.String(); got != "WHERE\n- a.py:3\nWATCH OUT" {
		t.Fatalf("stripped = %q", got)
	}
}
