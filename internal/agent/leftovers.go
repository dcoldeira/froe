package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/dcoldeira/froe/internal/tools"
)

// A run that edits one place of several and stops has finished the wrong job.
//
// Measured 2026-09-24 on 07-multi-site, five models: the column appears four
// ways in one file - "Causal Order", "Causal\nOrder", causal_order() and its
// width - and every model searched for the exact phrase, found the first, and
// stopped. froe locate already had the answer to this: search the files it
// found for the same WORDS however they are spelled, and hand what turns up
// back to the model once to judge. On 10-locate-real-shape that took
// bonsai-27b from 0/3 to 3/3. This is the same check for a run that edits.
//
// The terms are never invented. The candidates are the phrases the task itself
// quotes, the searches the run made that found something, and the text its
// own edits replaced - and a candidate counts only once an edit has actually
// REMOVED it. A search is often navigation, not the change: measured
// 2026-09-24 on 06, bonsai-27b grepped for the table's name to find it, the
// check flagged the assert that reads that name, and the extra turn pushed a
// correct run past the time limit.

// leftoverNoiseLimit skips a term that matches too much of a file to be the
// thing being changed.
const leftoverNoiseLimit = 25

// leftoverMax caps the lines shown in one push.
const leftoverMax = 12

// taskQuote matches a phrase the task puts in quotes or backticks.
var taskQuote = regexp.MustCompile("\"([^\"\n]{3,60})\"|`([^`\n]{3,60})`")

// changeTerms collects what a run may be changing, as searches, and the
// edits that show which of them it really did change.
type changeTerms struct {
	list  []tools.Search
	seen  map[string]bool
	edits [][2]string // old_string, new_string of every successful edit
}

// add records a phrase of two words or more. One word out of a sentence
// matches everything; RelaxSeparators refuses it for the same reason.
func (c *changeTerms) add(phrase string) {
	phrase = strings.TrimSpace(phrase)
	if phrase == "" || len(phrase) > 80 || strings.Contains(phrase, "\n") {
		return
	}
	s := tools.Search{Pattern: phrase, Literal: true, IgnoreCase: true}
	if _, ok := tools.RelaxSeparators(s); !ok {
		return
	}
	key := strings.ToLower(phrase)
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	if !c.seen[key] {
		c.seen[key] = true
		c.list = append(c.list, s)
	}
}

// fromTask records the task's own quoted phrases.
func (c *changeTerms) fromTask(task string) {
	for _, m := range taskQuote.FindAllStringSubmatch(task, -1) {
		c.add(m[1] + m[2])
	}
}

// fromCall records what a successful call says the run is changing: a grep
// pattern that found something, or the text an edit replaced.
func (c *changeTerms) fromCall(name, args, result string) {
	switch name {
	case "grep":
		if tools.IsNoMatch(result) {
			return
		}
		var a struct {
			Pattern string `json:"pattern"`
		}
		if json.Unmarshal([]byte(args), &a) == nil {
			c.add(a.Pattern)
		}
	case "edit_file":
		var a struct {
			Old string `json:"old_string"`
			New string `json:"new_string"`
		}
		if json.Unmarshal([]byte(args), &a) == nil {
			c.add(a.Old)
			c.edits = append(c.edits, [2]string{a.Old, a.New})
		}
	}
}

// removed returns the candidates some edit took out: each matches an edit's
// old text, spelled any way, more often than its new text.
func (c *changeTerms) removed() []tools.Search {
	var out []tools.Search
	for _, t := range c.list {
		r, ok := tools.RelaxSeparators(t)
		if !ok {
			continue
		}
		re, err := regexp.Compile("(?i)" + r.Pattern)
		if err != nil {
			continue
		}
		for _, e := range c.edits {
			if len(re.FindAllString(e[0], -1)) > len(re.FindAllString(e[1], -1)) {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// leftover is a line in an edited file that still matches a change term.
type leftover struct {
	Path string
	Line int
	Text string
}

// leftovers searches every edited file for the change terms, relaxed to any
// spelling of the same words, and keeps only matches that end where a word
// ends - "Order" is not "Ordering".
func leftovers(ctx context.Context, env tools.Env, files []string, terms []tools.Search) []leftover {
	var out []leftover
	for _, f := range files {
		abs, err := tools.ResolvePath(env, f)
		if err != nil {
			continue
		}
		seen := map[int]bool{}
		for _, t := range terms {
			variants := []tools.Search{t}
			if r, ok := tools.RelaxSeparators(t); ok {
				variants = append(variants, r)
			}
			for _, v := range variants {
				ms, err := tools.GrepLines(ctx, abs, v)
				if err != nil || len(ms) > leftoverNoiseLimit {
					continue
				}
				for _, m := range ms {
					if seen[m.Line] || !tools.EndsAtWord(m.Text, v) {
						continue
					}
					seen[m.Line] = true
					out = append(out, leftover{Path: f, Line: m.Line, Text: strings.TrimSpace(m.Text)})
					if len(out) >= leftoverMax {
						return out
					}
				}
			}
		}
	}
	return out
}

// leftoverNudge is sent when a run finishes with matching lines still in the
// files it edited.
func leftoverNudge(ls []leftover) string {
	var b strings.Builder
	b.WriteString("Before you finish: froe searched the files you edited for what you were " +
		"changing, spelled any way (spaces, underscores, \\n, case), and these lines still match:\n")
	for _, l := range ls {
		fmt.Fprintf(&b, "  %s:%d  %s\n", l.Path, l.Line, truncateLine(l.Text, 100))
	}
	b.WriteString("\nIf a line is part of the same change - the same field spelled another way, " +
		"its width, the code that fills it - change it too, and anything that must stay " +
		"consistent with it. If it is not, leave it. Then finish.")
	return b.String()
}

func truncateLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
