package resolve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/registry"
)

// liveRuntime returns a runtime pointing at a server that answers /models.
func liveRuntime(t *testing.T, name string) (registry.Runtime, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	return registry.Runtime{Name: name, Kind: "openai-compat", BaseURL: srv.URL}, srv.Close
}

func deadRuntime(name string) registry.Runtime {
	return registry.Runtime{Name: name, Kind: "openai-compat", BaseURL: "http://127.0.0.1:1"}
}

func TestPickPrefersMainRoleAndSmallestModel(t *testing.T) {
	rt, stop := liveRuntime(t, "live")
	defer stop()

	cat := &registry.Catalogue{
		Runtimes: map[string]registry.Runtime{"live": rt},
		Models: []registry.Model{
			{ID: "big", Runtime: "live", SizeGB: 19, Roles: []string{"main"}},
			{ID: "small", Runtime: "live", SizeGB: 3.5, Roles: []string{"main"}},
			{ID: "tiny", Runtime: "live", SizeGB: 1, Roles: []string{"fast"}},
		},
	}

	got, err := Pick(context.Background(), cat, "")
	if err != nil {
		t.Fatal(err)
	}
	// "main" beats "fast" even though tiny is smaller overall.
	if got.Model.ID != "small" {
		t.Errorf("picked %q, want %q", got.Model.ID, "small")
	}
}

func TestPickFallsBackThroughRoles(t *testing.T) {
	rt, stop := liveRuntime(t, "live")
	defer stop()

	cat := &registry.Catalogue{
		Runtimes: map[string]registry.Runtime{"live": rt, "dead": deadRuntime("dead")},
		Models: []registry.Model{
			{ID: "main-model", Runtime: "dead", SizeGB: 4, Roles: []string{"main"}},
			{ID: "fast-model", Runtime: "live", SizeGB: 1, Roles: []string{"fast"}},
		},
	}

	got, err := Pick(context.Background(), cat, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Model.ID != "fast-model" {
		t.Errorf("picked %q, want the available fast model", got.Model.ID)
	}
}

// An explicitly requested model must never be silently swapped for another:
// a surprise model is worse than a clear failure.
func TestPickNeverSubstitutesAnExplicitModel(t *testing.T) {
	rt, stop := liveRuntime(t, "live")
	defer stop()

	cat := &registry.Catalogue{
		Runtimes: map[string]registry.Runtime{"live": rt, "dead": deadRuntime("dead")},
		Models: []registry.Model{
			{ID: "wanted", Runtime: "dead", SizeGB: 4, Roles: []string{"main"}},
			{ID: "available", Runtime: "live", SizeGB: 1, Roles: []string{"main"}},
		},
	}

	_, err := Pick(context.Background(), cat, "wanted")
	if err == nil {
		t.Fatal("expected an error rather than a substitution")
	}
	if !strings.Contains(err.Error(), "wanted") {
		t.Errorf("error should name the requested model, got: %v", err)
	}
}

func TestPickReportsUnknownModel(t *testing.T) {
	cat := &registry.Catalogue{Runtimes: map[string]registry.Runtime{}, Models: nil}
	_, err := Pick(context.Background(), cat, "nope")
	if err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("got %v, want an unknown-model error", err)
	}
}

// When nothing is up, the error must say what would fix it.
func TestPickExplainsWhenNothingIsAvailable(t *testing.T) {
	cat := &registry.Catalogue{
		Runtimes: map[string]registry.Runtime{"dead": deadRuntime("dead")},
		Models:   []registry.Model{{ID: "m", Runtime: "dead", Roles: []string{"main"}}},
	}
	_, err := Pick(context.Background(), cat, "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "dead") {
		t.Errorf("error should name the down runtime, got: %v", err)
	}
}

// Ties fall to the order ChooseByRole is given. That order is ID order in
// practice, because registry.Load sorts the catalogue by ID - so a tie is NOT
// a way to express a preference, and this test documents the behaviour rather
// than protecting a preference that would be built on sand. The real worked
// example: "qwen2.5-coder-cpu:1.5b" sorts before "qwen2.5-coder:1.5b" purely
// because '-' precedes ':', and the CPU-pinned model measured 71.8s against
// 7.5s on the same diff with both cold.
func TestChooseByRoleBreaksSizeTiesByTheOrderGiven(t *testing.T) {
	models := []registry.Model{
		{ID: "first", Runtime: "ollama", SizeGB: 0.99, Roles: []string{"fast"}},
		{ID: "second", Runtime: "ollama", SizeGB: 0.99, Roles: []string{"fast"}},
	}
	got, ok := ChooseByRole(models, []string{"fast"}, func(string) bool { return true })
	if !ok || got.ID != "first" {
		t.Errorf("got %q, want the first of two equal-sized models", got.ID)
	}
}

// The preference that actually matters, pinned against the real catalogue:
// `froe commit` must land on the model marco used (7.5s cold), not its
// CPU-pinned twin (71.8s). A dedicated role says so outright instead of hoping
// a size tie breaks the right way.
func TestDefaultCatalogueGivesCommitTheFastGPUModel(t *testing.T) {
	cat, err := registry.Load(t.TempDir()) // no user overrides: the built-in defaults
	if err != nil {
		t.Fatal(err)
	}

	got, ok := ChooseByRole(cat.Models, []string{"commit"}, func(string) bool { return true })
	if !ok {
		t.Fatal("no model carries the commit role - `froe commit` would fall back to a size tie")
	}
	if got.ID != "qwen2.5-coder:1.5b" {
		t.Errorf("commit role resolves to %q, want qwen2.5-coder:1.5b", got.ID)
	}
	if strings.Contains(got.ID, "-cpu") {
		t.Errorf("commit resolved to the CPU-pinned model %q - measured 70s against 4s", got.ID)
	}
}

func TestChooseByRoleStillPrefersTheSmallerModel(t *testing.T) {
	models := []registry.Model{
		{ID: "big", Runtime: "ollama", SizeGB: 19, Roles: []string{"fast"}},
		{ID: "small", Runtime: "ollama", SizeGB: 1, Roles: []string{"fast"}},
	}
	got, ok := ChooseByRole(models, []string{"fast"}, func(string) bool { return true })
	if !ok || got.ID != "small" {
		t.Errorf("got %q, want the smaller model", got.ID)
	}
}

func TestChooseByRoleFollowsThePreferenceOrder(t *testing.T) {
	models := []registry.Model{
		{ID: "heavy-one", Runtime: "ollama", SizeGB: 19, Roles: []string{"heavy"}},
		{ID: "fast-one", Runtime: "ollama", SizeGB: 1, Roles: []string{"fast"}},
	}
	up := func(string) bool { return true }

	if got, _ := ChooseByRole(models, []string{"fast", "heavy"}, up); got.ID != "fast-one" {
		t.Errorf("got %q, want fast-one", got.ID)
	}
	if got, _ := ChooseByRole(models, []string{"heavy", "fast"}, up); got.ID != "heavy-one" {
		t.Errorf("got %q, want heavy-one", got.ID)
	}
}

func TestChooseByRoleSkipsModelsWhoseRuntimeIsDown(t *testing.T) {
	models := []registry.Model{
		{ID: "lmstudio-model", Runtime: "lmstudio", SizeGB: 1, Roles: []string{"fast"}},
		{ID: "ollama-model", Runtime: "ollama", SizeGB: 2, Roles: []string{"fast"}},
	}
	got, ok := ChooseByRole(models, []string{"fast"}, func(rt string) bool { return rt == "ollama" })
	if !ok || got.ID != "ollama-model" {
		t.Errorf("got %q, want the model whose runtime is up", got.ID)
	}
}
