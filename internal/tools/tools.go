// Package tools implements the actions an agent can take.
//
// Every tool declares whether it mutates state; the agent gates those through
// internal/perms rather than trusting the model. That gating matters more with
// a local 7B than with a frontier model, not less — a small model misuses tools
// more often, and the blast radius is the same either way.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dcoldeira/froe/internal/provider"
)

// MemoryWriter records durable facts about a project. An interface rather than
// a concrete store so tools stay testable without a database.
type MemoryWriter interface {
	Remember(project, text, source string) error
}

// Env is the execution context handed to every tool.
type Env struct {
	// Root confines all filesystem access. Nothing outside it is reachable,
	// regardless of what the model asks for.
	Root string
	// Project identifies the project for memory purposes. Defaults to Root.
	Project string
	// Memory is nil when persistence is disabled, in which case the remember
	// tool reports that plainly rather than silently discarding the fact.
	Memory MemoryWriter
}

// ProjectKey returns the key memories are stored under.
func (e Env) ProjectKey() string {
	if e.Project != "" {
		return e.Project
	}
	return e.Root
}

// Tool is one action.
type Tool interface {
	Name() string
	Description() string
	// Schema is the JSON Schema for the tool's arguments. It is sent to the
	// model as-is and, for grammar-constrained backends, compiled into a GBNF.
	Schema() json.RawMessage
	// Mutating reports whether running this changes state. Read-only tools run
	// without prompting; mutating ones are gated.
	Mutating() bool
	Run(ctx context.Context, args json.RawMessage, env Env) (string, error)
}

// Registry holds the available tools.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry assembles the default tool set.
func NewRegistry() *Registry {
	r := &Registry{tools: map[string]Tool{}}
	for _, t := range []Tool{
		Read{}, Glob{}, Grep{}, Write{}, Edit{}, Bash{},
		GitLog{}, GitBlame{}, Remember{}, WebSearch{},
	} {
		r.tools[t.Name()] = t
	}
	return r
}

// NewReadOnlyRegistry returns only the tools that cannot change anything.
//
// Deny by default: it filters on Mutating() rather than listing the safe tools,
// so a tool added later is excluded until it declares itself non-mutating. That
// also keeps web_search out — it is marked mutating precisely because it sends
// repository content to a third party, which a local-first tool must not do
// without being asked.
//
// This is what `froe locate` runs on. The guarantee is structural, not a
// flag: there is no edit_file in the registry, so no prompt can talk it into
// changing a file.
func NewReadOnlyRegistry() *Registry {
	r := &Registry{tools: map[string]Tool{}}
	for name, t := range NewRegistry().tools {
		if t.Mutating() {
			continue
		}
		r.tools[name] = t
	}
	return r
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Names returns tool names in stable order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Defs renders the tool set for a provider, in stable order so prompt caching
// is not defeated by map iteration order.
func (r *Registry) Defs() []provider.ToolDef {
	defs := make([]provider.ToolDef, 0, len(r.tools))
	for _, n := range r.Names() {
		t := r.tools[n]
		defs = append(defs, provider.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
	return defs
}

// ErrOutsideRoot is returned when a path escapes the project root.
var ErrOutsideRoot = fmt.Errorf("path is outside the project root")

// resolve turns a model-supplied path into an absolute path inside Root, or
// fails. This is the hard boundary from docs/ARCHITECTURE.md §6: no config
// overrides it, because the model is not trusted to stay inside the project.
//
// Symlinks are resolved before the check so a link pointing outside cannot be
// used as an escape hatch.
// ResolvePath is resolve for callers outside this package: the agent's
// leftover check reads the files a run edited, and must hold to the same root.
func ResolvePath(env Env, path string) (string, error) { return resolve(env, path) }

func resolve(env Env, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}

	root, err := filepath.Abs(env.Root)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("project root %q: %w", env.Root, err)
	}

	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target = filepath.Clean(target)

	// Resolve symlinks on the deepest existing ancestor: the target itself may
	// not exist yet (a file being created), but its parent chain must not lead
	// outside the root.
	probe := target
	var suffix []string
	for {
		if real, err := filepath.EvalSymlinks(probe); err == nil {
			probe = real
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fmt.Errorf("cannot resolve %q", path)
		}
		suffix = append([]string{filepath.Base(probe)}, suffix...)
		probe = parent
	}
	resolved := filepath.Join(append([]string{probe}, suffix...)...)

	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrOutsideRoot, path)
	}
	return resolved, nil
}

// rel renders a path relative to the root for display.
func rel(env Env, abs string) string {
	if root, err := filepath.Abs(env.Root); err == nil {
		if r, err := filepath.Rel(root, abs); err == nil {
			return r
		}
	}
	return abs
}

// decode unmarshals tool arguments, turning the common failure — a small model
// emitting malformed JSON — into a message the agent can feed back.
func decode(args json.RawMessage, v any) error {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// Search tools report "nothing found" by QUOTING what was searched for, which
// the model needs - but it means four reworded searches that all find nothing
// produce four different strings. A loop detector keyed on result text sees
// four distinct outcomes and never fires.
//
// Observed on the third issue-657 run: turns 12-23 rewording globs
// (services/invoices/*, **/invoices_pdf*, **/invoices*report*, ...) for a
// path named in the issue that does not exist, two turns after the real
// file had been found and edited. Nothing caught it; the run burned its
// whole budget.
//
// The prefixes live here, beside the predicate, so a new wording cannot be
// added in one place and forgotten in the other.
const (
	noFilesMatchPrefix = "(no files match "
	noMatchesForPrefix = "(no matches for "
)

// IsNoMatch reports whether a tool result means "nothing was found", whatever
// arguments produced it.
func IsNoMatch(result string) bool {
	return strings.HasPrefix(result, noFilesMatchPrefix) ||
		strings.HasPrefix(result, noMatchesForPrefix)
}
