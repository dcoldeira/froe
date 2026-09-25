package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dcoldeira/froe/internal/repo"
	"github.com/dcoldeira/froe/internal/tools"
)

// This file is everything froe does with locate's ANSWER once the model has
// stopped: unwrapping it, reading the places out of it, checking those places
// exist, and sweeping the files it found for the ones it stopped short of.
//
// It is deterministic Go rather than more prompting, on purpose. Measured over
// three runs of a real production issue: the model found the right file every time
// and never once found all the sites in it, because having identified the file
// it never searched INSIDE it. That is the 8192-token window, not the wording
// of the prompt - so the sweep happens here, replaying the model's own
// successful searches, at a cost of no turns and no tokens.

// site is one "<path>:<line>" entry from the WHERE section.
type site struct {
	Path string
	Line int
}

// missed is a line the sweep found and the answer did not mention.
type missed struct {
	Path string
	Line int
	Text string
}

const (
	// sweepNoiseLimit drops a pattern that matches too much of one file to be
	// pointing at specific places. A term that appears forty times is the
	// file's subject, not a site list.
	sweepNoiseLimit = 25
	// sweepMaxPerFile and sweepMaxTotal keep the addendum readable. It exists
	// to catch the two or three sites the model stopped short of, not to dump
	// a search.
	sweepMaxPerFile = 8
	sweepMaxTotal   = 20
)

// fenceStripper drops markdown code fences from the answer as it streams.
//
// Measured 2026-09-15: a run that answered correctly wrapped the whole thing in
// a ```python fence, so the first line the user read was "```python" and the
// last was "```". Buffering the answer to unwrap it afterwards would cost the
// streaming - on a model generating ~8 tokens a second, watching the answer
// appear IS the progress bar - so the filter is a line-at-a-time stream.
//
// The rule is deliberately blunt: any line that is a fence marker is dropped,
// wherever it appears. locate's format has three prose sections and no code
// blocks, so a fence is never content here; dropping the marker keeps whatever
// was inside it and loses only the decoration.
type fenceStripper struct {
	w io.Writer
	// line holds the bytes of the current, unterminated line.
	line []byte
	// started stays false until a line with content has been passed through,
	// so a leading blank line or two does not open the answer.
	started bool
}

func newFenceStripper(w io.Writer) *fenceStripper { return &fenceStripper{w: w} }

func (f *fenceStripper) Write(p []byte) (int, error) {
	for _, b := range p {
		if b != '\n' {
			f.line = append(f.line, b)
			continue
		}
		f.emit(string(f.line), true)
		f.line = f.line[:0]
	}
	return len(p), nil
}

// Close flushes a final line that never got its newline.
func (f *fenceStripper) Close() error {
	if len(f.line) > 0 {
		f.emit(string(f.line), false)
		f.line = f.line[:0]
	}
	return nil
}

func (f *fenceStripper) emit(line string, newline bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "```") {
		return
	}
	if !f.started {
		if trimmed == "" {
			return
		}
		f.started = true
	}
	if newline {
		line += "\n"
	}
	io.WriteString(f.w, line)
}

// siteLine matches "path:line" anywhere on a line.
//
// Not anchored to the start: a model that ignores the format still usually
// cites a real path, and "It lives in services/report.py:384" is worth
// checking. The prose guard is the path shape, not the position - an entry
// needs a dot or a slash in it, so "step 2:3" is not read as a place.
var siteLine = regexp.MustCompile(`([^\s:]+):(\d+)`)

// parseSites reads the places out of the WHERE section.
//
// It falls back to the whole answer when there is no WHERE heading: a model
// that ignored the format has still usually cited real paths, and checking
// those is worth more than insisting on the shape.
func parseSites(answer string) []site {
	body := whereSection(answer)
	if strings.TrimSpace(body) == "" {
		body = answer
	}

	var out []site
	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		for _, m := range siteLine.FindAllStringSubmatch(line, -1) {
			path := strings.Trim(m[1], "`\"',")
			// A bare word followed by a number is prose ("step 2:3"), not a
			// path.
			if !strings.ContainsAny(path, "./") {
				continue
			}
			n, err := strconv.Atoi(m[2])
			if err != nil {
				continue
			}
			path = strings.TrimPrefix(path, "./")
			key := path + ":" + m[2]
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, site{Path: path, Line: n})
		}
	}
	return out
}

// heading matches one of the answer's three section titles.
var heading = regexp.MustCompile(`^\s*#*\s*\**\s*(WHERE|WHAT IT IS|WATCH OUT)\b`)

// whereSection returns the lines under the LAST WHERE heading, up to the next
// heading.
//
// The last, not the first: what is captured is every word the model wrote all
// run, and on a run that reaches its turn limit that includes an attempt at the
// answer from an earlier turn. Only the final one is the answer, and a path
// mused about in passing three turns ago is not a citation.
func whereSection(answer string) string {
	lines := strings.Split(answer, "\n")

	start := -1
	for i, l := range lines {
		if m := heading.FindStringSubmatch(l); m != nil && m[1] == "WHERE" {
			start = i + 1
		}
	}
	if start < 0 {
		return ""
	}
	for i := start; i < len(lines); i++ {
		if heading.MatchString(lines[i]) {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}

// splitByExistence sorts cited places into those whose file is really there and
// those that are not.
//
// The second group is the point: measured 2026-09-15, one run cited the
// non-existent path the ISSUE gave, with an invented line number, in a section
// whose own instructions say to cite only what a tool returned. A wrong answer
// that looks exactly like a right one is the worst failure this command has, and
// it is free to check.
func splitByExistence(root string, sites []site) (real, missing []site) {
	for _, s := range sites {
		abs, ok := resolveSite(root, s.Path)
		if !ok {
			missing = append(missing, s)
			continue
		}
		// A regular file, specifically. A directory passes os.Stat happily,
		// and sweeping one would run every recorded search across everything
		// under it and report line numbers from files nobody cited.
		if fi, err := os.Stat(abs); err == nil && fi.Mode().IsRegular() {
			real = append(real, s)
			continue
		}
		missing = append(missing, s)
	}
	return real, missing
}

// resolveSite turns a cited path into an absolute one, and reports false for
// anything outside the root.
//
// locate promises that nothing outside -root is reachable, and that has to hold
// for the paths the MODEL produces too, not just the ones the tools are asked
// for. A cited "/etc/hosts:1" would otherwise be statted and swept.
func resolveSite(root, path string) (string, bool) {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(absRoot, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return abs, true
}

// searchFromGrepCall reads a replayable search out of a grep tool call, or
// reports false if that call is not one worth replaying.
func searchFromGrepCall(name, args, result string) (tools.Search, bool) {
	if name != "grep" || tools.IsNoMatch(result) || strings.TrimSpace(result) == "" {
		return tools.Search{}, false
	}
	var a struct {
		Pattern string `json:"pattern"`
		Literal bool   `json:"literal"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return tools.Search{}, false
	}
	if strings.TrimSpace(a.Pattern) == "" {
		return tools.Search{}, false
	}
	// grep says so when a regex missed but the same text matched literally.
	// Replaying it as a regex would then find nothing.
	if strings.HasPrefix(result, "(matched as literal text") {
		a.Literal = true
	}
	// Case is relaxed here; separators are relaxed in the sweep itself, which
	// runs the words-only variant alongside this one.
	//
	// An earlier version of this comment argued that relaxing separators would
	// be "inventing a search the model never ran". That was wrong, and
	// 10-locate-real-shape is the measurement that says so: the label in a
	// report and the identifier in the code are the same thing spelled
	// differently, and only the words survive between them.
	return tools.Search{Pattern: a.Pattern, Literal: a.Literal, IgnoreCase: true}, true
}

// sweepSites re-runs every recorded search inside every cited file and returns
// the matching lines the answer did not mention.
func sweepSites(ctx context.Context, root string, sites []site, searches []tools.Search) []missed {
	cited := map[string][]int{}
	var files []string
	for _, s := range sites {
		if _, ok := cited[s.Path]; !ok {
			files = append(files, s.Path)
		}
		cited[s.Path] = append(cited[s.Path], s.Line)
	}

	var out []missed
	for _, f := range files {
		abs, ok := resolveSite(root, f)
		if !ok {
			continue
		}
		found := map[int]string{}
		for _, s := range searches {
			// The model's own pattern, and the same WORDS with whatever sits
			// between them in source. The second is the one that finds the
			// sites: measured 2026-09-16 on 10-locate-real-shape, a report
			// says "Shipping Method" and the code says it three ways -
			// 'Shipping Method', 'Shipping\nMethod' and shipping_method - so the
			// literal finds one place of three.
			variants := []tools.Search{s}
			if relaxed, ok := tools.RelaxSeparators(s); ok {
				variants = append(variants, relaxed)
			}
			for _, v := range variants {
				ms, err := tools.GrepLines(ctx, abs, v)
				if err != nil || len(ms) > sweepNoiseLimit {
					continue
				}
				for _, m := range ms {
					if !tools.EndsAtWord(m.Text, v) {
						continue // the phrase is only the start of a longer word
					}
					if _, ok := found[m.Line]; !ok {
						found[m.Line] = m.Text
					}
				}
			}
		}

		// A cited line suppresses THAT line and nothing else.
		//
		// A tolerance of a line or two was tried first, on the theory that a
		// citation of 384 covers a call spanning 383-386. It hid a real second
		// site two lines below the first - caught by
		// TestSweepFindsTheSitesTheAnswerStoppedShortOf, where the headers list
		// sits two lines under the row value. Showing a near-duplicate is mild
		// noise; hiding a site is the exact failure this sweep exists to fix.
		skip := map[int]bool{}
		for _, c := range cited[f] {
			skip[c] = true
		}
		var lines []int
		for n := range found {
			if skip[n] {
				continue
			}
			lines = append(lines, n)
		}
		sort.Ints(lines)
		if len(lines) > sweepMaxPerFile {
			lines = lines[:sweepMaxPerFile]
		}
		for _, n := range lines {
			if len(out) >= sweepMaxTotal {
				return out
			}
			out = append(out, missed{Path: f, Line: n, Text: found[n]})
		}
	}
	return out
}

// renderMissed formats the addendum. It goes to stdout with the answer, not to
// stderr with the progress, because these are places in the code - the thing
// the user ran the command for.
func renderMissed(ms []missed) string {
	if len(ms) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nALSO MATCHING\n")
	b.WriteString("  froe re-ran the searches that worked, inside the files above.\n")
	b.WriteString("  These lines matched and are not in WHERE:\n")
	for _, m := range ms {
		fmt.Fprintf(&b, "  %s:%d  %s\n", m.Path, m.Line, truncate(m.Text, 90))
	}
	return b.String()
}

// quoted matches a phrase in double quotes, single quotes or backticks.
var quoted = regexp.MustCompile("\"([^\"\n]{2,60})\"|'([^'\n]{2,60})'|`([^`\n]{2,60})`")

// issueSearches derives searches from the report's OWN words.
//
// The sweep was built to replay only searches the model had already run, on the
// principle that it should never invent one. Measured 2026-09-16 on
// 10-locate-real-shape, that principle is what now caps it: all three runs
// answered with the single place the literal label appears, and only the one
// that happened to run a grep got the other two sites, because the other two
// never gave the sweep anything to replay.
//
// A phrase the REPORT put in quotes is not an invention either. It is the
// user's own words for the thing they are asking about, and it is available
// before the model has done anything at all.
//
// Paths are skipped: the report's path is the thing most likely to be wrong,
// and searching a file tree for it finds nothing by definition.
func issueSearches(issue string) []tools.Search {
	var out []tools.Search
	seen := map[string]bool{}

	for _, m := range quoted.FindAllStringSubmatch(issue, -1) {
		term := strings.TrimSpace(m[1] + m[2] + m[3])
		if term == "" || seen[term] {
			continue
		}
		if strings.ContainsAny(term, "/\\") || filepath.Ext(term) != "" {
			continue
		}
		// Two word runs at least - the same threshold RelaxSeparators uses,
		// and for the same reason. One bare word out of a sentence is a
		// search for nothing in particular.
		if len(wordish.FindAllString(term, -1)) < 2 {
			continue
		}
		seen[term] = true
		out = append(out, tools.Search{Pattern: term, Literal: true, IgnoreCase: true})
		if len(out) >= maxIssueSearches {
			break
		}
	}
	return out
}

// wordish matches one run of letters and digits.
var wordish = regexp.MustCompile(`[A-Za-z0-9]+`)

// maxIssueSearches caps how many of the report's phrases are replayed. A report
// quotes a handful of things and only the first few are what it is about.
const maxIssueSearches = 3

// ── Correcting a citation that does not exist ────────────────────────────────
//
// A cited path that is not in the tree used to be a line on stderr and nothing
// more: the answer kept the bad citation, and the user was left to work out
// what was meant. Measured 2026-09-17, mistral-medium cited a file under a
// directory it does not live in on all three runs of 10-locate-real-shape.
// The file exists — at the tree root, not in that directory — so the model
// had found something real and mis-stated where it was.
//
// The obvious fix is to resolve the base name and swap the path in. It is
// WRONG, and the same fixture proves it: a same-named file is a DECOY there,
// carrying a shipping term while being nowhere near the site. Promoting it
// would convert an error froe had DETECTED into one froe ENDORSED, which is
// strictly worse than the stderr line it replaced.
//
// So resolution is not promotion. froe reports what it found, replaces the
// model's asserted line number with lines it verified itself, and says plainly
// when it could not confirm anything — leaving the judgement where the rest of
// the design leaves it.

// resolution is a cited path that is not in the tree, with whatever froe could
// establish about it.
type resolution struct {
	Cited site
	// Candidates are real files sharing the cited base name, repo-relative.
	Candidates []string
	// Evidence is lines in the single candidate that the recorded searches
	// actually match. Empty means nothing corroborated the citation.
	Evidence []missed
}

// maxCandidates caps how many same-named files are listed. More than a handful
// means the base name is not distinctive and the list is not a correction.
const maxCandidates = 4

// resolveMissing looks for what a non-existent citation might have meant.
//
// Base name only: a model that gets the directory wrong usually has the file
// right, and that is the failure actually observed. A fuzzier match would start
// inventing.
func resolveMissing(ctx context.Context, root string, missing []site, searches []tools.Search) []resolution {
	if len(missing) == 0 {
		return nil
	}
	files, err := repo.ListFiles(ctx, root)
	if err != nil {
		files = nil
	}

	byBase := map[string][]string{}
	for _, f := range files {
		byBase[filepath.Base(f)] = append(byBase[filepath.Base(f)], f)
	}

	out := make([]resolution, 0, len(missing))
	for _, s := range missing {
		r := resolution{Cited: s}
		cands := byBase[filepath.Base(s.Path)]
		sort.Strings(cands)
		if len(cands) > maxCandidates {
			cands = cands[:maxCandidates]
		}
		r.Candidates = cands

		// Evidence only where there is one candidate. With several, froe does
		// not know which was meant, and searching all of them would present a
		// guess as a finding.
		if len(cands) == 1 {
			r.Evidence = sweepSites(ctx, root, []site{{Path: cands[0]}}, searches)
		}
		out = append(out, r)
	}
	return out
}

// renderResolutions formats the correction block.
//
// It goes to stdout with the answer because it is part of the answer: a
// citation the user would otherwise act on has been withdrawn, and what
// replaced it is a place in the code.
func renderResolutions(rs []resolution) string {
	if len(rs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nNOT IN THIS TREE\n")
	b.WriteString("  froe checked every path in WHERE. These are not here, so do not\n")
	b.WriteString("  act on them as cited:\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "  ✕ %s:%d\n", r.Cited.Path, r.Cited.Line)
		switch {
		case len(r.Candidates) == 0:
			b.WriteString("      no file of that name exists anywhere in the tree - treat the\n")
			b.WriteString("      citation as unfounded, not as a typo\n")
		case len(r.Candidates) == 1 && len(r.Evidence) > 0:
			fmt.Fprintf(&b, "      one file of that name exists: %s\n", r.Candidates[0])
			fmt.Fprintf(&b, "      froe did NOT verify it is what was meant. The searches match it at:\n")
			for _, m := range r.Evidence {
				fmt.Fprintf(&b, "        %s:%d  %s\n", m.Path, m.Line, truncate(m.Text, 80))
			}
		case len(r.Candidates) == 1:
			fmt.Fprintf(&b, "      one file of that name exists: %s\n", r.Candidates[0])
			b.WriteString("      but nothing that was searched for matches inside it, so the\n")
			b.WriteString("      citation is not simply a wrong directory\n")
		default:
			b.WriteString("      files of that name exist, and froe cannot tell which was meant:\n")
			for _, c := range r.Candidates {
				fmt.Fprintf(&b, "        %s\n", c)
			}
		}
	}
	return b.String()
}

// ── An answer produced without opening anything ──────────────────────────────
//
// Measured 2026-09-17, first full eval suite: mistral-medium scored 0/3 on BOTH
// locate tasks with `0 tool calls` in all six runs, while bonsai-27b scores 3/3
// on both. The pre-search added the day before hands a large-context model
// enough to answer at turn 1, so it never opens a file — and an answer nothing
// was read for is an answer nothing corroborates.
//
// 09-locate-wrong-path is the clearest case: the model cites the header and
// misses the `col_widths` coupling TWO LINES BELOW its own citation. The sweep
// cannot catch that, because the sweep replays searches and no search matches a
// line that does not contain the searched term. Position is the only signal
// left, and position is free.
//
// So: print what surrounds a citation the run never opened. The gate was
// ToolCalls == 0 until 2026-09-18, which was too narrow by exactly one tool
// call. Measured on two real production issues: every run made ONE grep and
// then answered, so the block never fired - and in one of them a
// `col_widths` coupling sat three lines under a cited header, inside
// surroundRadius, and went unreported three times running.
//
// A grep hit is not a reading. What corroborates a citation is having opened
// the line, so the gate is per-citation and range-aware: read_file takes an
// offset and a limit, and reading lines 1-50 says nothing about line 493.
// A run that opened nothing still surrounds everything, so the old behaviour
// is the empty case of this one rather than a branch beside it.
//
// It does not claim the surrounding lines are relevant. It says the answer was
// never checked against the file and shows what is there, which is a different
// and honest claim.

// openRange is a span of lines read_file actually returned. A citation is
// corroborated only if it falls inside one: reading lines 1-50 of a file says
// nothing about the line 493 the answer went on to cite.
type openRange struct{ From, To int }

// readRangeFromCall reports the span a read_file call returned. offset defaults
// to 1 and an absent limit means "to the end of the file" - except that
// read_file truncates a long one and says where it stopped, and past that line
// nothing was returned whatever the call asked for.
func readRangeFromCall(name, args, result string) (string, openRange, bool) {
	if name != "read_file" {
		return "", openRange{}, false
	}
	var a struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", openRange{}, false
	}
	path := strings.TrimSpace(a.Path)
	if path == "" {
		return "", openRange{}, false
	}
	from := a.Offset
	if from < 1 {
		from = 1
	}
	to := math.MaxInt
	if a.Limit > 0 {
		to = from + a.Limit - 1
	}
	if n, ok := truncatedAt(result); ok && n < to {
		to = n
	}
	return path, openRange{From: from, To: to}, true
}

// truncatedAt reads the line number out of read_file's own truncation notice.
func truncatedAt(result string) (int, bool) {
	const marker = "(truncated at line "
	i := strings.Index(result, marker)
	if i < 0 {
		return 0, false
	}
	rest := result[i+len(marker):]
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if end <= 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

// covered reports whether the run actually opened the line this site cites.
func covered(opened map[string][]openRange, s site) bool {
	for _, r := range opened[s.Path] {
		if s.Line >= r.From && s.Line <= r.To {
			return true
		}
	}
	return false
}

// surroundRadius is how far either side of a citation to show. Three lines
// catches a coupling in the next statement without turning into a file dump.
const surroundRadius = 3

// surroundings returns the non-blank, uncited lines within surroundRadius of
// each cited line.
func surroundings(root string, sites []site, opened map[string][]openRange) []missed {
	// Every citation, so a line the answer already named is never echoed back -
	// including one in a file that WAS opened, which still adds nothing here.
	cited := map[string]map[int]bool{}
	for _, s := range sites {
		if _, ok := cited[s.Path]; !ok {
			cited[s.Path] = map[int]bool{}
		}
		cited[s.Path][s.Line] = true
	}

	// Only the citations nothing in the run corroborates.
	needs := map[string][]int{}
	var files []string
	for _, s := range sites {
		if covered(opened, s) {
			continue
		}
		if _, ok := needs[s.Path]; !ok {
			files = append(files, s.Path)
		}
		needs[s.Path] = append(needs[s.Path], s.Line)
	}

	var out []missed
	for _, f := range files {
		abs, ok := resolveSite(root, f)
		if !ok {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")

		want := map[int]bool{}
		for _, n := range needs[f] {
			for i := n - surroundRadius; i <= n+surroundRadius; i++ {
				if i >= 1 && i <= len(lines) && !cited[f][i] {
					want[i] = true
				}
			}
		}

		var ns []int
		for n := range want {
			if strings.TrimSpace(lines[n-1]) != "" {
				ns = append(ns, n)
			}
		}
		sort.Ints(ns)
		if len(ns) > sweepMaxPerFile {
			ns = ns[:sweepMaxPerFile]
		}
		for _, n := range ns {
			if len(out) >= sweepMaxTotal {
				return out
			}
			out = append(out, missed{Path: f, Line: n, Text: strings.TrimSpace(lines[n-1])})
		}
	}
	return out
}

// renderSurroundings formats the block, and is explicit that froe is showing
// context rather than asserting relevance.
func renderSurroundings(ms []missed) string {
	if len(ms) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nAROUND THOSE LINES\n")
	b.WriteString("  These citations were never opened during the run, so nothing in it\n")
	b.WriteString("  corroborates them. froe has not judged these relevant - they are\n")
	b.WriteString("  simply what sits beside each citation it did not read:\n")
	for _, m := range ms {
		fmt.Fprintf(&b, "  %s:%d  %s\n", m.Path, m.Line, truncate(m.Text, 84))
	}
	return b.String()
}
