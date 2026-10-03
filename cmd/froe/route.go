package main

import (
	"context"
	"fmt"

	"github.com/dcoldeira/froe/internal/registry"
	"github.com/dcoldeira/froe/internal/resolve"
)

// settleModel settles the model for one task: the routed one when routing is
// on, current otherwise, loaded either way. say reports each step as it
// happens - the route before a slow load, then the load.
//
// Shared by `froe rpc` (Neovim) and `froe chat` so the two front ends cannot
// route the same task differently (docs/ARCHITECTURE.md §8).
//
// A model that fails to load ends the run with the load error rather than
// quietly using another - a surprise model is worse than a clear failure
// (resolve.Pick). The class is returned even when a model is pinned: a task
// that needs checking needs it whichever model answers (agent.RequireEvidence).
func settleModel(ctx context.Context, cat *registry.Catalogue, current resolve.Choice, routing bool,
	task string, hasSelection bool, say func(string)) (string, resolve.Choice, error) {

	choice := current
	class, why := resolve.Route(task, hasSelection)
	if routing {
		c, err := resolve.PickWithPreference(ctx, cat, "", resolve.RoutePreference[class])
		if err != nil {
			return "", current, err
		}
		choice = c
		say(fmt.Sprintf("%s → %s (%s)", class, c.Model.ID, why))
	}

	loaded, err := resolve.EnsureLoaded(ctx, choice)
	if err != nil {
		return "", current, err
	}
	if loaded {
		say("loaded " + choice.Model.ID)
	}
	return class, choice, nil
}
