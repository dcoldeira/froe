package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// A citation can name the right line and still carry the wrong number.
//
// Measured 2026-09-24, bonsai-27b on 09-locate-wrong-path, three runs: two
// answers named the widths tuple as the second place to change - the half of
// the edit that gets forgotten - and quoted it correctly, but cited it as :8
// and :7 when it is on line 10. Both had read the whole 17-line file. The
// count skipped its blank lines. The grader, rightly, failed them: a user
// sent to line 8 finds a closing bracket.
//
// The answer usually carries the evidence to fix this itself, because a model
// quotes the code beside the number. relines checks each quote against the
// line it was cited on and, when the quote is not there but is on exactly one
// other line of that file, reports the line it is really on. One match or
// nothing: a quote that fits several lines is not evidence of which one was
// meant, and guessing would be the error this exists to catch.

// relined is a citation whose quoted code is on a different line.
type relined struct {
	Path   string
	Said   int
	Actual int
	Quote  string
}

// quoted pulls the code-like pieces out of what follows a citation: text in
// quotes or backticks, and identifiers that cannot be prose (they contain an
// underscore, or a capital after the first letter).
var (
	inQuotes   = regexp.MustCompile("\"([^\"]{3,})\"|`([^`]{3,})`|'([^']{3,})'")
	identifier = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{3,}`)
)

// quoteCandidates returns what a citation's trailing text claims is on the
// line, longest first - the longest match is the most specific.
func quoteCandidates(rest string) []string {
	rest = strings.TrimSpace(strings.TrimLeft(rest, ": \t"))
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.Join(strings.Fields(s), " ")
		if len(s) >= 3 && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	// The whole remainder, if it reads as code rather than a description.
	if strings.ContainsAny(rest, "=(){}[],") {
		add(rest)
	}
	for _, m := range inQuotes.FindAllStringSubmatch(rest, -1) {
		for _, g := range m[1:] {
			add(g)
		}
	}
	for _, id := range identifier.FindAllString(rest, -1) {
		if strings.Contains(id, "_") || strings.ToLower(id[1:]) != id[1:] {
			add(id)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// relines checks every WHERE citation's quote against its cited line.
func relines(root, answer string) []relined {
	body := whereSection(answer)
	if strings.TrimSpace(body) == "" {
		body = answer
	}

	files := map[string][]string{}
	lines := func(path string) []string {
		if ls, ok := files[path]; ok {
			return ls
		}
		var ls []string
		if abs, ok := resolveSite(root, path); ok {
			if b, err := os.ReadFile(abs); err == nil {
				for _, l := range strings.Split(string(b), "\n") {
					ls = append(ls, strings.Join(strings.Fields(l), " "))
				}
			}
		}
		files[path] = ls
		return ls
	}

	var out []relined
	for _, line := range strings.Split(body, "\n") {
		locs := siteLine.FindAllStringSubmatchIndex(line, -1)
		for k, loc := range locs {
			path := strings.TrimPrefix(strings.Trim(line[loc[2]:loc[3]], "`\"',"), "./")
			if !strings.ContainsAny(path, "./") {
				continue
			}
			var said int
			fmt.Sscanf(line[loc[4]:loc[5]], "%d", &said)
			// The quote is whatever follows this citation, up to the next one.
			end := len(line)
			if k+1 < len(locs) {
				end = locs[k+1][0]
			}
			ls := lines(path)
			if said < 1 || said > len(ls) {
				continue
			}
			if r, ok := reline(path, said, ls, quoteCandidates(line[loc[1]:end])); ok {
				out = append(out, r)
			}
		}
	}
	return out
}

// reline decides one citation. ls holds the file's lines, whitespace-collapsed.
func reline(path string, said int, ls []string, cands []string) (relined, bool) {
	if len(cands) == 0 {
		return relined{}, false
	}
	for _, c := range cands {
		if strings.Contains(ls[said-1], c) {
			return relined{}, false // the cited line holds what was quoted
		}
	}
	for _, c := range cands {
		var hits, defs []int
		for i, l := range ls {
			if strings.Contains(l, c) {
				hits = append(hits, i+1)
				if definesName(l, c) {
					defs = append(defs, i+1)
				}
			}
		}
		// A name cited on its own means where it is defined. Measured
		// 2026-09-24: "Index 4 in WITNESS_SUMMARY_WIDTHS", cited two lines
		// out, matched both the tuple and the assert that reads it - and
		// only one of those is a place to edit.
		switch {
		case len(hits) == 1:
			return relined{Path: path, Said: said, Actual: hits[0], Quote: c}, true
		case len(defs) == 1:
			return relined{Path: path, Said: said, Actual: defs[0], Quote: c}, true
		}
	}
	return relined{}, false
}

// definesName reports whether a line defines name rather than uses it:
// "NAME = ...", "NAME: T = ...", "def NAME(", "class NAME", "func NAME(", and
// the Go/JS declaration keywords.
func definesName(line, name string) bool {
	if identifier.FindString(name) != name {
		return false
	}
	re := regexp.MustCompile(`^\s*((def|class|func|var|const|let|type)\s+)?` +
		regexp.QuoteMeta(name) + `\s*(=[^=]|:|\(|\{|$)`)
	return re.MatchString(line)
}

// applyRelines moves corrected citations to their real line, so the sweep and
// the surroundings downstream work from where the code is, not where the
// answer said it was.
func applyRelines(sites []site, rs []relined) []site {
	to := map[site]int{}
	for _, r := range rs {
		to[site{Path: r.Path, Line: r.Said}] = r.Actual
	}
	out := make([]site, len(sites))
	for i, s := range sites {
		if n, ok := to[s]; ok {
			s.Line = n
		}
		out[i] = s
	}
	return out
}

func renderRelines(rs []relined) string {
	if len(rs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nLINE NUMBERS CORRECTED\n")
	b.WriteString("  froe checked the code quoted beside each citation against the file.\n")
	b.WriteString("  These quotes are on a different line from the one cited:\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "  %s:%d  (cited as :%d)  %s\n", r.Path, r.Actual, r.Said, r.Quote)
	}
	return b.String()
}
