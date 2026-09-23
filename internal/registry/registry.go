// Package registry holds the model and runtime catalogues.
//
// Two rules from docs/DECISIONS.md are enforced by this package's shape:
//
//   - D3: models are data, never code. No model id, context size or capability
//     appears in Go source. Shipped defaults are embedded TOML; the user
//     overlays their own without recompiling.
//   - D6: a runtime is a binary path, not a name. Several llama.cpp builds may
//     coexist (upstream for most models, PrismML's fork for Bonsai's Q1_0_g128
//     kernels), so a model binds to a named Runtime rather than to "llama.cpp".
package registry

import (
	"embed"
	"fmt"
	"os"
	"sort"

	"github.com/BurntSushi/toml"
)

//go:embed defaults/*.toml
var defaults embed.FS

// Tristate captures a capability we have not yet confirmed. Unverified claims
// are recorded honestly rather than optimistically assumed true — the PrismML
// fork's GBNF support is the motivating case.
type Tristate string

const (
	Yes        Tristate = "yes"
	No         Tristate = "no"
	Unverified Tristate = "unverified"
)

func (t Tristate) True() bool { return t == Yes }

// ToolStrategy is how tool calls are extracted from a model. See
// docs/ARCHITECTURE.md §4 — this is the difference between a small model that
// can hold an agent loop together and one that cannot.
type ToolStrategy string

const (
	// ToolNative uses the provider's function-calling API.
	ToolNative ToolStrategy = "native"
	// ToolGrammar constrains the sampler with a GBNF grammar so invalid tool
	// calls are impossible. llama.cpp only, and the biggest local win available.
	ToolGrammar ToolStrategy = "grammar"
	// ToolReact is a text protocol with strict parsing and error-feedback retry.
	ToolReact ToolStrategy = "react"
)

// Runtime is an inference backend: a binary, where it listens, what it can do.
type Runtime struct {
	Name      string   `toml:"-"`
	Kind      string   `toml:"kind"`   // openai-compat | ollama | anthropic
	Binary    string   `toml:"binary"` // optional: empty for hosted/managed backends
	BaseURL   string   `toml:"base_url"`
	Grammar   Tristate `toml:"grammar"`
	APIKeyEnv string   `toml:"api_key_env"` // hosted backends: env var holding the key
	Managed   bool     `toml:"managed"`     // true if something else owns the process
	Notes     string   `toml:"notes"`
}

// Hosted reports whether this runtime executes somewhere else. The signal is
// APIKeyEnv: a backend you must authenticate to is not running on this machine,
// and nothing about local memory applies to it.
func (r Runtime) Hosted() bool { return r.APIKeyEnv != "" }

// Model is one entry in the catalogue.
//
// Note what is NOT a planning input here: ParamsB is metadata only. Fit is
// decided from SizeGB and PeakMB, per D7 — the same 27B is a 16.5 GB CPU-bound
// crawl at Q4 and a 3.5 GB near-resident model at 1.125 bpw.
type Model struct {
	ID string `toml:"id"`
	// RuntimeID is what the BACKEND calls this model, when that differs from
	// the id froe uses. LM Studio serves what we call "bonsai-27b-lmstudio"
	// under "prism-ml/bonsai-27b"; without the distinction, presence checks
	// report a loaded model as missing. Empty means the two are the same.
	RuntimeID    string       `toml:"runtime_id"`
	Runtime      string       `toml:"runtime"`
	ParamsB      float64      `toml:"params_b"`
	Quant        string       `toml:"quant"`
	SizeGB       float64      `toml:"size_gb"`
	CtxMax       int          `toml:"ctx_max"`
	PeakMB       int          `toml:"peak_mb"`  // measured peak at PeakCtx
	PeakCtx      int          `toml:"peak_ctx"` // context the measurement used
	ToolStrategy ToolStrategy `toml:"tool_strategy"`
	Vision       bool         `toml:"vision"`
	// CPUOnly marks a model deliberately pinned away from the GPU (Ollama
	// num_gpu=0, LM Studio --gpu 0). On a small card this is how a fast model
	// coexists with a GPU-resident main model instead of fighting it for VRAM.
	CPUOnly     bool     `toml:"cpu_only"`
	ThinkBudget int      `toml:"thinking_budget_tokens"`
	Roles       []string `toml:"roles"`
	Notes       string   `toml:"notes"`
	// Extra passes backend parameters straight through from TOML, so a new
	// knob never requires a code change (D3).
	Extra map[string]any `toml:"extra"`
}

// EstimatedPeakMB returns the model's peak memory, falling back to an estimate
// from on-disk size when no measurement exists. The fallback is deliberately
// crude: 15% overhead for weights plus a modest KV allowance. Any number it
// produces should be replaced by `froe bench`.
func (m Model) EstimatedPeakMB() (mb int, measured bool) {
	if m.PeakMB > 0 {
		return m.PeakMB, true
	}
	return int(m.SizeGB*1024*1.15) + 512, false
}

// ServeID is the identifier to send to the backend.
func (m Model) ServeID() string {
	if m.RuntimeID != "" {
		return m.RuntimeID
	}
	return m.ID
}

// HasRole reports whether the model is a candidate for a router role.
func (m Model) HasRole(role string) bool {
	for _, r := range m.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Catalogue is the merged view of shipped defaults plus user overlays.
type Catalogue struct {
	Models   []Model
	Runtimes map[string]Runtime
}

type modelsFile struct {
	Model []Model `toml:"model"`
}

type runtimesFile struct {
	Runtime map[string]Runtime `toml:"runtime"`
}

// Load reads the embedded defaults, then overlays any user files found in
// dir (typically ~/.config/froe). Entries sharing an id or name replace the
// shipped one outright, so a user can correct a wrong default without editing
// source. A missing user file is not an error.
func Load(dir string) (*Catalogue, error) {
	c := &Catalogue{Runtimes: map[string]Runtime{}}

	mf, err := decodeModels(mustRead("defaults/models.toml"))
	if err != nil {
		return nil, fmt.Errorf("embedded models.toml: %w", err)
	}
	rf, err := decodeRuntimes(mustRead("defaults/runtimes.toml"))
	if err != nil {
		return nil, fmt.Errorf("embedded runtimes.toml: %w", err)
	}
	c.Models = mf.Model
	for name, rt := range rf.Runtime {
		rt.Name = name
		c.Runtimes[name] = rt
	}

	if dir != "" {
		if b, err := os.ReadFile(dir + "/models.toml"); err == nil {
			user, err := decodeModels(b)
			if err != nil {
				return nil, fmt.Errorf("%s/models.toml: %w", dir, err)
			}
			c.mergeModels(user.Model)
		}
		if b, err := os.ReadFile(dir + "/runtimes.toml"); err == nil {
			user, err := decodeRuntimes(b)
			if err != nil {
				return nil, fmt.Errorf("%s/runtimes.toml: %w", dir, err)
			}
			for name, rt := range user.Runtime {
				rt.Name = name
				c.Runtimes[name] = rt
			}
		}
	}

	sort.Slice(c.Models, func(i, j int) bool { return c.Models[i].ID < c.Models[j].ID })
	return c, nil
}

// mergeModels replaces shipped entries with user entries of the same id and
// appends the rest.
func (c *Catalogue) mergeModels(users []Model) {
	idx := map[string]int{}
	for i, m := range c.Models {
		idx[m.ID] = i
	}
	for _, u := range users {
		if i, ok := idx[u.ID]; ok {
			c.Models[i] = u
			continue
		}
		idx[u.ID] = len(c.Models)
		c.Models = append(c.Models, u)
	}
}

func decodeModels(b []byte) (modelsFile, error) {
	var f modelsFile
	err := toml.Unmarshal(b, &f)
	return f, err
}

func decodeRuntimes(b []byte) (runtimesFile, error) {
	var f runtimesFile
	err := toml.Unmarshal(b, &f)
	return f, err
}

// mustRead reads an embedded file. A failure here means the binary was built
// wrong, not that the user did anything — panicking is correct.
func mustRead(name string) []byte {
	b, err := defaults.ReadFile(name)
	if err != nil {
		panic("registry: embedded file missing: " + name)
	}
	return b
}
