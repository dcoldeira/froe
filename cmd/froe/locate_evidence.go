package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dcoldeira/froe/internal/repo"
	"github.com/dcoldeira/froe/internal/tools"
)

// This file searches the project BEFORE the model takes its first turn.
//
// Measured 2026-09-16 on 10-locate-real-shape: every run opened by reading the
// path the report gave, found it missing, and then spent two to four of its six
// turns globbing for a filename. One run died there, at turn 3, having already
// been shown the real file in its own project map. That is mechanical work
// costing 45 seconds a turn, and the terms it needs are sitting in the report
// in quotation marks.
//
// So froe runs those searches itself and hands the model the lines. The model
// stops searching and starts judging, which is the part it is actually good at.
// Same principle as D9: the mechanical half belongs in Go.

const (
	// evidenceMaxFiles bounds how many files one term reports. A term that
	// appears everywhere is not a location, and the point of the block is to
	// name a few candidates rather than to reproduce a search.
	evidenceMaxFiles = 4
	// evidenceMaxLinesPerFile keeps a file's entry to a glance.
	evidenceMaxLinesPerFile = 6
	// evidenceLineWidth trims a long source line. Indentation is stripped
	// already; what is left is the code.
	evidenceLineWidth = 100
)

// fileHits is one file's matches for one term.
type fileHits struct {
	Path    string
	Matches []tools.Match
}

// gatherEvidence searches the project for the phrases the report quotes and
// renders what it found, within a token budget.
//
// It returns the searches it ran as well, so the completeness sweep can replay
// them afterwards without deriving them a second time.
func gatherEvidence(ctx context.Context, root, issue string, budget int) (string, []tools.Search) {
	terms := issueSearches(issue)
	if len(terms) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("EVIDENCE - froe searched this project for the terms the report quotes,\n")
	b.WriteString("before you started. These lines came from the project itself, so unlike the\n")
	b.WriteString("path in the report they are known to exist.\n")

	var used []tools.Search
	any := false
	for _, term := range terms {
		hits, searches := searchTerm(ctx, root, term)
		used = append(used, searches...)
		if len(hits) == 0 {
			continue
		}
		section := renderTerm(term.Pattern, hits)
		if repo.EstimateTokens(b.String()+section) > budget {
			break
		}
		b.WriteString(section)
		any = true
	}
	if !any {
		return "", used
	}
	return b.String(), used
}

// searchTerm runs a term literally and as words-only, and merges the result by
// file. Both are run because the relaxed form is BOUNDED - it cannot match a
// separator longer than a few characters - so neither is a superset of the
// other.
func searchTerm(ctx context.Context, root string, term tools.Search) ([]fileHits, []tools.Search) {
	searches := []tools.Search{term}
	if relaxed, ok := tools.RelaxSeparators(term); ok {
		searches = append(searches, relaxed)
	}

	byFile := map[string][]tools.Match{}
	seen := map[string]bool{}
	for _, s := range searches {
		ms, err := tools.GrepTree(ctx, root, s)
		if err != nil {
			continue
		}
		for _, m := range ms {
			key := fmt.Sprintf("%s:%d", m.Path, m.Line)
			if seen[key] {
				continue
			}
			seen[key] = true
			byFile[m.Path] = append(byFile[m.Path], m)
		}
	}

	hits := make([]fileHits, 0, len(byFile))
	for path, ms := range byFile {
		sort.Slice(ms, func(i, j int) bool { return ms[i].Line < ms[j].Line })
		hits = append(hits, fileHits{Path: path, Matches: ms})
	}
	// Most matches first: the file that mentions a thing six times is where it
	// lives, and the one that mentions it once is usually a caller. Ties break
	// on path so the block is stable between runs.
	sort.Slice(hits, func(i, j int) bool {
		if len(hits[i].Matches) != len(hits[j].Matches) {
			return len(hits[i].Matches) > len(hits[j].Matches)
		}
		return hits[i].Path < hits[j].Path
	})
	return hits, searches
}

func renderTerm(term string, hits []fileHits) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%q - %s\n", term, plural(len(hits), "file", "files"))

	shown := hits
	if len(shown) > evidenceMaxFiles {
		shown = shown[:evidenceMaxFiles]
	}
	for _, h := range shown {
		fmt.Fprintf(&b, "  %s  (%s)\n", h.Path, plural(len(h.Matches), "match", "matches"))
		ms := h.Matches
		if len(ms) > evidenceMaxLinesPerFile {
			ms = ms[:evidenceMaxLinesPerFile]
		}
		for _, m := range ms {
			fmt.Fprintf(&b, "    %d: %s\n", m.Line, truncate(m.Text, evidenceLineWidth))
		}
		if len(h.Matches) > len(ms) {
			fmt.Fprintf(&b, "    ... %d more in this file\n", len(h.Matches)-len(ms))
		}
	}
	if len(hits) > len(shown) {
		fmt.Fprintf(&b, "  ... %d more files\n", len(hits)-len(shown))
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// evidenceShare is the fraction of the context window the evidence block may
// take. The project map already costs about a fifth at 8192, and what is left
// has to hold the system prompt, the report and six turns of tool results.
const evidenceShare = 8
