package resolve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dcoldeira/froe/internal/registry"
)

// startable is a runtime that is down until its start command runs: the
// command leaves a marker with its arguments, and the server comes up when
// the marker appears. failing makes the command fail and the server stay down.
func startable(t *testing.T, failing bool) (registry.Runtime, string) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // no model list: every model is kept
	}))
	t.Cleanup(srv.Close)
	marker := t.TempDir() + "/started"
	go func() {
		for i := 0; i < 400; i++ {
			if _, err := os.Stat(marker); err == nil {
				srv.Start()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	cmd := "echo {model} {path} {port} > " + marker + " && sleep 0.3"
	if failing {
		cmd = "exit 1"
	}
	return registry.Runtime{
		Name: "coli", Kind: "openai-compat", BaseURL: "http://" + srv.Listener.Addr().String() + "/v1",
		Start: []string{"sh", "-c", cmd},
	}, marker
}

func catalogue(coli, other registry.Runtime) *registry.Catalogue {
	return &registry.Catalogue{
		Runtimes: map[string]registry.Runtime{"coli": coli, "other": other},
		Models: []registry.Model{
			{ID: "coder", RuntimeID: "coder-id", Runtime: "coli", Path: "/models/coder", Roles: []string{"default"}},
			{ID: "local", Runtime: "other", Roles: []string{"main"}},
		},
	}
}

// With the default's runtime down and another local runtime up, froe starts
// the default instead of silently using a different model. Found 2026-11-01:
// colibri down, Ollama up, and the default went to an Ollama model.
func TestPickStartsTheDefaultsRuntimeBeforeALocalFallback(t *testing.T) {
	coli, marker := startable(t, false)
	other, stop := liveRuntime(t, "other")
	defer stop()
	var told []string
	Starting = func(rt, model string, argv []string) { told = append(told, rt+" "+model) }
	t.Cleanup(func() { Starting = nil })

	c, err := Pick(context.Background(), catalogue(coli, other), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Model.ID != "coder" {
		t.Fatalf("picked %q, want the default, coder, once its runtime started", c.Model.ID)
	}
	b, _ := os.ReadFile(marker)
	port := coli.BaseURL[strings.LastIndex(coli.BaseURL, ":")+1 : len(coli.BaseURL)-len("/v1")]
	if got := strings.TrimSpace(string(b)); got != "coder-id /models/coder "+port {
		t.Errorf("start command got %q, want the serve id, path and port", got)
	}
	if len(told) != 1 || told[0] != "coli coder" {
		t.Errorf("Starting was told %v", told)
	}
}

// A start that fails leaves the old behaviour: the local fallback, not an error.
func TestPickFallsBackLocallyWhenTheStartFails(t *testing.T) {
	coli, _ := startable(t, true)
	other, stop := liveRuntime(t, "other")
	defer stop()
	c, err := Pick(context.Background(), catalogue(coli, other), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Model.ID != "local" {
		t.Fatalf("picked %q, want the local fallback", c.Model.ID)
	}
}

// A pinned model on a down runtime is started too.
func TestPickStartsAPinnedModelsRuntime(t *testing.T) {
	coli, _ := startable(t, false)
	other, stop := liveRuntime(t, "other")
	defer stop()
	c, err := Pick(context.Background(), catalogue(coli, other), "coder")
	if err != nil || c.Model.ID != "coder" {
		t.Fatalf("got %q, %v", c.Model.ID, err)
	}
}

func TestStartCommandFillsThePlaceholders(t *testing.T) {
	rt := registry.Runtime{BaseURL: "http://127.0.0.1:8001/v1",
		Start: []string{"froe-colibri", "--model", "{path}", "--model-id", "{model}", "--port", "{port}"}}
	m := registry.Model{ID: "qwen3-coder-30b-colibri", RuntimeID: "qwen3-coder-30b", Path: "~/colibri-models/q"}
	got := strings.Join(StartCommand(rt, m), " ")
	home, _ := os.UserHomeDir()
	want := "froe-colibri --model " + home + "/colibri-models/q --model-id qwen3-coder-30b --port 8001"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// The shipped colibri entry can start itself, and the default has a path.
func TestShippedColibriCanStart(t *testing.T) {
	cat, err := registry.Load("")
	if err != nil {
		t.Fatal(err)
	}
	rt := cat.Runtimes["colibri"]
	if len(rt.Start) == 0 || rt.Start[0] != "froe-colibri" {
		t.Fatalf("colibri start = %v", rt.Start)
	}
	for _, m := range cat.Models {
		if m.ID == "qwen3-coder-30b-colibri" && m.Path == "" {
			t.Error("the default colibri model has no path to start it with")
		}
	}
}
