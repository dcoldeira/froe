package resolve

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/dcoldeira/froe/internal/probe"
)

// EnsureLoaded makes the choice's runtime serve its model, running the
// runtime's load command when the model is not the loaded one. It reports
// whether it had to load.
//
// It does nothing when the runtime has no load command or will not say what
// is loaded: reloading blind could evict a model that is already serving.
//
// Also covers the idle timeout. LM Studio unloads a model after its TTL, and
// the next request then JIT-loads it with LM Studio's own defaults - four
// parallel slots, a quarter of the context froe budgets for.
func EnsureLoaded(ctx context.Context, c Choice) (bool, error) {
	if len(c.Runtime.Load) == 0 {
		return false, nil
	}
	loaded, known := probe.Loaded(ctx, c.Runtime, c.Model.ServeID())
	if !known || loaded {
		return false, nil
	}
	argv := LoadCommand(c.Runtime.Load, c.Model.ServeID())
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		return true, fmt.Errorf("loading %s: %w\n%s", c.Model.ID, err, lastLines(string(out), 5))
	}
	return true, nil
}

// LoadCommand fills "{model}" in a runtime's load template.
func LoadCommand(template []string, serveID string) []string {
	argv := make([]string, len(template))
	for i, a := range template {
		argv[i] = strings.ReplaceAll(a, "{model}", serveID)
	}
	return argv
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
