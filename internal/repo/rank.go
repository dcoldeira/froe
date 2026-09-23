package repo

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Scored is a file with its relevance to a task.
type Scored struct {
	FileEntry
	Score int
	Why   string
}

var wordRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{2,}`)

// stopWords are too common in task descriptions to discriminate between files.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true,
	"from": true, "into": true, "then": true, "when": true, "what": true, "why": true,
	"how": true, "add": true, "fix": true, "use": true, "make": true, "get": true,
	"set": true, "run": true, "not": true, "are": true, "was": true, "can": true,
	"file": true, "files": true, "code": true, "please": true, "should": true,
	"find": true, "there": true, "have": true, "does": true, "you": true,
}

// Keywords extracts discriminating terms from a task description.
func Keywords(task string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range wordRe.FindAllString(task, -1) {
		lw := strings.ToLower(w)
		if stopWords[lw] || seen[lw] {
			continue
		}
		seen[lw] = true
		out = append(out, lw)
		if len(out) >= 12 {
			break
		}
	}
	return out
}

// Rank scores files against a task.
//
// Scoring is deliberately crude and explainable. Embeddings were considered and
// rejected (docs/ARCHITECTURE.md §9): they need a model, an index and a store,
// and on a repository of a few thousand files a symbol-name match is a stronger
// signal than cosine similarity — a file DEFINING the thing you named is almost
// always the file you want.
func Rank(ctx context.Context, m *Map, task string) []Scored {
	kws := Keywords(task)
	scored := make([]Scored, 0, len(m.Files))

	counts := contentMatches(ctx, m.Root, kws)

	for _, f := range m.Files {
		s := Scored{FileEntry: f}
		lowerPath := strings.ToLower(f.Path)
		base := strings.ToLower(filepath.Base(f.Path))
		var reasons []string

		for _, kw := range kws {
			// Defining a symbol by that name is the strongest signal.
			for _, sym := range f.Symbols {
				if strings.Contains(strings.ToLower(sym.Name), kw) {
					s.Score += 10
					reasons = append(reasons, "defines "+sym.Name)
					break
				}
			}
			// The filename itself is nearly as strong.
			if strings.Contains(base, kw) {
				s.Score += 8
				reasons = append(reasons, "filename")
			} else if strings.Contains(lowerPath, kw) {
				s.Score += 4
				reasons = append(reasons, "path")
			}
		}

		// Content matches are weak on their own — a common word appears
		// everywhere — so they are capped and only break ties.
		if n := counts[f.Path]; n > 0 {
			bonus := n
			if bonus > 5 {
				bonus = 5
			}
			s.Score += bonus
			reasons = append(reasons, fmt.Sprintf("%d matches", n))
		}

		if len(reasons) > 2 {
			reasons = reasons[:2]
		}
		s.Why = strings.Join(reasons, ", ")
		scored = append(scored, s)
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Path < scored[j].Path
	})
	return scored
}

// contentMatches counts keyword hits per file using a single ripgrep pass.
// One process for all keywords, not one per keyword.
func contentMatches(ctx context.Context, root string, kws []string) map[string]int {
	out := map[string]int{}
	if len(kws) == 0 {
		return out
	}
	rg, err := exec.LookPath("rg")
	if err != nil {
		return out
	}

	argv := []string{"--count-matches", "--no-heading", "--color", "never",
		"--ignore-case", "--max-count", "20"}
	for _, kw := range kws {
		argv = append(argv, "-e", regexp.QuoteMeta(kw))
	}
	argv = append(argv, root)

	res, err := exec.CommandContext(ctx, rg, argv...).Output()
	if err != nil {
		return out // exit 1 means no matches, which is not an error here
	}
	for _, line := range strings.Split(strings.TrimRight(string(res), "\n"), "\n") {
		i := strings.LastIndex(line, ":")
		if i < 0 {
			continue
		}
		n, err := strconv.Atoi(line[i+1:])
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(root, line[:i]); err == nil {
			out[rel] += n
		}
	}
	return out
}

// RenderRanked writes the most relevant files in full, then a directory
// summary so the model still knows the shape of what it cannot see.
//
// On a 469-file project a flat alphabetical map truncated to budget shows the
// first 30 files and hides everything else, which is worse than useless — it
// looks complete. Ranking plus a directory tail keeps the map honest.
func RenderRanked(m *Map, scored []Scored, budget int) string {
	var b strings.Builder
	b.WriteString("Project structure, most relevant first (symbols only - use read_file for detail):\n\n")
	used := EstimateTokens(b.String())

	// Reserve a slice of the budget so the directory summary always fits.
	summaryBudget := budget / 5
	if summaryBudget > 800 {
		summaryBudget = 800
	}

	shown := map[string]bool{}
	for _, f := range scored {
		if f.Score <= 0 {
			break
		}
		entry := renderFile(f.FileEntry, rankedSymbolCap)
		if f.Why != "" {
			entry = strings.Replace(entry, "\n", "  ["+f.Why+"]\n", 1)
		}
		cost := EstimateTokens(entry)
		if used+cost > budget-summaryBudget {
			break
		}
		b.WriteString(entry)
		used += cost
		shown[f.Path] = true
	}

	if len(shown) == 0 {
		// Nothing scored: fall back to the flat map rather than an empty one.
		return m.Render(budget)
	}

	var rest []FileEntry
	for _, f := range m.Files {
		if !shown[f.Path] {
			rest = append(rest, f)
		}
	}
	if len(rest) > 0 {
		fmt.Fprintf(&b, "\nOther directories (%d more files, not expanded):\n", len(rest))
		b.WriteString(dirSummary(rest, summaryBudget))
	}
	return b.String()
}

// dirSummary lists directories with file counts, so the model can glob into
// them rather than assuming they do not exist.
func dirSummary(files []FileEntry, budget int) string {
	counts := map[string]int{}
	for _, f := range files {
		d := filepath.Dir(f.Path)
		if d == "." {
			d = "(root)"
		}
		counts[d]++
	}
	dirs := make([]string, 0, len(counts))
	for d := range counts {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool {
		if counts[dirs[i]] != counts[dirs[j]] {
			return counts[dirs[i]] > counts[dirs[j]]
		}
		return dirs[i] < dirs[j]
	})

	var b strings.Builder
	used, listed := 0, 0
	for _, d := range dirs {
		line := fmt.Sprintf("  %s/ (%d)\n", d, counts[d])
		c := EstimateTokens(line)
		if used+c > budget {
			fmt.Fprintf(&b, "  … %d more directories\n", len(dirs)-listed)
			break
		}
		b.WriteString(line)
		used += c
		listed++
	}
	return b.String()
}
