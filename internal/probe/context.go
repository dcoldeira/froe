package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/dcoldeira/froe/internal/registry"
)

// ContextSize reports the context window a model is ACTUALLY loaded with, or 0
// when the backend will not say.
//
// This matters more than it sounds. A registry entry records what a model can
// support — Bonsai advertises 262144 — but the runtime decides what it actually
// loads, and LM Studio's default is 8192. Budgeting against the theoretical
// figure built a 5075-token instruction block and a 5943-token map for a window
// that could hold neither, and the request failed at 9372 tokens. Ask the
// backend; never trust the registry for this.
func ContextSize(ctx context.Context, rt registry.Runtime, modelID string) int {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	base := strings.TrimSuffix(strings.TrimRight(rt.BaseURL, "/"), "/v1")

	if n := lmStudioContext(ctx, base, modelID); n > 0 {
		return n
	}
	if n := llamaCppContext(ctx, base); n > 0 {
		return n
	}
	return 0
}

// lmStudioContext reads LM Studio's native endpoint, which reports both the
// model's maximum and what it was actually loaded with.
func lmStudioContext(ctx context.Context, base, modelID string) int {
	var body struct {
		Data []struct {
			ID                  string `json:"id"`
			State               string `json:"state"`
			LoadedContextLength int    `json:"loaded_context_length"`
		} `json:"data"`
	}
	if !getJSON(ctx, base+"/api/v0/models", &body) {
		return 0
	}
	for _, m := range body.Data {
		if m.ID == modelID && m.LoadedContextLength > 0 {
			return m.LoadedContextLength
		}
	}
	// A single loaded model is unambiguous even if the id does not match the
	// registry's spelling.
	for _, m := range body.Data {
		if m.State == "loaded" && m.LoadedContextLength > 0 {
			return m.LoadedContextLength
		}
	}
	return 0
}

// llamaCppContext reads llama.cpp's /props.
func llamaCppContext(ctx context.Context, base string) int {
	var body struct {
		DefaultGenerationSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
		NCtx int `json:"n_ctx"`
	}
	if !getJSON(ctx, base+"/props", &body) {
		return 0
	}
	if n := body.DefaultGenerationSettings.NCtx; n > 0 {
		return n
	}
	return body.NCtx
}

func getJSON(ctx context.Context, url string, v any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(v) == nil
}

// ModelsPresent lists the model ids a runtime can actually serve.
//
// A runtime being up is NOT the same as a model being present: `froe doctor`
// reported qwen2.5-coder:7b as runnable for a whole session because the
// registry listed it and Ollama was running, when it had never been pulled. A
// model you cannot run should not appear runnable.
//
// Returns nil when the runtime does not answer, which callers must treat as
// "unknown", never as "empty".
func ModelsPresent(ctx context.Context, rt registry.Runtime) map[string]bool {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	base := strings.TrimRight(rt.BaseURL, "/")
	if !getJSON(ctx, base+"/models", &body) || len(body.Data) == 0 {
		return nil
	}
	out := make(map[string]bool, len(body.Data))
	for _, m := range body.Data {
		out[m.ID] = true
	}
	return out
}
