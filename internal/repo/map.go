package repo

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// FileEntry is one file in the map.
type FileEntry struct {
	Path    string
	Bytes   int64
	Lines   int
	Symbols []Symbol
}

// Map is a structural view of a project.
type Map struct {
	Root    string
	Files   []FileEntry
	Skipped int // files present but not summarised (unsupported language)
}

// flatSymbolCap and rankedSymbolCap bound symbols per file when rendering.
// The ranked view is tighter because it is trying to show many relevant files;
// the flat view has no ranking to lean on and shows more of each.
const (
	flatSymbolCap   = 25
	rankedSymbolCap = 8
)

// maxFilesScanned bounds the walk. Past this the map is no longer something a
// model can hold in context anyway, and building it costs real time.
const maxFilesScanned = 4000

// maxFileBytes skips files too large to summarise usefully. A 5 MB generated
// file contributes noise proportional to its size and insight close to zero.
const maxFileBytes = 512 * 1024

var skipDirs = map[string]bool{
	".git": true, ".claude": true, "node_modules": true, "vendor": true, "target": true,
	"dist": true, "build": true, ".venv": true, "venv": true,
	"__pycache__": true, ".dart_tool": true, ".idea": true, ".next": true,
}

// Build constructs a repo map rooted at dir.
func Build(ctx context.Context, dir string) (*Map, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	paths, err := ListFiles(ctx, root)
	if err != nil {
		return nil, err
	}

	m := &Map{Root: root}
	for _, rel := range paths {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		abs := filepath.Join(root, rel)
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			continue
		}
		if !Supported(rel) {
			m.Skipped++
			continue
		}
		if info.Size() > maxFileBytes {
			m.Skipped++
			continue
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		content := string(b)
		m.Files = append(m.Files, FileEntry{
			Path:    rel,
			Bytes:   info.Size(),
			Lines:   strings.Count(content, "\n") + 1,
			Symbols: ExtractSymbols(rel, content),
		})
	}

	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return m, nil
}

// ListFiles returns the project's files, relative to root.
//
// It prefers `git ls-files`, which respects .gitignore EXACTLY and is far
// faster than walking a large tree. That matters beyond speed: a real production repo's backend
// keeps six agent worktrees under .claude/, which git ignores and a naive walk
// does not — 1102 duplicate Python files that would otherwise flood both the
// map and any glob result. Falling back to a manual walk keeps non-git
// directories working.
func ListFiles(ctx context.Context, root string) ([]string, error) {
	if git, err := exec.LookPath("git"); err == nil {
		cmd := exec.CommandContext(ctx, git, "-C", root, "ls-files", "--cached", "--others", "--exclude-standard")
		if out, err := cmd.Output(); err == nil {
			lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
			if len(lines) > maxFilesScanned {
				lines = lines[:maxFilesScanned]
			}
			var paths []string
			for _, l := range lines {
				if l != "" && !inSkippedDir(l) {
					paths = append(paths, l)
				}
			}
			return paths, nil
		}
	}
	return walkFiles(ctx, root)
}

func inSkippedDir(rel string) bool {
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if skipDirs[part] {
			return true
		}
	}
	return false
}

func walkFiles(ctx context.Context, root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if len(paths) >= maxFilesScanned {
			return filepath.SkipAll
		}
		if rel, err := filepath.Rel(root, p); err == nil {
			paths = append(paths, rel)
		}
		return nil
	})
	return paths, err
}

// Render writes the map as text for a model, spending at most budget tokens.
//
// Files are dropped whole rather than truncated mid-list, and the drop is
// REPORTED: a silently truncated map is worse than a short one, because the
// model cannot tell the difference between "this file does not exist" and
// "this file did not fit" (D4).
func (m *Map) Render(budget int) string {
	var b strings.Builder
	b.WriteString("Project structure (symbols only - use read_file for detail):\n\n")

	used := EstimateTokens(b.String())
	var shown int

	for _, f := range m.Files {
		entry := renderFile(f, flatSymbolCap)
		cost := EstimateTokens(entry)
		if used+cost > budget {
			break
		}
		b.WriteString(entry)
		used += cost
		shown++
	}

	if dropped := len(m.Files) - shown; dropped > 0 {
		fmt.Fprintf(&b, "\n(%d more files not listed - the map hit its context budget. "+
			"Use glob or grep to find them.)\n", dropped)
	}
	if m.Skipped > 0 {
		fmt.Fprintf(&b, "(%d files skipped: unsupported type or too large)\n", m.Skipped)
	}
	return b.String()
}

// renderFile renders one entry, showing at most maxSyms symbols.
//
// A map is for choosing WHICH file to open, not for reading its contents.
// One 2774-line module with 40 methods otherwise consumes the entire budget and
// hides the other 468 files — breadth is worth more here than depth, because a
// file the model cannot see is a file it will guess the name of.
func renderFile(f FileEntry, maxSyms int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d lines)\n", f.Path, f.Lines)

	syms := f.Symbols
	if maxSyms > 0 && len(syms) > maxSyms {
		fmt.Fprintf(&b, "  %s %s\n", syms[0].Kind, syms[0].Name)
		for _, s := range syms[1:maxSyms] {
			fmt.Fprintf(&b, "  %s %s\n", s.Kind, s.Name)
		}
		fmt.Fprintf(&b, "  … +%d more\n", len(syms)-maxSyms)
		return b.String()
	}
	for _, s := range syms {
		fmt.Fprintf(&b, "  %s %s\n", s.Kind, s.Name)
	}
	return b.String()
}

// Stats summarises the map for humans.
func (m *Map) Stats() (files, symbols int) {
	for _, f := range m.Files {
		symbols += len(f.Symbols)
	}
	return len(m.Files), symbols
}
