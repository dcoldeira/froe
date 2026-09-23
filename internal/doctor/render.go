package doctor

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

// ANSI styling, disabled when stdout is not a terminal or NO_COLOR is set.
type style struct{ on bool }

func newStyle(w io.Writer) style {
	if os.Getenv("NO_COLOR") != "" {
		return style{}
	}
	f, ok := w.(*os.File)
	if !ok {
		return style{}
	}
	info, err := f.Stat()
	if err != nil {
		return style{}
	}
	return style{on: info.Mode()&os.ModeCharDevice != 0}
}

func (s style) wrap(code, text string) string {
	if !s.on {
		return text
	}
	return "\033[" + code + "m" + text + "\033[0m"
}

func (s style) bold(t string) string   { return s.wrap("1", t) }
func (s style) dim(t string) string    { return s.wrap("2", t) }
func (s style) green(t string) string  { return s.wrap("32", t) }
func (s style) yellow(t string) string { return s.wrap("33", t) }
func (s style) red(t string) string    { return s.wrap("31", t) }

// marker returns the status glyph and colour for a fit class.
func (s style) marker(f Fit) string {
	switch f {
	case FitResident:
		return s.green("●")
	case FitOffload:
		return s.yellow("◐")
	case FitCPU:
		return s.yellow("○")
	case FitTooBig:
		return s.red("✕")
	case FitMissing:
		return s.yellow("↓")
	case FitHosted:
		return s.green("☁")
	default:
		return s.dim("·")
	}
}

// Render writes the human-readable report.
func Render(w io.Writer, r *Report) {
	s := newStyle(w)

	fmt.Fprintln(w, s.bold("\nHost"))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  hostname\t%s\n", r.Host.Hostname)
	fmt.Fprintf(tw, "  cpu\t%s  (%d threads)\n", or(r.Host.CPUModel, "unknown"), r.Host.Threads)
	fmt.Fprintf(tw, "  ram\t%s total, %s available\n", gb(r.Host.RAMTotalMB), gb(r.Host.RAMAvailMB))
	if r.Host.GPU.Detected {
		fmt.Fprintf(tw, "  gpu\t%s  %s VRAM (%s free)\n",
			r.Host.GPU.Name, gb(r.Host.GPU.VRAMMB), gb(r.Host.GPU.FreeVRAMMB()))
	} else {
		fmt.Fprintf(tw, "  gpu\t%s\n", s.yellow("none detected - everything will be CPU-bound"))
	}
	fmt.Fprintf(tw, "  config\t%s\n", r.ConfigDir)
	tw.Flush()

	fmt.Fprintln(w, s.bold("\nRuntimes"))
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, rt := range r.Runtimes {
		glyph := s.red("✕")
		if rt.OK {
			glyph = s.green("●")
		}
		grammar := ""
		if rt.Grammar == "yes" {
			grammar = s.dim("gbnf")
		} else if rt.Grammar == "unverified" {
			grammar = s.dim("gbnf?")
		}
		fmt.Fprintf(tw, "  %s %s\t%s\t%s\t%s\n",
			glyph, rt.Name, string(rt.State), s.dim(rt.Detail), grammar)
	}
	tw.Flush()

	fmt.Fprintln(w, s.bold("\nModels"))
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "   \t%s\t%s\t%s\t%s\t%s\t%s\n",
		s.dim("MODEL"), s.dim("FIT"), s.dim("DISK"), s.dim("PEAK"), s.dim("GPU"), s.dim("RUNTIME"))
	for _, m := range r.Models {
		peak := gb(m.PeakMB)
		if !m.PeakMeasured {
			peak = "~" + peak
		}
		gpu := "-"
		if m.Fit == FitResident || m.Fit == FitOffload {
			gpu = fmt.Sprintf("%d%%", m.GPUPercent)
		}
		fit := string(m.Fit)
		if m.Tight {
			fit = s.yellow(fit + "!")
		}
		size := fmt.Sprintf("%.1f GB", m.SizeGB)
		if m.Fit == FitHosted {
			size, peak = "-", "-"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.marker(m.Fit), m.ID, fit, size, peak, gpu, s.dim(m.Runtime))
	}
	tw.Flush()

	// Reasons, only where something is wrong — the table stays scannable.
	var notes []string
	for _, m := range r.Models {
		if m.Reason != "" {
			notes = append(notes, fmt.Sprintf("  %s %s", s.dim(m.ID+":"), m.Reason))
		}
	}
	if len(notes) > 0 {
		fmt.Fprintln(w, s.dim("\n  why"))
		fmt.Fprintln(w, strings.Join(notes, "\n"))
	}

	fmt.Fprintln(w, s.dim("\n  ~peak = estimated from disk size, not measured. `froe bench` replaces these."))
	summary(w, s, r)
	fmt.Fprintln(w)
}

// summary states plainly what is usable right now, since that is the question
// the command is actually being asked.
func summary(w io.Writer, s style, r *Report) {
	var usable, blocked int
	for _, m := range r.Models {
		switch m.Fit {
		case FitResident, FitOffload, FitCPU:
			usable++
		case FitBlocked:
			blocked++
		}
	}
	fmt.Fprintf(w, "\n  %s %d of %d models runnable now",
		s.bold("summary:"), usable, len(r.Models))
	if blocked > 0 {
		fmt.Fprintf(w, ", %s", s.yellow(fmt.Sprintf("%d blocked on a missing runtime", blocked)))
	}
	fmt.Fprintln(w)
}

func gb(mb int) string {
	if mb <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f GB", float64(mb)/1024)
}

func or(a, b string) string {
	if a == "" {
		return b
	}
	return a
}
