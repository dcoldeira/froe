package resolve

import (
	"regexp"
	"strings"
)

// Route classes. Each names a registry role, so which model serves a class is
// data (D3): tag a model "quick" or "careful" in models.toml to change it.
const (
	RouteQuick   = "quick"
	RouteCareful = "careful"
)

// RoutePreference is the role order tried for each class. Both fall back to
// the measured default, so a catalogue with no route roles behaves exactly as
// it did before routing existed.
var RoutePreference = map[string][]string{
	RouteQuick:   {"quick", "default", "main", "fast"},
	RouteCareful: {"careful", "heavy", "default", "main"},
}

// Route decides which class of model a task needs, and says why.
//
// The rule is deliberately plain - word patterns, no model call - because a
// classifier that needs a model would first have to load one, and on an 8 GB
// card only one model fits. The why is shown to the user, so a wrong route is
// visible and easy to override by pinning a model.
//
// Careful wins any tie. Measured 2026-09-29 on one read-only prompt (count the
// .py files under src/qrl/lang, name the type-check function, name the file
// with the CLI entry point): ministral-3-8b-lmstudio answered in 4.7s with a
// wrong count and names it never checked; bonsai-27b-lmstudio answered in
// 38.6s, read the files, and got all three right. A plain lookup is therefore
// careful work, not quick work: the fast model guesses instead of looking.
func Route(task string, hasSelection bool) (class, why string) {
	t := strings.ToLower(task)
	for _, r := range carefulRules {
		if r.re.MatchString(t) {
			return RouteCareful, r.why
		}
	}
	if hasSelection {
		return RouteQuick, "the highlighted text is already in the prompt"
	}
	for _, r := range quickRules {
		if r.re.MatchString(t) {
			return RouteQuick, r.why
		}
	}
	return RouteQuick, "no sign it needs checking"
}

type routeRule struct {
	re  *regexp.Regexp
	why string
}

// Whole words plus common endings: "counts" and "fixing" match, "prefix" and
// "address" do not (an open-ended \w* made "address" an edit). Verbs ending
// in e are listed without it ("writ", "chang") so "writing" matches too.
func words(ws ...string) *regexp.Regexp {
	return regexp.MustCompile(`\b(` + strings.Join(ws, "|") + `)(e|s|es|d|ed|ing|ion|ions)?\b`)
}

var carefulRules = []routeRule{
	{words("fix", "add", "implement", "refactor", "renam", "chang", "updat",
		"remov", "delet", "writ", "creat", "edit", "replac", "mov", "migrat"),
		"it asks for a change to files"},
	{words("count", "how many", "list all", "list every", "every", "each"),
		"it asks for a count or a complete list"},
	{words("verify", "check", "confirm", "prove", "make sure", "test"),
		"it asks for something to be checked"},
	{words("where", "which file", "which function", "find", "locat", "name the"),
		"it asks where something is in the code"},
	// Two or more numbered items: "(1) ... (2)" or "1. ... 2.".
	{regexp.MustCompile(`(\(\s*2\s*\)|(^|\s)2[.)]\s)`),
		"it has several numbered parts"},
}

var quickRules = []routeRule{
	{words("explain", "what does", "what is", "summary", "summarise", "summarize", "describe", "why does"),
		"it asks for an explanation"},
}
