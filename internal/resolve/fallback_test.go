package resolve

import (
	"testing"

	"github.com/dcoldeira/froe/internal/registry"
)

// The catalogue of 2026-10-01: a local quick and careful model, and hosted
// models that fill the same preference lists further down.
var (
	fbRuntimes = map[string]registry.Runtime{
		"lmstudio": {Name: "lmstudio"},
		"ollama":   {Name: "ollama"},
		"mistral":  {Name: "mistral", APIKeyEnv: "MISTRAL_API_KEY"},
	}
	fbModels = []registry.Model{
		{ID: "ministral-3-8b-lmstudio", Runtime: "lmstudio", Roles: []string{"default", "main", "quick"}, SizeGB: 6},
		{ID: "bonsai-27b-lmstudio", Runtime: "lmstudio", Roles: []string{"main", "heavy", "careful"}, SizeGB: 4.7},
		{ID: "ministral-3:8b", Runtime: "ollama", Roles: []string{"main"}, SizeGB: 6},
		{ID: "codestral-latest", Runtime: "mistral", Roles: []string{"main"}, SizeGB: 0},
		{ID: "mistral-large-latest", Runtime: "mistral", Roles: []string{"heavy"}, SizeGB: 0},
	}
)

func fbHosted(rt string) bool { return fbRuntimes[rt].Hosted() }

func pickWith(up ...string) func(string) bool {
	set := map[string]bool{}
	for _, u := range up {
		set[u] = true
	}
	return func(rt string) bool { return set[rt] }
}

func TestFallbackFromLocalToHostedIsCaught(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class string
		up    []string
		want  string // the local model reported as left behind, "" for none
	}{
		// The 2026-10-01 failure, both classes: LM Studio asleep, Mistral up.
		{"careful, lmstudio down", RouteCareful, []string{"mistral"}, "bonsai-27b-lmstudio"},
		{"quick, lmstudio down", RouteQuick, []string{"mistral"}, "ministral-3-8b-lmstudio"},
		// Everything local down and no hosted key: nothing at all is picked.
		{"nothing up", RouteCareful, nil, "bonsai-27b-lmstudio"},
		// LM Studio up: the intended model is picked, nothing to catch.
		{"lmstudio up", RouteCareful, []string{"lmstudio", "mistral"}, ""},
		// Falls back to another LOCAL model: the work stays on this machine.
		{"quick, ollama takes over", RouteQuick, []string{"ollama", "mistral"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pref := RoutePreference[tc.class]
			got, ok := ChooseByRole(fbModels, pref, pickWith(tc.up...), fbHosted)
			want, leaves := leavesMachine(fbModels, pref, fbRuntimes, got, ok)
			if leaves != (tc.want != "") || (leaves && want.ID != tc.want) {
				t.Fatalf("picked %q (ok=%v); leavesMachine = %q, %v; want %q",
					got.ID, ok, want.ID, leaves, tc.want)
			}
		})
	}
}

// A preference that names a hosted model first is the user's own choice.
func TestHostedFirstPreferenceIsNotCaught(t *testing.T) {
	pref := []string{"heavy"}
	models := []registry.Model{fbModels[4]}
	got, ok := ChooseByRole(models, pref, pickWith("mistral"), fbHosted)
	if _, leaves := leavesMachine(models, pref, fbRuntimes, got, ok); leaves {
		t.Fatal("a hosted-first preference was treated as a fallback")
	}
}
