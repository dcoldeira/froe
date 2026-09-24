package main

import (
	"strings"
	"testing"
)

// The recheck hands the model every unjudged line, with where it is, and asks
// for a judgement in the answer's own sections.
func TestRecheckPromptListsEveryLine(t *testing.T) {
	got := recheckPrompt([]missed{
		{Path: "src/qrl/reporting/witness_pdf_report.py", Line: 491, Text: `'Causal\nOrder', 'P_win',`},
		{Path: "src/qrl/reporting/witness_pdf_report.py", Line: 499, Text: `_s(data.get('causal_order', 'indefinite')),`},
	})
	for _, want := range []string{
		"src/qrl/reporting/witness_pdf_report.py:491",
		"src/qrl/reporting/witness_pdf_report.py:499",
		"WHERE", "WATCH OUT",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("recheck prompt is missing %q:\n%s", want, got)
		}
	}
}

// A line the answer names anywhere has been judged - a lookalike in WATCH OUT
// included - so the sweep must not list it again as if it had been missed.
func TestWithoutMentionedDropsJudgedLines(t *testing.T) {
	ms := []missed{
		{Path: "src/r.py", Line: 491},
		{Path: "src/r.py", Line: 499},
		{Path: "src/r.py", Line: 513},
	}
	answer := "WHERE\n  src/r.py:491  header\n\nWATCH OUT\n  src/r.py:513 is the DAG Details lookalike\n"
	got := withoutMentioned(ms, answer)
	if len(got) != 1 || got[0].Line != 499 {
		t.Fatalf("want only :499 left, got %+v", got)
	}
}
