// Package doctor answers "what can this machine actually run, right now".
//
// It exists because the answer is not obvious and getting it wrong is
// expensive: discovering mid-task that a model is CPU-bound costs minutes of
// silence, and discovering a runtime is missing costs a failed session. Fit is
// computed from footprint and measured peak memory, never from parameter count
// (docs/DECISIONS.md D7).
package doctor

import (
	"context"
	"sort"

	"github.com/dcoldeira/froe/internal/config"
	"github.com/dcoldeira/froe/internal/hw"
	"github.com/dcoldeira/froe/internal/probe"
	"github.com/dcoldeira/froe/internal/registry"
)

// osReserveMB is held back from total RAM when judging capacity: the kernel,
// the desktop and froe itself all need room, and a model sized to the last
// megabyte thrashes rather than runs.
const osReserveMB = 2048

// Fit is how a model would run on this host.
type Fit string

const (
	// FitResident: peak memory fits in free VRAM. The fast case.
	FitResident Fit = "resident"
	// FitOffload: partially on GPU, remainder in system RAM.
	FitOffload Fit = "offload"
	// FitCPU: no usable GPU, but it fits in RAM. Expect slow prefill.
	FitCPU Fit = "cpu"
	// FitTooBig: exceeds free VRAM plus usable RAM. Will not load.
	FitTooBig Fit = "too-big"
	// FitBlocked: would fit, but its runtime is unavailable.
	FitBlocked Fit = "blocked"
	// FitMissing: the runtime is up but does not have this model.
	FitMissing Fit = "not pulled"
	// FitHosted: runs on someone else's hardware, so local memory says nothing
	// about it. Reported separately rather than as a fit this host achieved.
	FitHosted Fit = "hosted"
)

// ModelReport is one model's assessment against this host.
type ModelReport struct {
	ID           string   `json:"id"`
	Runtime      string   `json:"runtime"`
	Quant        string   `json:"quant"`
	SizeGB       float64  `json:"size_gb"`
	PeakMB       int      `json:"peak_mb"`
	PeakMeasured bool     `json:"peak_measured"`
	Fit          Fit      `json:"fit"`
	Tight        bool     `json:"tight"`
	GPUPercent   int      `json:"gpu_percent"`
	CtxMax       int      `json:"ctx_max"`
	Tools        string   `json:"tool_strategy"`
	Roles        []string `json:"roles"`
	Reason       string   `json:"reason,omitempty"`
	Notes        string   `json:"notes,omitempty"`
}

// Report is the whole doctor output.
type Report struct {
	Host      hw.Host        `json:"host"`
	Runtimes  []probe.Report `json:"runtimes"`
	Models    []ModelReport  `json:"models"`
	ConfigDir string         `json:"config_dir"`
}

// Run gathers everything.
func Run(ctx context.Context) (*Report, error) {
	host := hw.Detect()
	dir := config.Dir()

	cat, err := registry.Load(dir)
	if err != nil {
		return nil, err
	}

	probes := probe.All(ctx, cat.Runtimes)

	names := make([]string, 0, len(probes))
	for name := range probes {
		names = append(names, name)
	}
	sort.Strings(names)

	rep := &Report{Host: host, ConfigDir: dir}
	available := make(map[string]bool, len(probes))
	present := make(map[string]map[string]bool, len(probes))
	for _, name := range names {
		rep.Runtimes = append(rep.Runtimes, probes[name])
		available[name] = probes[name].OK
		if probes[name].OK {
			present[name] = probe.ModelsPresent(ctx, cat.Runtimes[name])
		}
	}

	hosted := make(map[string]bool, len(cat.Runtimes))
	for name, rt := range cat.Runtimes {
		hosted[name] = rt.Hosted()
	}

	for _, m := range cat.Models {
		rep.Models = append(rep.Models, assess(m, host, available, present, hosted))
	}
	return rep, nil
}

// assess decides how a model would run here.
func assess(m registry.Model, h hw.Host, runtimeOK map[string]bool, present map[string]map[string]bool, hosted map[string]bool) ModelReport {
	peak, measured := m.EstimatedPeakMB()
	r := ModelReport{
		ID: m.ID, Runtime: m.Runtime, Quant: m.Quant, SizeGB: m.SizeGB,
		PeakMB: peak, PeakMeasured: measured, CtxMax: m.CtxMax,
		Tools: string(m.ToolStrategy), Roles: m.Roles, Notes: m.Notes,
	}

	// A hosted model occupies nothing here. Running it through the fit
	// arithmetic would credit a 0 GB entry with a perfect GPU fit and report
	// "resident" for a model that is not on this machine at all.
	if hosted[m.Runtime] {
		r.Fit, r.PeakMB, r.PeakMeasured = FitHosted, 0, false
		if ok, known := runtimeOK[m.Runtime]; !known || !ok {
			r.Fit = FitBlocked
			r.Reason = "runtime " + m.Runtime + " unavailable"
		}
		return r
	}

	freeVRAM := h.GPU.FreeVRAMMB()
	// A CPU-pinned model must never be credited with VRAM it will not use, or
	// doctor reports a fit the model cannot achieve.
	if m.CPUOnly {
		freeVRAM = 0
	}

	// Capacity is judged against TOTAL RAM less an OS reserve, not against
	// MemAvailable. MemAvailable moves with whatever else is running, and a
	// verdict that flips because a browser is open is not a verdict. Current
	// pressure is reported separately as "tight".
	usableRAM := h.RAMTotalMB - osReserveMB
	if usableRAM < 0 {
		usableRAM = 0
	}

	switch {
	case peak <= freeVRAM:
		r.Fit, r.GPUPercent = FitResident, 100
	case peak <= freeVRAM+usableRAM:
		if freeVRAM > 0 {
			r.Fit = FitOffload
			r.GPUPercent = freeVRAM * 100 / peak
			r.Reason = "spills to system RAM"
		} else {
			r.Fit = FitCPU
			if m.CPUOnly {
				r.Reason = "pinned to CPU - leaves the GPU free for a resident model"
			} else {
				r.Reason = "no GPU - prefill will dominate"
			}
		}
	default:
		r.Fit = FitTooBig
		r.Reason = "exceeds free VRAM plus usable RAM"
	}

	// Loadable in principle, but not against what is free right now.
	if r.Fit == FitOffload || r.Fit == FitCPU {
		if peak > freeVRAM+h.RAMAvailMB {
			r.Tight = true
			r.Reason = "needs more RAM than is free right now - close applications or expect swapping"
		}
	}

	// A perfect fit is worthless if nothing can serve it.
	if ok, known := runtimeOK[m.Runtime]; !known {
		r.Fit = FitBlocked
		r.Reason = "runtime " + m.Runtime + " not defined"
	} else if !ok && r.Fit != FitTooBig {
		r.Fit = FitBlocked
		r.Reason = "runtime " + m.Runtime + " unavailable"
	} else if ok {
		// The runtime answering is not the same as it having this model.
		if have := present[m.Runtime]; have != nil && !have[m.ServeID()] {
			r.Fit = FitMissing
			r.Reason = "not present in " + m.Runtime + " - pull it first"
		}
	}
	return r
}
