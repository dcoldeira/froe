package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dcoldeira/froe/internal/repo"
)

// maxGlobResults bounds a listing so a match on "**" cannot flood the context.
const maxGlobResults = 300

// Glob lists files matching a pattern.
type Glob struct{}

func (Glob) Name() string   { return "glob" }
func (Glob) Mutating() bool { return false }
func (Glob) Description() string {
	return "List project files matching a glob pattern, e.g. '**/*.go' or 'internal/**'. " +
		"Use this to discover files before reading them."
}

func (Glob) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Glob pattern, e.g. **/*.go"}
  },
  "required": ["pattern"]
}`)
}

func (Glob) Run(ctx context.Context, args json.RawMessage, env Env) (string, error) {
	var a struct {
		Pattern string `json:"pattern"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Pattern == "" {
		a.Pattern = "**/*"
	}

	root, err := resolve(env, ".")
	if err != nil {
		return "", err
	}

	// Shared with the repo map so both respect .gitignore identically. A glob
	// that reports ignored build output or agent worktrees is worse than
	// useless: it sends the model to read files that are not the project.
	paths, err := repo.ListFiles(ctx, root)
	if err != nil {
		return "", err
	}

	var matches []string
	for _, r := range paths {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if matchGlob(a.Pattern, r) {
			matches = append(matches, r)
		}
	}

	sort.Strings(matches)
	if len(matches) == 0 {
		return fmt.Sprintf(noFilesMatchPrefix+"%q)", a.Pattern), nil
	}
	out := matches
	var note string
	if len(matches) > maxGlobResults {
		out = matches[:maxGlobResults]
		note = fmt.Sprintf("\n(%d more not shown - narrow the pattern)", len(matches)-maxGlobResults)
	}
	return strings.Join(out, "\n") + note, nil
}

// matchGlob supports ** in addition to filepath.Match's single-segment globs.
func matchGlob(pattern, name string) bool {
	if strings.Contains(pattern, "**") {
		// "**/" may match zero directories, so try with the prefix removed too.
		if trimmed := strings.Replace(pattern, "**/", "", 1); trimmed != pattern {
			if matchGlob(trimmed, name) {
				return true
			}
		}
		parts := strings.SplitN(pattern, "**", 2)
		prefix, suffix := parts[0], strings.TrimPrefix(parts[1], "/")
		if !strings.HasPrefix(name, prefix) {
			return false
		}
		rest := strings.TrimPrefix(name, prefix)
		if suffix == "" {
			return true
		}
		for {
			// Recurse, not filepath.Match: the suffix may hold another **,
			// which Match would collapse into a single-segment *.
			if matchGlob(suffix, rest) {
				return true
			}
			i := strings.Index(rest, "/")
			if i < 0 {
				return false
			}
			rest = rest[i+1:]
		}
	}
	ok, _ := filepath.Match(pattern, name)
	if ok {
		return true
	}
	// A bare pattern with no separator should also match by basename.
	if !strings.Contains(pattern, "/") {
		ok, _ = filepath.Match(pattern, filepath.Base(name))
		return ok
	}
	return false
}
