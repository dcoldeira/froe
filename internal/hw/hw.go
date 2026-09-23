// Package hw detects the capabilities of the machine froe is running on.
//
// Detection is best-effort and never fatal: a field that cannot be determined
// is left zero and reported as unknown rather than guessed at. Nothing here
// knows about models — hw answers "what is this box", and internal/doctor
// decides what that means for a given model.
package hw

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// GPU is a single discrete or integrated graphics device.
type GPU struct {
	Name     string `json:"name"`
	VRAMMB   int    `json:"vram_mb"`
	UsedMB   int    `json:"used_mb"`
	Driver   string `json:"driver"`
	Detected bool   `json:"detected"`
}

// FreeVRAMMB is the VRAM actually available to us, not the card's nameplate
// capacity. A compositor or another process may already hold some.
func (g GPU) FreeVRAMMB() int {
	if !g.Detected {
		return 0
	}
	if free := g.VRAMMB - g.UsedMB; free > 0 {
		return free
	}
	return 0
}

// Host is everything froe knows about the current machine.
type Host struct {
	Hostname   string `json:"hostname"`
	CPUModel   string `json:"cpu_model"`
	Threads    int    `json:"threads"`
	RAMTotalMB int    `json:"ram_total_mb"`
	RAMAvailMB int    `json:"ram_avail_mb"`
	GPU        GPU    `json:"gpu"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
}

// Detect gathers host capabilities. It does not return an error: partial
// information is more useful than none, and every consumer handles zero values.
func Detect() Host {
	h := Host{
		Threads: runtime.NumCPU(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
	h.Hostname, _ = os.Hostname()
	h.CPUModel = cpuModel()
	h.RAMTotalMB, h.RAMAvailMB = memory()
	h.GPU = detectNVIDIA()
	return h
}

// cpuModel reads the first "model name" line from /proc/cpuinfo.
func cpuModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "model name", "Model": // x86 uses the former, arm64 the latter
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// memory reads MemTotal and MemAvailable from /proc/meminfo, in MB.
//
// MemAvailable is the number that matters: MemTotal includes memory held by
// reclaimable page cache, so sizing a model against it overstates what can
// actually be allocated.
func memory() (total, avail int) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(v) // "  30412345 kB"
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		switch k {
		case "MemTotal":
			total = kb / 1024
		case "MemAvailable":
			avail = kb / 1024
		}
		if total > 0 && avail > 0 {
			break
		}
	}
	return total, avail
}

// detectNVIDIA shells out to nvidia-smi. Absence of the tool is the common
// case on machines without an NVIDIA card and is not an error.
func detectNVIDIA() GPU {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return GPU{}
	}
	out, err := exec.Command(bin,
		"--query-gpu=name,memory.total,memory.used,driver_version",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return GPU{}
	}
	// Only the first GPU is considered; multi-GPU scheduling is out of scope.
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	parts := strings.Split(line, ",")
	if len(parts) < 4 {
		return GPU{}
	}
	total, err1 := strconv.Atoi(strings.TrimSpace(parts[1]))
	used, err2 := strconv.Atoi(strings.TrimSpace(parts[2]))
	if err1 != nil || err2 != nil {
		return GPU{}
	}
	return GPU{
		Name:     strings.TrimSpace(parts[0]),
		VRAMMB:   total,
		UsedMB:   used,
		Driver:   strings.TrimSpace(parts[3]),
		Detected: true,
	}
}
