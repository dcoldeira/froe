package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// maxGitLines bounds git output so history cannot flood the context window.
const maxGitLines = 80

// GitLog shows recent commits, optionally for one path.
//
// History is context a coding agent otherwise lacks entirely: why a line looks
// the way it does, what changed recently, whether a fix has been attempted
// before. Reading the working tree alone cannot answer any of that.
type GitLog struct{}

func (GitLog) Name() string   { return "git_log" }
func (GitLog) Mutating() bool { return false }
func (GitLog) Description() string {
	return "Show recent git commits, optionally limited to one file or directory. " +
		"Use it to see what changed recently and why, before changing it again."
}

func (GitLog) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":  {"type": "string", "description": "Optional file or directory to limit history to"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "How many commits, default 10"},
    "grep":  {"type": "string", "description": "Optional: only commits whose message matches this text"}
  }
}`)
}

func (GitLog) Run(ctx context.Context, args json.RawMessage, env Env) (string, error) {
	var a struct {
		Path  string `json:"path"`
		Limit int    `json:"limit"`
		Grep  string `json:"grep"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Limit <= 0 || a.Limit > 50 {
		a.Limit = 10
	}

	root, err := resolve(env, ".")
	if err != nil {
		return "", err
	}

	argv := []string{"-C", root, "log",
		"--max-count", strconv.Itoa(a.Limit),
		"--date=short",
		"--pretty=format:%h %ad %an: %s"}
	if a.Grep != "" {
		argv = append(argv, "--grep", a.Grep)
	}
	if a.Path != "" {
		abs, err := resolve(env, a.Path)
		if err != nil {
			return "", err
		}
		argv = append(argv, "--", abs)
	}

	out, err := runGit(ctx, argv)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "(no commits found)", nil
	}
	return clampLines(out, maxGitLines), nil
}

// GitBlame shows who last changed each line of a file.
type GitBlame struct{}

func (GitBlame) Name() string   { return "git_blame" }
func (GitBlame) Mutating() bool { return false }
func (GitBlame) Description() string {
	return "Show the commit, author and date that last changed each line of a file. " +
		"Use a line range - blaming a whole file is rarely useful and costs a lot of context."
}

func (GitBlame) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":  {"type": "string", "description": "File to blame"},
    "start": {"type": "integer", "minimum": 1, "description": "First line"},
    "end":   {"type": "integer", "minimum": 1, "description": "Last line"}
  },
  "required": ["path"]
}`)
}

func (GitBlame) Run(ctx context.Context, args json.RawMessage, env Env) (string, error) {
	var a struct {
		Path  string `json:"path"`
		Start int    `json:"start"`
		End   int    `json:"end"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	abs, err := resolve(env, a.Path)
	if err != nil {
		return "", err
	}
	root, err := resolve(env, ".")
	if err != nil {
		return "", err
	}

	argv := []string{"-C", root, "blame", "--date=short", "-w"}
	if a.Start > 0 {
		end := a.End
		if end < a.Start {
			// A start with no end means "from here", not "the whole file".
			end = a.Start + 40
		}
		argv = append(argv, "-L", fmt.Sprintf("%d,%d", a.Start, end))
	}
	argv = append(argv, "--", abs)

	out, err := runGit(ctx, argv)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "(no blame output - is the file tracked?)", nil
	}
	return clampLines(out, maxGitLines), nil
}

// runGit executes git and surfaces its own error text, never a bare exit code.
func runGit(ctx context.Context, argv []string) (string, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git is not installed")
	}
	cmd := exec.CommandContext(ctx, git, argv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git: %s", msg)
	}
	return string(out), nil
}

func clampLines(s string, max int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= max {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:max], "\n") +
		fmt.Sprintf("\n(%d more lines - narrow the range)", len(lines)-max)
}
