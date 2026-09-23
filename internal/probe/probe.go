// Package probe determines whether a runtime could serve a request right now.
//
// Shared by `froe doctor` (which reports it) and `froe ask` (which picks a
// model from it), so the two can never disagree about what is available.
package probe

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/dcoldeira/froe/internal/config"
	"github.com/dcoldeira/froe/internal/registry"
)

// State is the observed condition of a backend.
type State string

const (
	StateRunning  State = "running"
	StateStopped  State = "installed, not running"
	StateNoBinary State = "binary not found"
	StateNoKey    State = "no API key"
	StateKeySet   State = "key set"
	StateUnreach  State = "not responding"
)

// Report is one runtime's assessment.
type Report struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	State   State  `json:"state"`
	Detail  string `json:"detail,omitempty"`
	Grammar string `json:"grammar"`
	OK      bool   `json:"ok"`
	Notes   string `json:"notes,omitempty"`
}

// DefaultTimeout bounds a liveness check. Short, because a stopped backend
// costs the full timeout and there are several of them.
const DefaultTimeout = 800 * time.Millisecond

// Runtime assesses one backend.
//
// Hosted backends are checked by looking for their API key rather than by
// calling them: probing should not make network requests to a vendor, and a
// missing key is the failure that actually matters.
func Runtime(ctx context.Context, rt registry.Runtime) Report {
	r := Report{
		Name: rt.Name, Kind: rt.Kind,
		Grammar: string(rt.Grammar), Notes: rt.Notes,
	}

	if rt.APIKeyEnv != "" {
		if os.Getenv(rt.APIKeyEnv) != "" {
			r.State, r.OK, r.Detail = StateKeySet, true, rt.APIKeyEnv
		} else {
			r.State, r.Detail = StateNoKey, rt.APIKeyEnv+" unset"
		}
		return r
	}

	if rt.Binary != "" {
		if _, err := os.Stat(config.ExpandUser(rt.Binary)); err != nil {
			r.State, r.Detail = StateNoBinary, rt.Binary
			return r
		}
	}

	if rt.BaseURL == "" {
		r.State, r.Detail = StateNoBinary, "no base_url configured"
		return r
	}

	if EndpointUp(ctx, rt.BaseURL) {
		r.State, r.OK, r.Detail = StateRunning, true, rt.BaseURL
		return r
	}
	if rt.Binary != "" {
		// Binary present but nothing listening: startable, just not started.
		r.State, r.Detail = StateStopped, rt.BaseURL
		return r
	}
	r.State, r.Detail = StateUnreach, rt.BaseURL
	return r
}

// All probes every runtime concurrently and returns reports keyed by name.
func All(ctx context.Context, runtimes map[string]registry.Runtime) map[string]Report {
	out := make(map[string]Report, len(runtimes))
	type result struct {
		name string
		rep  Report
	}
	ch := make(chan result, len(runtimes))
	for name, rt := range runtimes {
		go func(name string, rt registry.Runtime) {
			ch <- result{name, Runtime(ctx, rt)}
		}(name, rt)
	}
	for range runtimes {
		r := <-ch
		out[r.name] = r.rep
	}
	return out
}

// EndpointUp checks for an OpenAI-compatible /models endpoint. Any HTTP
// response counts: a 401 still proves something is listening and speaking.
func EndpointUp(ctx context.Context, baseURL string) bool {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}
