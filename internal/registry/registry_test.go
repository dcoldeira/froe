package registry

import "testing"

// The embedded catalogue must parse, or the binary is broken in a way no user
// action can fix.
func TestEmbeddedDefaultsLoad(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Models) == 0 {
		t.Fatal("no models in embedded catalogue")
	}
	if len(c.Runtimes) == 0 {
		t.Fatal("no runtimes in embedded catalogue")
	}
	for name, rt := range c.Runtimes {
		if rt.Name != name {
			t.Errorf("runtime %q has Name %q - key not propagated", name, rt.Name)
		}
	}
	for _, m := range c.Models {
		if _, ok := c.Runtimes[m.Runtime]; !ok {
			t.Errorf("model %q references undefined runtime %q", m.ID, m.Runtime)
		}
	}
}

// D6: Bonsai must not be bound to upstream llama.cpp, which cannot load
// Q1_0_g128 at all.
func TestBonsaiBindsToTheFork(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range c.Models {
		if m.ID == "bonsai-27b" {
			if m.Runtime != "llamacpp-prismml" {
				t.Errorf("bonsai-27b runtime = %q, want llamacpp-prismml", m.Runtime)
			}
			return
		}
	}
	t.Fatal("bonsai-27b missing from catalogue")
}

func TestEstimatedPeakMB(t *testing.T) {
	m := Model{SizeGB: 4, PeakMB: 9000}
	if mb, measured := m.EstimatedPeakMB(); mb != 9000 || !measured {
		t.Errorf("got (%d, %v), want (9000, true)", mb, measured)
	}
	m = Model{SizeGB: 4}
	mb, measured := m.EstimatedPeakMB()
	if measured {
		t.Error("absent peak_mb should not report as measured")
	}
	if mb <= 4096 {
		t.Errorf("estimate %d MB should exceed raw weight size", mb)
	}
}

func TestHasRole(t *testing.T) {
	m := Model{Roles: []string{"main", "heavy"}}
	if !m.HasRole("heavy") {
		t.Error("HasRole(heavy) = false")
	}
	if m.HasRole("fast") {
		t.Error("HasRole(fast) = true")
	}
}

// The backend's name for a model can differ from ours. llama-server serves
// "qwen3-coder-30b-llamacpp" under its --alias "qwen3-coder-30b"; conflating
// the two made a loaded model report as missing.
func TestServeIDPrefersRuntimeID(t *testing.T) {
	m := Model{ID: "qwen3-coder-30b-llamacpp", RuntimeID: "qwen3-coder-30b"}
	if got := m.ServeID(); got != "qwen3-coder-30b" {
		t.Errorf("ServeID = %q", got)
	}
	plain := Model{ID: "qwen2.5-coder:7b"}
	if got := plain.ServeID(); got != "qwen2.5-coder:7b" {
		t.Errorf("ServeID = %q, want the id when runtime_id is unset", got)
	}
}

// The shipped default must carry the name llama-server serves it under, and
// the GGUF path froe-llamacpp starts it from.
func TestShippedDefaultModelHasRuntimeIDAndPath(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range c.Models {
		if m.ID == "qwen3-coder-30b-llamacpp" {
			if m.RuntimeID == "" {
				t.Error("qwen3-coder-30b-llamacpp has no runtime_id; presence checks will fail")
			}
			if m.Path == "" {
				t.Error("qwen3-coder-30b-llamacpp has no path; froe cannot start llama-server for it")
			}
			return
		}
	}
	t.Fatal("qwen3-coder-30b-llamacpp missing from the catalogue")
}
