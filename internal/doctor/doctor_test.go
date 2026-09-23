package doctor

import (
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/hw"
	"github.com/dcoldeira/froe/internal/registry"
)

// host builds a test host. RAM figures are in MB.
func host(vramTotal, vramUsed, ramTotal, ramAvail int) hw.Host {
	h := hw.Host{RAMTotalMB: ramTotal, RAMAvailMB: ramAvail}
	if vramTotal > 0 {
		h.GPU = hw.GPU{Name: "test", VRAMMB: vramTotal, UsedMB: vramUsed, Detected: true}
	}
	return h
}

func okRuntimes() map[string]bool { return map[string]bool{"rt": true} }

func TestAssessFit(t *testing.T) {
	// The ultrabook: 4 GB VRAM, 30 GB RAM.
	ultrabook := host(4096, 0, 30720, 18000)
	// The workstation: 16 GB VRAM, 16 GB RAM.
	workstation := host(16384, 0, 16384, 12000)

	tests := []struct {
		name    string
		model   registry.Model
		host    hw.Host
		wantFit Fit
		wantGPU int
	}{
		{
			name:    "bonsai fits entirely in workstation VRAM",
			model:   registry.Model{ID: "bonsai-27b", Runtime: "rt", SizeGB: 3.53, PeakMB: 5325},
			host:    workstation,
			wantFit: FitResident,
			wantGPU: 100,
		},
		{
			name:  "bonsai spills slightly on the ultrabook",
			model: registry.Model{ID: "bonsai-27b", Runtime: "rt", SizeGB: 3.53, PeakMB: 5325},
			host:  ultrabook,
			// 5325 MB peak vs 4096 MB VRAM: most of it is resident.
			wantFit: FitOffload,
			wantGPU: 76,
		},
		{
			name:    "27B at Q4 is a heavy offload, not a failure",
			model:   registry.Model{ID: "q4-27b", Runtime: "rt", SizeGB: 16.5, PeakMB: 17000},
			host:    ultrabook,
			wantFit: FitOffload,
			wantGPU: 24,
		},
		{
			name:    "model larger than VRAM plus usable RAM will not load",
			model:   registry.Model{ID: "huge", Runtime: "rt", SizeGB: 70, PeakMB: 72000},
			host:    ultrabook,
			wantFit: FitTooBig,
		},
		{
			name:    "no GPU falls back to CPU rather than reporting offload",
			model:   registry.Model{ID: "small", Runtime: "rt", SizeGB: 1, PeakMB: 1600},
			host:    host(0, 0, 30720, 18000),
			wantFit: FitCPU,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := assess(tc.model, tc.host, okRuntimes(), nil, nil)
			if got.Fit != tc.wantFit {
				t.Errorf("fit = %q, want %q (reason: %s)", got.Fit, tc.wantFit, got.Reason)
			}
			if tc.wantGPU != 0 && got.GPUPercent != tc.wantGPU {
				t.Errorf("gpu%% = %d, want %d", got.GPUPercent, tc.wantGPU)
			}
		})
	}
}

// A verdict that changes because a browser is open is not a verdict. Capacity
// is judged against total RAM; current pressure is reported separately.
func TestFitIsIndependentOfCurrentRAMPressure(t *testing.T) {
	m := registry.Model{ID: "mid", Runtime: "rt", SizeGB: 19, PeakMB: 22300}

	idle := assess(m, host(4096, 0, 30720, 28000), okRuntimes(), nil, nil)
	busy := assess(m, host(4096, 0, 30720, 6000), okRuntimes(), nil, nil)

	if idle.Fit != busy.Fit {
		t.Fatalf("fit changed with RAM pressure: idle=%q busy=%q", idle.Fit, busy.Fit)
	}
	if idle.Tight {
		t.Error("idle host should not be flagged tight")
	}
	if !busy.Tight {
		t.Error("busy host should be flagged tight")
	}
}

// A model that fits perfectly is useless if nothing can serve it.
func TestBlockedWhenRuntimeUnavailable(t *testing.T) {
	m := registry.Model{ID: "bonsai-27b", Runtime: "llamacpp-prismml", SizeGB: 3.53, PeakMB: 5325}

	got := assess(m, host(16384, 0, 16384, 12000), map[string]bool{"llamacpp-prismml": false}, nil, nil)
	if got.Fit != FitBlocked {
		t.Errorf("fit = %q, want %q", got.Fit, FitBlocked)
	}

	got = assess(m, host(16384, 0, 16384, 12000), map[string]bool{}, nil, nil)
	if got.Fit != FitBlocked {
		t.Errorf("undefined runtime: fit = %q, want %q", got.Fit, FitBlocked)
	}
}

// Peak memory must be marked estimated when it was not measured, so the
// distinction survives into the report and into `froe bench` later.
func TestEstimatedPeakIsFlagged(t *testing.T) {
	measured := registry.Model{ID: "a", Runtime: "rt", SizeGB: 3.53, PeakMB: 5325}
	if r := assess(measured, host(16384, 0, 16384, 12000), okRuntimes(), nil, nil); !r.PeakMeasured {
		t.Error("explicit peak_mb should be reported as measured")
	}
	estimated := registry.Model{ID: "b", Runtime: "rt", SizeGB: 3.53}
	r := assess(estimated, host(16384, 0, 16384, 12000), okRuntimes(), nil, nil)
	if r.PeakMeasured {
		t.Error("absent peak_mb should be reported as estimated")
	}
	const rawWeightsMB = 3.53 * 1024
	if float64(r.PeakMB) <= rawWeightsMB {
		t.Errorf("estimate %d MB should exceed raw weight size", r.PeakMB)
	}
}

// A CPU-pinned model must never be credited with VRAM it will not use.
// On a 4 GB card this is how a fast model coexists with a GPU-resident main
// model: Ollama does NOT fall back to CPU on its own, it hard-fails with a
// CUDA OOM, so the pinning has to be deliberate and visible.
func TestCPUOnlyModelIsNotCreditedWithVRAM(t *testing.T) {
	// Plenty of free VRAM available — a non-pinned model would claim it.
	h := host(16384, 0, 30720, 18000)

	normal := assess(registry.Model{ID: "n", Runtime: "rt", SizeGB: 1, PeakMB: 1600}, h, okRuntimes(), nil, nil)
	if normal.Fit != FitResident {
		t.Fatalf("control: fit = %q, want resident", normal.Fit)
	}

	pinned := assess(registry.Model{ID: "p", Runtime: "rt", SizeGB: 1, PeakMB: 1600, CPUOnly: true}, h, okRuntimes(), nil, nil)
	if pinned.Fit != FitCPU {
		t.Errorf("fit = %q, want %q", pinned.Fit, FitCPU)
	}
	if pinned.GPUPercent != 0 {
		t.Errorf("gpu%% = %d, want 0 for a CPU-pinned model", pinned.GPUPercent)
	}
	if pinned.Reason == normal.Reason {
		t.Error("a pinned model should explain itself differently from a GPU-less host")
	}
}

// The shipped catalogue must carry the CPU-pinned variant, since the
// contention it solves is a property of the 4 GB machine, not of one session.
func TestShippedCatalogueHasACPUPinnedFastModel(t *testing.T) {
	cat, err := registry.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range cat.Models {
		if m.CPUOnly && m.HasRole("fast") {
			return
		}
	}
	t.Error("no CPU-pinned model with the fast role in the shipped catalogue")
}

// A runtime being up is not the same as a model being present. `froe doctor`
// listed qwen2.5-coder:7b as runnable for a whole session because Ollama was
// running and the registry named it — it had never been pulled.
func TestModelMissingFromRuntimeIsNotRunnable(t *testing.T) {
	h := host(16384, 0, 30720, 18000)
	m := registry.Model{ID: "qwen2.5-coder:7b", Runtime: "rt", SizeGB: 4.7, PeakMB: 5900}

	present := map[string]map[string]bool{"rt": {"some-other-model": true}}
	got := assess(m, h, okRuntimes(), present, nil)

	if got.Fit != FitMissing {
		t.Errorf("fit = %q, want %q", got.Fit, FitMissing)
	}
	if !strings.Contains(got.Reason, "pull") {
		t.Errorf("reason should say what to do, got %q", got.Reason)
	}
}

// An unreachable runtime tells us nothing about which models it has, and must
// not be read as "it has none".
func TestUnknownPresenceIsNotTreatedAsMissing(t *testing.T) {
	h := host(16384, 0, 30720, 18000)
	m := registry.Model{ID: "x", Runtime: "rt", SizeGB: 1, PeakMB: 1600}

	got := assess(m, h, okRuntimes(), map[string]map[string]bool{"rt": nil}, nil)
	if got.Fit == FitMissing {
		t.Error("unknown presence was treated as missing")
	}
}

// A hosted model has no local footprint. Without special-casing it, SizeGB 0
// estimates a 512 MB peak, that fits in any free VRAM, and doctor cheerfully
// reports "resident" for a model running on someone else's hardware.
func TestHostedModelIsNotCreditedWithLocalMemory(t *testing.T) {
	m := registry.Model{ID: "mistral-medium-latest", Runtime: "mistral", CtxMax: 262144}
	h := host(4096, 0, 30720, 28000)

	got := assess(m, h, map[string]bool{"mistral": true}, nil, map[string]bool{"mistral": true})

	if got.Fit != FitHosted {
		t.Errorf("fit = %q, want %q", got.Fit, FitHosted)
	}
	if got.GPUPercent != 0 {
		t.Errorf("GPUPercent = %d, want 0 - it uses none of this machine's GPU", got.GPUPercent)
	}
	if got.PeakMB != 0 || got.PeakMeasured {
		t.Errorf("PeakMB = %d (measured=%v), want 0/false", got.PeakMB, got.PeakMeasured)
	}
}

// A hosted runtime with no key is unusable, and must not read as available.
func TestHostedModelWithUnavailableRuntimeIsBlocked(t *testing.T) {
	m := registry.Model{ID: "mistral-medium-latest", Runtime: "mistral"}
	h := host(4096, 0, 30720, 28000)

	got := assess(m, h, map[string]bool{"mistral": false}, nil, map[string]bool{"mistral": true})

	if got.Fit != FitBlocked {
		t.Errorf("fit = %q, want %q", got.Fit, FitBlocked)
	}
}
