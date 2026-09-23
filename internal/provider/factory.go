package provider

import (
	"fmt"
	"os"

	"github.com/dcoldeira/froe/internal/registry"
)

// New builds a Provider for a model on its runtime.
//
// The switch is on Runtime.Kind, not on the runtime's name: several distinct
// llama.cpp builds coexist (D6) and all of them are "openai-compat". Kind says
// how to talk; name says where.
func New(rt registry.Runtime, m registry.Model) (Provider, error) {
	caps := Caps{
		Streaming:   true,
		MaxContext:  m.CtxMax,
		Vision:      m.Vision,
		NativeTools: m.ToolStrategy == registry.ToolNative,
		Grammar:     rt.Grammar.True(),
	}

	var apiKey string
	if rt.APIKeyEnv != "" {
		apiKey = os.Getenv(rt.APIKeyEnv)
		if apiKey == "" {
			return nil, fmt.Errorf("runtime %q needs %s to be set", rt.Name, rt.APIKeyEnv)
		}
	}

	if rt.BaseURL == "" {
		return nil, fmt.Errorf("runtime %q has no base_url", rt.Name)
	}

	switch rt.Kind {
	case "openai-compat", "ollama":
		return NewOpenAICompat(rt.Name, rt.BaseURL, apiKey, caps), nil
	case "anthropic":
		return NewAnthropic(rt.Name, rt.BaseURL, apiKey, caps), nil
	default:
		return nil, fmt.Errorf("runtime %q has unknown kind %q", rt.Name, rt.Kind)
	}
}

// RequestFor seeds a Request from a registry entry, so model-specific
// parameters travel with the model rather than being hardcoded at call sites.
func RequestFor(m registry.Model, msgs []Message) Request {
	req := Request{Model: m.ServeID(), Messages: msgs, Extra: m.Extra}
	if m.ThinkBudget > 0 {
		budget := m.ThinkBudget
		req.ThinkingBudget = &budget
	}
	return req
}
