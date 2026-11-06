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
// loads, and llama-server starts with the -c it is given (8192 from
// froe-llamacpp). Budgeting against the theoretical
// figure built a 5075-token instruction block and a 5943-token map for a window
// that could hold neither, and the request failed at 9372 tokens. Ask the
// backend; never trust the registry for this.
func ContextSize(ctx context.Context, rt registry.Runtime, modelID string) int {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	base := strings.TrimSuffix(strings.TrimRight(rt.BaseURL, "/"), "/v1")

	if n := llamaCppContext(ctx, base); n > 0 {
		return n
	}
	if n := openAIModelsContext(ctx, base, modelID); n > 0 {
		return n
	}
	return 0
}

// openAIModelsContext reads the context window from the model list at
// /v1/models, for runtimes that put it there: colibri reports context_length
// (and max_model_len, as vLLM does), both the window the server was started
// with, not the model's theoretical maximum.
func openAIModelsContext(ctx context.Context, base, modelID string) int {
	var body struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			MaxModelLen   int    `json:"max_model_len"`
		} `json:"data"`
	}
	if !getJSON(ctx, base+"/v1/models", &body) {
		return 0
	}
	window := func(i int) int {
		if n := body.Data[i].ContextLength; n > 0 {
			return n
		}
		return body.Data[i].MaxModelLen
	}
	for i, m := range body.Data {
		if m.ID == modelID {
			return window(i)
		}
	}
	// A server with one model is unambiguous even when its id is spelled
	// differently from the registry's.
	if len(body.Data) == 1 {
		return window(0)
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
