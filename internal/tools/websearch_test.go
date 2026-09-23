package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSearchRequiresAPIKey(t *testing.T) {
	t.Setenv(tavilyAPIKeyEnv, "")
	_, err := WebSearch{}.Run(context.Background(), args(t, map[string]any{"query": "golang"}), Env{})
	if err == nil || !strings.Contains(err.Error(), tavilyAPIKeyEnv) {
		t.Fatalf("got %v, want an error naming %s", err, tavilyAPIKeyEnv)
	}
}

func TestWebSearchRequiresQuery(t *testing.T) {
	t.Setenv(tavilyAPIKeyEnv, "test-key")
	_, err := WebSearch{}.Run(context.Background(), args(t, map[string]any{"query": ""}), Env{})
	if err == nil || !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("got %v, want a query-required error", err)
	}
}

func TestWebSearchFormatsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization header = %q", got)
		}
		var req tavilyRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Query != "golang generics" {
			t.Errorf("query = %q", req.Query)
		}
		json.NewEncoder(w).Encode(tavilyResponse{
			Answer: "Generics were added in Go 1.18.",
			Results: []tavilyResult{
				{Title: "Go Generics", URL: "https://go.dev/doc/generics", Content: "An overview."},
			},
		})
	}))
	defer srv.Close()

	orig := tavilySearchURL
	tavilySearchURL = srv.URL
	defer func() { tavilySearchURL = orig }()

	t.Setenv(tavilyAPIKeyEnv, "test-key")
	out, err := WebSearch{}.Run(context.Background(), args(t, map[string]any{"query": "golang generics"}), Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Generics were added in Go 1.18.", "Go Generics", "https://go.dev/doc/generics", "An overview."} {
		if !strings.Contains(out, want) {
			t.Errorf("result missing %q, got: %q", want, out)
		}
	}
}

func TestWebSearchSurfacesHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid API key"}`))
	}))
	defer srv.Close()

	orig := tavilySearchURL
	tavilySearchURL = srv.URL
	defer func() { tavilySearchURL = orig }()

	t.Setenv(tavilyAPIKeyEnv, "bad-key")
	_, err := WebSearch{}.Run(context.Background(), args(t, map[string]any{"query": "golang"}), Env{})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("got %v, want an error naming the HTTP status", err)
	}
}

func TestWebSearchIsGated(t *testing.T) {
	if !(WebSearch{}).Mutating() {
		t.Error("web_search reaches outside the machine and must be gated like bash, not auto-run like grep")
	}
}
