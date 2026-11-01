package resolve

import (
	"testing"

	"github.com/dcoldeira/froe/internal/registry"
)

// The shipped catalogue's default is the colibri model, and no LM Studio
// model is ever picked by role: LM Studio is closed source (decided
// 2026-11-01). They stay usable by naming them.
func TestShippedDefaultIsColibriNeverLMStudio(t *testing.T) {
	cat, err := registry.Load("")
	if err != nil {
		t.Fatal(err)
	}
	all := func(string) bool { return true }
	none := func(string) bool { return false }
	m, ok := ChooseByRole(cat.Models, RolePreference, all, none)
	if !ok || m.ID != "qwen3-coder-30b-colibri" {
		t.Fatalf("default with every runtime up = %q, want qwen3-coder-30b-colibri", m.ID)
	}
	for _, route := range []string{RouteQuick, RouteCareful} {
		m, ok := ChooseByRole(cat.Models, RoutePreference[route], all, none)
		if !ok || m.Runtime == "lmstudio" {
			t.Errorf("route %s picked %q on %s", route, m.ID, m.Runtime)
		}
	}
	// With colibri down, the fallback is another local runtime, never LM Studio.
	noColibri := func(rt string) bool { return rt != "colibri" }
	if m, ok := ChooseByRole(cat.Models, RolePreference, noColibri, none); ok && m.Runtime == "lmstudio" {
		t.Errorf("fallback picked %q on LM Studio", m.ID)
	}
	for _, m := range cat.Models {
		if m.Runtime == "lmstudio" && len(m.Roles) > 0 {
			t.Errorf("%s has roles %v; LM Studio models get none", m.ID, m.Roles)
		}
	}
}
