package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dcoldeira/froe/internal/registry"
)

// colibriModels is the shape `coli serve` v2.1.0 answers at /v1/models,
// trimmed to the fields that matter (captured 2026-11-01).
const colibriModels = `{"object":"list","data":[{"id":"qwen3-coder-30b","object":"model",
"owned_by":"colibri","context_length":8192,"max_model_len":8192}]}`

func serve(t *testing.T, routes map[string]string) registry.Runtime {
	t.Helper()
	mux := http.NewServeMux()
	for path, body := range routes {
		body := body
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return registry.Runtime{BaseURL: srv.URL + "/v1"}
}

func TestContextSizeFromModelsList(t *testing.T) {
	rt := serve(t, map[string]string{"/v1/models": colibriModels})
	if n := ContextSize(context.Background(), rt, "qwen3-coder-30b"); n != 8192 {
		t.Fatalf("matching id: got %d, want 8192", n)
	}
	// One served model is unambiguous even when the registry spells it differently.
	if n := ContextSize(context.Background(), rt, "qwen3-coder-30b-colibri"); n != 8192 {
		t.Fatalf("single model, other id: got %d, want 8192", n)
	}
}

func TestContextSizeMaxModelLenOnly(t *testing.T) {
	rt := serve(t, map[string]string{"/v1/models": `{"data":[{"id":"a","max_model_len":32768},{"id":"b","max_model_len":4096}]}`})
	if n := ContextSize(context.Background(), rt, "b"); n != 4096 {
		t.Fatalf("got %d, want 4096", n)
	}
	// Two models and no match: refuse to guess.
	if n := ContextSize(context.Background(), rt, "c"); n != 0 {
		t.Fatalf("ambiguous: got %d, want 0", n)
	}
}

func TestContextSizeLlamaCppWinsOverModelsList(t *testing.T) {
	rt := serve(t, map[string]string{
		"/props":     `{"default_generation_settings":{"n_ctx":16384}}`,
		"/v1/models": colibriModels,
	})
	if n := ContextSize(context.Background(), rt, "qwen3-coder-30b"); n != 16384 {
		t.Fatalf("got %d, want 16384", n)
	}
}
