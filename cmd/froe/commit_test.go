package main

import (
	"strings"
	"testing"
)

// Small models wrap a commit message in whatever they like. The cleaner keeps
// the message and nothing else.
func TestCleanCommitMessage(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "physics: add CHSH noise model", "physics: add CHSH noise model"},
		{"preamble", "Commit message:\ncausal: expose witness robustness", "causal: expose witness robustness"},
		{"fenced", "```\nlang: type-check Switch(d)\n\nAdds T-Switch.\n```", "lang: type-check Switch(d)\n\nAdds T-Switch."},
		{"fenced with language", "```text\ndocs: update the book\n```", "docs: update the book"},
		{"quoted whole", `"tests: pin the switch witness"`, "tests: pin the switch witness"},
		{"backticked whole", "`fix: round witness value`", "fix: round witness value"},
		{"blank runs and trailing space", "feat: GHZ states  \n\n\n\nAdds Mermin.\t", "feat: GHZ states\n\nAdds Mermin."},
	} {
		if got := cleanCommitMessage(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A subject that merely contains quotes is not "wrapped" in them.
func TestCleanCommitMessageKeepsInnerQuotes(t *testing.T) {
	for _, s := range []string{
		`docs: rename "QPL" to "QRL"`,
		`"QRL" replaces the old name`,
	} {
		if got := cleanCommitMessage(s); got != s {
			t.Errorf("got %q, want %q unchanged", got, s)
		}
	}
}

// A long line ending in a colon is content, not a preamble.
func TestCleanCommitMessageKeepsALongFirstLine(t *testing.T) {
	s := "physics: the loophole-free Bell test now reports the detection threshold:\neta_crit = 0.8284"
	if got := cleanCommitMessage(s); got != s {
		t.Errorf("got %q", got)
	}
}

func TestPushArgsSetsUpstreamOnlyWhenMissing(t *testing.T) {
	if got := strings.Join(pushArgs("main", true), " "); got != "push origin main" {
		t.Errorf("with upstream: %s", got)
	}
	if got := strings.Join(pushArgs("paper-read-through", false), " "); got != "push -u origin paper-read-through" {
		t.Errorf("without upstream: %s", got)
	}
}

// A diff too big for the window is cut at a line boundary, and the prompt
// says so, so the model describes the change from the file list.
func TestBuildCommitPromptTruncatesAtALine(t *testing.T) {
	stat := " src/qrl/causal/witness.py | 400 ++++\n"
	diff := strings.Repeat("+    return trace(OMEGA @ W)\n", 400)
	p := buildCommitPrompt(stat, diff, 500)
	if !strings.Contains(p, "src/qrl/causal/witness.py") {
		t.Fatal("file list missing")
	}
	if !strings.Contains(p, "[diff truncated") {
		t.Fatal("truncation not announced")
	}
	body := p[strings.Index(p, "Staged diff:\n")+len("Staged diff:\n") : strings.Index(p, "\n\n[diff truncated")]
	if !strings.HasSuffix(body, "W)") || strings.Count(body, "\n")+1 != strings.Count(body, "return") {
		t.Fatalf("diff cut mid-line: ...%q", body[len(body)-40:])
	}
	if small := buildCommitPrompt(stat, "+x\n", 500); strings.Contains(small, "truncated") {
		t.Error("a small diff was marked truncated")
	}
}

func TestIndent(t *testing.T) {
	if got := indent("subject\n\nbody"); got != "  subject\n  \n  body" {
		t.Errorf("indent = %q", got)
	}
}
