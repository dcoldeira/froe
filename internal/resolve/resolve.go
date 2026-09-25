// Package resolve picks which model to use for a request.
//
// Split out from the CLI because the agent (Phase 3) and the router (Phase 8)
// need the same answer, and two implementations would drift.
package resolve

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dcoldeira/froe/internal/probe"
	"github.com/dcoldeira/froe/internal/registry"
)

// Choice is a resolved model plus the runtime that will serve it.
type Choice struct {
	Model   registry.Model
	Runtime registry.Runtime
}

// RolePreference is the order roles are tried when nothing is pinned.
// "default" names the one measured pick, so it wins over smaller "main"
// models without an ordering trick (see ChooseByRole). "main" next because ask
// is interactive; "fast" is a usable fallback, "heavy" a last resort since it
// is the slowest thing on the box.
var RolePreference = []string{"default", "main", "fast", "heavy"}

// Pick chooses a model.
//
// When id is non-empty it is honoured exactly, and an unavailable runtime is an
// error rather than a silent substitution — a surprise model is worse than a
// clear failure. When id is empty, the best available model is chosen by role.
func Pick(ctx context.Context, cat *registry.Catalogue, id string) (Choice, error) {
	return PickWithPreference(ctx, cat, id, RolePreference)
}

// PickWithPreference is Pick with an explicit role order, for callers whose
// best model is not the interactive default. Writing a commit message wants
// the "fast" role first: it is a short, well-shaped job that a small model
// does in seconds, where the main model would spend minutes on it.
func PickWithPreference(ctx context.Context, cat *registry.Catalogue, id string, pref []string) (Choice, error) {
	if id != "" {
		for _, m := range cat.Models {
			if m.ID != id {
				continue
			}
			rt, ok := cat.Runtimes[m.Runtime]
			if !ok {
				return Choice{}, fmt.Errorf("model %q references undefined runtime %q", id, m.Runtime)
			}
			if rep := probe.Runtime(ctx, rt); !rep.OK {
				return Choice{}, fmt.Errorf("model %q needs runtime %q, which is %s (%s)",
					id, rt.Name, rep.State, rep.Detail)
			}
			return Choice{Model: m, Runtime: rt}, nil
		}
		return Choice{}, fmt.Errorf("unknown model %q (try `froe doctor` to list them)", id)
	}

	probes := probe.All(ctx, cat.Runtimes)

	if m, ok := ChooseByRole(pulled(ctx, cat, probes), pref, func(rt string) bool { return probes[rt].OK }); ok {
		return Choice{Model: m, Runtime: cat.Runtimes[m.Runtime]}, nil
	}

	return Choice{}, fmt.Errorf("no model is available: %s", summarise(probes))
}

// pulled drops models a running local runtime reports it does not have.
//
// A runtime answering is not the same as it having the model. Seen
// 2026-09-24: plain `froe do` resolved to qwen2.5-coder:7b, never pulled, and
// failed on the first request. It matters more since the default became an
// LM Studio model that a fresh install will not have. A runtime that cannot
// list its models (nil) keeps them all, and hosted runtimes are not asked:
// probing should not call a vendor.
func pulled(ctx context.Context, cat *registry.Catalogue, probes map[string]probe.Report) []registry.Model {
	present := map[string]map[string]bool{}
	for name, rep := range probes {
		if rt := cat.Runtimes[name]; rep.OK && rt.APIKeyEnv == "" {
			present[name] = probe.ModelsPresent(ctx, rt)
		}
	}
	return keepPresent(cat.Models, present)
}

// keepPresent is pulled without the network, for tests.
func keepPresent(models []registry.Model, present map[string]map[string]bool) []registry.Model {
	out := make([]registry.Model, 0, len(models))
	for _, m := range models {
		if have := present[m.Runtime]; have != nil && !have[m.ServeID()] {
			continue
		}
		out = append(out, m)
	}
	return out
}

// ChooseByRole picks the model to use for the first role in pref that has an
// available candidate. available reports whether a runtime can serve right now.
//
// Smallest first: on constrained hardware the lighter model is the better
// default, and an explicit --model always overrides.
//
// The sort is stable, so equal-sized models keep the order they arrive in -
// which is ID order, because registry.Load sorts the catalogue by ID.
//
// Do not lean on that to express a preference. The two 0.99 GB fast models are
// a worked example: "qwen2.5-coder-cpu:1.5b" sorts before "qwen2.5-coder:1.5b"
// only because '-' precedes ':' in ASCII, so the CPU-pinned model wins a tie it
// should lose - measured 2026-09-15 on one diff with both models cold, 71.8s
// against 7.5s. The fix is a dedicated role naming the intended model, not an
// ordering trick.
func ChooseByRole(models []registry.Model, pref []string, available func(runtime string) bool) (registry.Model, bool) {
	for _, role := range pref {
		var candidates []registry.Model
		for _, m := range models {
			if m.HasRole(role) && available(m.Runtime) {
				candidates = append(candidates, m)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].SizeGB < candidates[j].SizeGB
		})
		return candidates[0], true
	}
	return registry.Model{}, false
}

// summarise explains why nothing could be picked, naming what would fix it.
func summarise(probes map[string]probe.Report) string {
	var down []string
	for name, r := range probes {
		if !r.OK {
			down = append(down, fmt.Sprintf("%s (%s)", name, r.State))
		}
	}
	sort.Strings(down)
	if len(down) == 0 {
		return "no runtimes are configured"
	}
	return "all runtimes are down: " + strings.Join(down, ", ")
}
