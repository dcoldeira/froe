package resolve

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

// lmStudio fakes LM Studio's native model listing with one model in state.
func lmStudio(t *testing.T, id, state string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"` + id + `","state":"` + state + `"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEnsureLoadedRunsLoadWhenNotLoaded(t *testing.T) {
	srv := lmStudio(t, "prism-ml/bonsai-27b", "not-loaded")
	out := filepath.Join(t.TempDir(), "loaded")
	c := Choice{
		Model:   registry.Model{ID: "bonsai-27b-lmstudio", RuntimeID: "prism-ml/bonsai-27b"},
		Runtime: registry.Runtime{BaseURL: srv.URL + "/v1", Load: []string{"sh", "-c", "echo {model} > " + out}},
	}
	did, err := EnsureLoaded(context.Background(), c)
	if err != nil || !did {
		t.Fatalf("EnsureLoaded = %v, %v; want true, nil", did, err)
	}
	b, _ := os.ReadFile(out)
	if got := strings.TrimSpace(string(b)); got != "prism-ml/bonsai-27b" {
		t.Errorf("load command got model %q, want the ServeID", got)
	}
}

func TestEnsureLoadedLeavesALoadedModelAlone(t *testing.T) {
	srv := lmStudio(t, "prism-ml/bonsai-27b", "loaded")
	c := Choice{
		Model:   registry.Model{ID: "bonsai-27b-lmstudio", RuntimeID: "prism-ml/bonsai-27b"},
		Runtime: registry.Runtime{BaseURL: srv.URL + "/v1", Load: []string{"false"}},
	}
	if did, err := EnsureLoaded(context.Background(), c); did || err != nil {
		t.Errorf("EnsureLoaded = %v, %v; want false, nil (no reload)", did, err)
	}
}

func TestEnsureLoadedDoesNothingWhenStateUnknown(t *testing.T) {
	// A runtime that cannot report what is loaded (Ollama, llama.cpp) must not
	// be reloaded blind.
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	c := Choice{
		Model:   registry.Model{ID: "m"},
		Runtime: registry.Runtime{BaseURL: srv.URL + "/v1", Load: []string{"false"}},
	}
	if did, err := EnsureLoaded(context.Background(), c); did || err != nil {
		t.Errorf("EnsureLoaded = %v, %v; want false, nil", did, err)
	}
}

func TestEnsureLoadedReportsAFailedLoad(t *testing.T) {
	srv := lmStudio(t, "x", "not-loaded")
	c := Choice{
		Model:   registry.Model{ID: "x"},
		Runtime: registry.Runtime{BaseURL: srv.URL + "/v1", Load: []string{"sh", "-c", "echo no such model >&2; exit 1"}},
	}
	_, err := EnsureLoaded(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "no such model") {
		t.Errorf("err = %v, want the load command's output in it", err)
	}
}
