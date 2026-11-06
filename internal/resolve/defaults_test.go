package resolve

import (
	"testing"

	"github.com/dcoldeira/froe/internal/registry"
)

// The shipped catalogue's default is Qwen3-Coder-30B on llama.cpp, for both
// routes (measured 2026-11-06: 22/30 in 11m31s, against 16/30 in 62m55s for
// the same GGUF on colibri).
func TestShippedDefaultIsLlamaCpp(t *testing.T) {
	cat, err := registry.Load("")
	if err != nil {
		t.Fatal(err)
	}
	all := func(string) bool { return true }
	none := func(string) bool { return false }
	m, ok := ChooseByRole(cat.Models, RolePreference, all, none)
	if !ok || m.ID != "qwen3-coder-30b-llamacpp" {
		t.Fatalf("default with every runtime up = %q, want qwen3-coder-30b-llamacpp", m.ID)
	}
	for _, route := range []string{RouteQuick, RouteCareful} {
		m, ok := ChooseByRole(cat.Models, RoutePreference[route], all, none)
		if !ok || m.ID != "qwen3-coder-30b-llamacpp" {
			t.Errorf("route %s picked %q on %s", route, m.ID, m.Runtime)
		}
	}
	// The default runtime can be started by froe when it is down.
	if rt := cat.Runtimes["llamacpp"]; len(rt.Start) == 0 || !rt.PrefixReuse || !rt.ServesOne {
		t.Errorf("llamacpp runtime: start %v, prefix_reuse %v, serves_one %v", rt.Start, rt.PrefixReuse, rt.ServesOne)
	}
}
