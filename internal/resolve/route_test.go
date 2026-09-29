package resolve

import (
	"testing"

	"github.com/dcoldeira/froe/internal/registry"
)

func TestRoute(t *testing.T) {
	cases := []struct {
		task      string
		selection bool
		want      string
	}{
		// The measured prompt from 2026-09-29: the fast model got it wrong.
		{"Read-only task, do not edit any files. (1) Count the .py files under src/qrl/lang/. " +
			"(2) Name the function in src/qrl/lang that type-checks a program.", false, RouteCareful},
		{"fix the off-by-one in parser.go", false, RouteCareful},
		{"where is the CLI entry point defined?", false, RouteCareful},
		{"how many tests are skipped", false, RouteCareful},
		{"1. read the file 2. summarise it", false, RouteCareful},
		{"explain what this does", true, RouteQuick},
		{"explain the tool loop in agent.go", false, RouteQuick},
		{"hi", false, RouteQuick},
		{"writing a new test for lexer.go", false, RouteCareful},
		{"changing the default model", false, RouteCareful},
		// A selection does not make an edit quick.
		{"rename this variable", true, RouteCareful},
		// Word boundaries: "prefix" is not "fix", "address" is not "add".
		{"what is the prefix of the address", false, RouteQuick},
	}
	for _, c := range cases {
		got, why := Route(c.task, c.selection)
		if got != c.want {
			t.Errorf("Route(%q, %v) = %s (%s), want %s", c.task, c.selection, got, why, c.want)
		}
		if why == "" {
			t.Errorf("Route(%q) gave no reason", c.task)
		}
	}
}

func TestRoutePreferenceUsesRouteRoles(t *testing.T) {
	models := []registry.Model{
		{ID: "small", Runtime: "lms", SizeGB: 6, Roles: []string{"default", "main", "quick"}},
		{ID: "big", Runtime: "lms", SizeGB: 3.8, Roles: []string{"main", "heavy", "careful"}},
	}
	up := func(string) bool { return true }

	if m, _ := ChooseByRole(models, RoutePreference[RouteQuick], up); m.ID != "small" {
		t.Errorf("quick picked %s, want small", m.ID)
	}
	if m, _ := ChooseByRole(models, RoutePreference[RouteCareful], up); m.ID != "big" {
		t.Errorf("careful picked %s, want big", m.ID)
	}
}

func TestRoutePreferenceFallsBackToDefault(t *testing.T) {
	// No route roles anywhere: both classes must land on the default, so
	// routing changes nothing for a catalogue that has not opted in.
	models := []registry.Model{
		{ID: "only", Runtime: "lms", SizeGB: 6, Roles: []string{"default", "main"}},
	}
	up := func(string) bool { return true }
	for class, pref := range RoutePreference {
		if m, _ := ChooseByRole(models, pref, up); m.ID != "only" {
			t.Errorf("%s picked %s, want only", class, m.ID)
		}
	}
}
