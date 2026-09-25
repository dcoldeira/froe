package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/registry"
)

// lmStudio serves LM Studio's native model list with two models loaded.
func lmStudio(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[
			{"id":"prism-ml/bonsai-27b","state":"loaded","loaded_context_length":4096},
			{"id":"mistralai/ministral-3-8b","state":"loaded","loaded_context_length":16384}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The runtime is asked about the model by ITS name (runtime_id), so with two
// models loaded the right window comes back.
func TestEffectiveContextAsksTheRuntimeByItsOwnName(t *testing.T) {
	srv := lmStudio(t)
	m := registry.Model{ID: "ministral-3-8b-lmstudio", RuntimeID: "mistralai/ministral-3-8b", CtxMax: 262144}
	rt := registry.Runtime{Name: "lmstudio", BaseURL: srv.URL + "/v1"}
	if got := effectiveContext(context.Background(), m, rt, style{}, true); got != 16384 {
		t.Fatalf("effective context = %d, want the loaded 16384", got)
	}
}

// A silent local runtime is budgeted conservatively, never at the registry
// maximum - that would disable every context guard at once.
func TestEffectiveContextIsConservativeWhenALocalRuntimeIsSilent(t *testing.T) {
	m := registry.Model{ID: "m", CtxMax: 262144}
	rt := registry.Runtime{Name: "lmstudio", BaseURL: "http://127.0.0.1:1/v1"}
	if got := effectiveContext(context.Background(), m, rt, style{}, true); got != conservativeContext {
		t.Fatalf("effective context = %d, want %d", got, conservativeContext)
	}
}

// A hosted runtime's window is what the registry says.
func TestEffectiveContextTrustsTheRegistryForHostedModels(t *testing.T) {
	m := registry.Model{ID: "mistral-medium-latest", CtxMax: 262144}
	rt := registry.Runtime{Name: "mistral", BaseURL: "http://127.0.0.1:1/v1", APIKeyEnv: "MISTRAL_API_KEY"}
	if got := effectiveContext(context.Background(), m, rt, style{}, true); got != 262144 {
		t.Fatalf("effective context = %d, want 262144", got)
	}
}

func TestBuildContextCarriesInstructionsAndMap(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("Focus: the Soundness Theorem."), 0o644)
	os.MkdirAll(filepath.Join(root, "src/qrl/causal"), 0o755)
	os.WriteFile(filepath.Join(root, "src/qrl/causal/witness.py"), []byte("def witness_value(W):\n    pass\n"), 0o644)

	out := buildContextFor(context.Background(), root, "fix witness_value", 8192, style{}, true)
	if !strings.Contains(out, "Soundness Theorem") {
		t.Error("project instructions missing")
	}
	if !strings.Contains(out, "src/qrl/causal/witness.py") {
		t.Errorf("map missing:\n%s", out)
	}
	// Too small a window for a map to be worth it: instructions only.
	if small := buildContextFor(context.Background(), root, "x", 1024, style{}, true); strings.Contains(small, "witness.py") {
		t.Errorf("map built for a 1024 window:\n%s", small)
	}
}
