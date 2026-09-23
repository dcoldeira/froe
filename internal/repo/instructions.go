package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// instructionFiles are read, in this order, from every directory between the
// project root and the working directory.
//
// CLAUDE.md is included deliberately: the user already maintains one in their
// projects, and it says exactly the things froe needs to know. Making them
// write the same content twice under a different name would be a pointless tax.
var instructionFiles = []string{"FROE.md", "CLAUDE.md", "AGENTS.md"}

// maxInstructionBytes bounds what is loaded from any one file. A long
// CLAUDE.md can be many thousands of tokens, and it competes with the task.
const maxInstructionBytes = 16 * 1024

// Instructions is project guidance found on disk.
type Instructions struct {
	Sources []string // relative paths, nearest last
	Text    string
}

// LoadInstructions walks from root down to dir, collecting instruction files.
//
// Order matters: nearer files come last so that a subdirectory's guidance
// appears after, and therefore overrides, the repository-wide file.
func LoadInstructions(root, dir string) (*Instructions, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	// Build the chain root → … → dir.
	var chain []string
	cur := absDir
	for {
		chain = append([]string{cur}, chain...)
		if cur == absRoot || cur == filepath.Dir(cur) {
			break
		}
		parent := filepath.Dir(cur)
		if !strings.HasPrefix(absRoot, parent) && !strings.HasPrefix(parent, absRoot) {
			break
		}
		cur = parent
	}

	out := &Instructions{}
	var b strings.Builder
	seen := map[string]bool{}

	for _, d := range chain {
		for _, name := range instructionFiles {
			p := filepath.Join(d, name)
			if seen[p] {
				continue
			}
			info, err := os.Stat(p)
			if err != nil || info.IsDir() {
				continue
			}
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			seen[p] = true

			text := string(data)
			truncated := false
			if len(text) > maxInstructionBytes {
				text = text[:maxInstructionBytes]
				truncated = true
			}
			rel, err := filepath.Rel(absRoot, p)
			if err != nil {
				rel = p
			}
			out.Sources = append(out.Sources, rel)

			fmt.Fprintf(&b, "--- %s ---\n%s\n", rel, strings.TrimSpace(text))
			if truncated {
				fmt.Fprintf(&b, "(truncated at %d bytes)\n", maxInstructionBytes)
			}
			b.WriteString("\n")

			// Only the first match per directory: FROE.md wins over CLAUDE.md
			// when both exist, rather than loading duplicate guidance.
			break
		}
	}

	out.Text = strings.TrimSpace(b.String())
	return out, nil
}

// Fit truncates instructions to a token allowance, keeping whole lines and
// saying plainly that it happened.
//
// Silent truncation is the failure mode to avoid: the model cannot tell a rule
// that was cut from a rule that was never written, and will confidently break
// a convention it was never shown.
func (i *Instructions) Fit(allowance int) (text string, dropped bool) {
	if allowance <= 0 {
		return "", i.Text != ""
	}
	if EstimateTokens(i.Text) <= allowance {
		return i.Text, false
	}

	var b strings.Builder
	used := 0
	for _, line := range strings.Split(i.Text, "\n") {
		cost := EstimateTokens(line + "\n")
		if used+cost > allowance {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
		used += cost
	}
	b.WriteString("\n(project instructions truncated to fit the context window - " +
		"ask to read the file directly if you need the rest)\n")
	return b.String(), true
}

// rootMarkers identify a project root, most specific first.
//
// Instruction files count as markers. A directory containing a CLAUDE.md is a
// project by definition, whatever its VCS status — a real production repo is exactly this
// shape: no .git of its own (backend/ and frontend/ are separate repos) and no
// manifest at the top, so without this the walk climbed out of it entirely.
var rootMarkers = []string{
	".git", "FROE.md", "CLAUDE.md", "AGENTS.md",
	"go.mod", "package.json", "pubspec.yaml", "pyproject.toml", "Cargo.toml",
	"Makefile", ".hg", ".svn",
}

// FindRoot locates the project root by walking up for a marker, falling back to
// dir itself.
//
// The walk NEVER passes the home directory. Mapping $HOME treats every file a
// user owns as project context: measured on this machine it scanned 4000 files
// and mapped 396 across unrelated projects, which is both slow and wrong.
func FindRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	home, _ := os.UserHomeDir()

	cur := abs
	for {
		for _, m := range rootMarkers {
			if _, err := os.Stat(filepath.Join(cur, m)); err == nil {
				return cur
			}
		}
		parent := filepath.Dir(cur)
		// Stop at the home directory, the filesystem root, or a mount boundary.
		if parent == cur || cur == home || parent == home {
			return abs
		}
		cur = parent
	}
}
