package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// maxGrepLines bounds output. A broad pattern across a large repo otherwise
// returns more than the model's whole context window.
const maxGrepLines = 200

// Grep searches file contents.
//
// It shells out to ripgrep rather than reimplementing search: rg is already on
// the machine, respects .gitignore, and is far faster than anything worth
// writing here. docs/ARCHITECTURE.md §5 — ripgrep beats embeddings for code
// lookup at this scale.
type Grep struct{}

func (Grep) Name() string   { return "grep" }
func (Grep) Mutating() bool { return false }
func (Grep) Description() string {
	return "Search file contents with a regular expression. Returns matching lines with file and line number. " +
		"This is the fastest way to locate code - prefer it over reading files speculatively."
}

func (Grep) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Text to search for. Treated as a REGEX unless literal is true"},
    "literal": {"type": "boolean", "description": "Set true to search for the text exactly, ignoring regex characters like ( ) . * [ ]"},
    "path":    {"type": "string", "description": "Optional subdirectory to limit the search to"},
    "glob":    {"type": "string", "description": "Optional file filter, e.g. *.go"}
  },
  "required": ["pattern"]
}`)
}

// grepArgs is named so the literal-retry helper can take the same arguments
// rather than re-deriving them.
type grepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Glob    string `json:"glob"`
	Literal bool   `json:"literal"`
}

func (Grep) Run(ctx context.Context, args json.RawMessage, env Env) (string, error) {
	var a grepArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	searchPath := a.Path
	if searchPath == "" {
		searchPath = "."
	}
	abs, err := resolve(env, searchPath)
	if err != nil {
		return "", err
	}

	rg, err := exec.LookPath("rg")
	if err != nil {
		return "", fmt.Errorf("ripgrep (rg) is not installed")
	}

	// --  terminates flags so a pattern beginning with - is not read as one.
	argv := []string{"--line-number", "--no-heading", "--color", "never", "--max-count", "50"}
	if a.Literal {
		argv = append(argv, "--fixed-strings")
	}
	if a.Glob != "" {
		argv = append(argv, "--glob", a.Glob)
	}
	argv = append(argv, "--", a.Pattern, abs)

	cmd := exec.CommandContext(ctx, rg, argv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// rg exits 1 for "no matches", which is a result, not a failure.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			// No regex match. Do NOT advise "retry with literal" on a guess.
			//
			// This used to fire whenever the pattern contained any of
			// ()[]{}.*+?|^$\, on the theory that such a pattern was pasted
			// code. But that describes every deliberate regex too. Measured
			// 2026-09-15: a model searched "Shipping.*Method|shipping.*method",
			// got the hint, obediently re-ran it with literal:true - which can
			// never match - and then kept the habit, searching literally for
			// "tbl\s*\(\s*" three more times. The advice actively taught it
			// to break its own searches.
			//
			// So check instead of guessing: run the same pattern literally and
			// only mention it if that actually finds something. Then hand the
			// matches straight back rather than spending a turn on a retry.
			if !a.Literal {
				if lit, ok := literalRetry(ctx, rg, a, abs); ok {
					// Careful with the wording: this is a SUCCESS. Starting it
					// with the no-match prefix would make IsNoMatch() true and
					// let the fruitless-search detector count a find as a dead
					// end. Caught by TestGrepReturnsLiteralMatchesWhenTheRegexMisses.
					return fmt.Sprintf("(matched as literal text rather than as a regular "+
						"expression - pass \"literal\": true to search this way directly)\n%s",
						formatMatches(lit, abs)), nil
				}
			}
			return fmt.Sprintf(noMatchesForPrefix+"%q)", a.Pattern), nil
		}
		// Surface ripgrep's own message. "exit status 2" tells a model nothing
		// it can act on, and an unactionable error is how a run gets stuck
		// repeating the same doomed call.
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("ripgrep: %s", msg)
	}

	return formatMatches(out, abs), nil
}

// literalRetry re-runs a failed regex search as a fixed-string search, so the
// tool can say whether the text is actually there instead of speculating.
func literalRetry(ctx context.Context, rg string, a grepArgs, abs string) ([]byte, bool) {
	argv := []string{"--line-number", "--no-heading", "--color", "never",
		"--max-count", "50", "--fixed-strings"}
	if a.Glob != "" {
		argv = append(argv, "--glob", a.Glob)
	}
	argv = append(argv, "--", a.Pattern, abs)

	out, err := exec.CommandContext(ctx, rg, argv...).Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return nil, false
	}
	return out, true
}

// formatMatches trims absolute paths and caps the listing.
func formatMatches(out []byte, abs string) string {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	var b strings.Builder
	for i, line := range lines {
		if i >= maxGrepLines {
			fmt.Fprintf(&b, "(%d more matches - narrow the pattern)\n", len(lines)-maxGrepLines)
			break
		}
		// Absolute paths waste tokens and leak the machine's layout.
		b.WriteString(strings.TrimPrefix(line, abs+"/"))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// Search is one replayable search: what to look for and how to read it.
type Search struct {
	Pattern string
	// Literal searches for the text exactly, ignoring regex metacharacters.
	Literal bool
	// IgnoreCase is a deliberate choice of the CALLER, never a default. It is
	// right for locate's completeness sweep, where a label and the field
	// behind it differ by exactly that, and wrong for finding the references
	// to a symbol, where Foo and foo are two different things.
	IgnoreCase bool
}

// maxSeparator bounds how much may sit between two words of a relaxed search.
//
// Three characters covers every way a label is written in source - a space, an
// underscore, a hyphen, nothing at all, or the two characters of an escape like
// \n - without letting the words drift apart into a coincidence. Measured on
// the 10-locate-real-shape fixture: `Shipping.{0,3}Method` finds all three places
// the field appears and does NOT match the `Shipping Region` lookalike,
// while the unbounded `Shipping.*Method` the model sometimes writes would.
const maxSeparator = 3

// wordRun matches one run of letters and digits.
var wordRun = regexp.MustCompile(`[A-Za-z0-9]+`)

// alreadyRegex reports whether a pattern is a deliberate regular expression
// rather than a phrase copied out of a report.
var alreadyRegex = regexp.MustCompile(`[*+?\[\](){}|^$\\]`)

// RelaxSeparators derives a search for the same WORDS with whatever actually
// sits between them in source code.
//
// This is the difference between finding one site and finding all of them.
// Measured 2026-09-16: a report says "Shipping Method", and the code says
// 'Shipping Method' in one place, 'Shipping\nMethod' in the table headers and
// data.get('shipping_method') in the row that fills it. A literal search finds
// the first and misses the two that matter. Only the words survive the journey
// from a label into code, so only the words are searched for.
//
// It returns false for a single word, where there is nothing to relax, and for
// a pattern that is already a regex - the model wrote that one deliberately,
// and rebuilding it from its word runs would produce nonsense.
//
// The result is meant to be run IN ADDITION to the original, never instead of
// it: a relaxed pattern is bounded and can therefore miss something the
// original catches.
func RelaxSeparators(s Search) (Search, bool) {
	if alreadyRegex.MatchString(s.Pattern) && !s.Literal {
		return Search{}, false
	}
	words := wordRun.FindAllString(s.Pattern, -1)
	if len(words) < 2 {
		return Search{}, false
	}
	sep := fmt.Sprintf(".{0,%d}", maxSeparator)
	return Search{
		Pattern: strings.Join(words, sep),
		// Case goes with it. A label is Title Case in a report and lower_snake
		// in the code it names.
		IgnoreCase: true,
	}, true
}

// EndsAtWord reports whether text holds a match of s whose last word ends
// there, rather than running on into a longer word.
//
// A search for a label is also a search for every longer word that starts with
// it: "Causal Order" matches "Causal Ordering", and so does the relaxed
// Causal.{0,3}Order. Measured 2026-09-24 on 10-locate-real-shape: the sweep
// put both "Causal Ordering" lines - the lookalike table - beside the two real
// sites it found, so the list could not be acted on as it stood.
//
// A word ends where a lowercase letter is NOT followed by another lowercase
// letter. So "Ordering" is rejected, while causal_order_table and
// CausalOrderTable are kept: an underscore or a capital starts a new word.
func EndsAtWord(text string, s Search) bool {
	pattern := s.Pattern
	if s.Literal {
		pattern = regexp.QuoteMeta(pattern)
	}
	if s.IgnoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return true // cannot judge it, so do not hide it
	}
	for _, loc := range re.FindAllStringIndex(text, -1) {
		end := loc[1]
		if end >= len(text) || end == 0 || !isLower(text[end-1]) || !isLower(text[end]) {
			return true
		}
	}
	return false
}

func isLower(c byte) bool { return c >= 'a' && c <= 'z' }

// Match is one matching line.
type Match struct {
	// Path is relative to the search root, and empty when one file was
	// searched - the caller already knows which.
	Path string
	Line int
	Text string
}

// GrepLines runs one search inside ONE file and returns every line it matches.
//
// It backs `froe locate`'s completeness sweep. The sweep re-runs the searches
// the model itself already used successfully, restricted to the files it cited,
// because measured over three runs on a real production issue the model found the
// right file every time and never once found all the sites in it: having
// identified the file, it never searched inside it. Doing that here costs no
// turns and no tokens, and uses no pattern the model did not already prove.
//
// Unlike Grep.Run this does not cap, reformat or trim: the caller wants line
// numbers, and a single file cannot produce the unbounded output the cap exists
// for.
func GrepLines(ctx context.Context, file string, s Search) ([]Match, error) {
	return runGrep(ctx, file, s, false)
}

// GrepTree runs one search across a whole directory, returning paths relative
// to it.
//
// This is how `froe explain` finds what references a symbol. Deliberately not
// the model's job: a symbol's references are an exact, mechanical fact, and a
// list computed here is both complete and free, where the same list assembled
// over three tool calls is neither.
func GrepTree(ctx context.Context, root string, s Search) ([]Match, error) {
	return runGrep(ctx, root, s, true)
}

func runGrep(ctx context.Context, path string, s Search, withPath bool) ([]Match, error) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		return nil, fmt.Errorf("ripgrep (rg) is not installed")
	}

	argv := []string{"--line-number", "--no-heading", "--color", "never"}
	if withPath {
		argv = append(argv, "--with-filename")
	} else {
		argv = append(argv, "--no-filename")
	}
	if s.Literal {
		argv = append(argv, "--fixed-strings")
	}
	if s.IgnoreCase {
		argv = append(argv, "--ignore-case")
	}
	argv = append(argv, "--", s.Pattern, path)

	out, err := exec.CommandContext(ctx, rg, argv...).Output()
	if err != nil {
		// Exit 1 is "no matches", which is an answer rather than a failure.
		// Exit 2 is a bad pattern - also not worth surfacing to these callers,
		// both of which are replaying or composing a pattern rather than
		// taking one from a user to correct.
		return nil, nil
	}

	var ms []Match
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		m, ok := parseMatch(line, withPath)
		if !ok {
			continue
		}
		if withPath {
			rel, err := filepath.Rel(path, m.Path)
			if err == nil {
				m.Path = rel
			}
		}
		ms = append(ms, m)
	}
	return ms, nil
}

// parseMatch reads one ripgrep output line. A path can contain a colon, so the
// fields are taken from the left in the order rg emits them rather than by
// splitting on every colon.
func parseMatch(line string, withPath bool) (Match, bool) {
	var m Match
	if withPath {
		// rg prints <path>:<line>:<text>, and the line number is the first
		// field that parses as a number, so scan for it from the left.
		rest := line
		for {
			i := strings.IndexByte(rest, ':')
			if i < 0 {
				return Match{}, false
			}
			candidate := rest[:i]
			if n, err := strconv.Atoi(candidate); err == nil && m.Path != "" {
				m.Line = n
				m.Text = strings.TrimSpace(rest[i+1:])
				return m, true
			}
			if m.Path == "" {
				m.Path = candidate
			} else {
				m.Path += ":" + candidate
			}
			rest = rest[i+1:]
		}
	}
	n, text, ok := strings.Cut(line, ":")
	if !ok {
		return Match{}, false
	}
	num, err := strconv.Atoi(n)
	if err != nil {
		return Match{}, false
	}
	return Match{Line: num, Text: strings.TrimSpace(text)}, true
}
