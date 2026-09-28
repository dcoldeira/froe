package agent

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A run that does the first item of a numbered list and stops has finished the
// wrong job.
//
// Measured 2026-09-28 in Neovim on QRL, mistral-medium-latest: asked for four
// numbered changes to a physics script, it made (1), ran the script, and
// answered "Done. Replaced apply_depolarizing_to_one_qubit ... All results
// still match." Items (2)-(4) were never mentioned. The correction before it,
// also a list, lost half its items the same way. Nothing in the run was wrong;
// it simply stopped remembering the list after the first edit.
//
// So when a run that changed files finishes, and the task numbered its asks,
// the items are quoted back once unless the answer already accounts for each.

// checklistMaxItem bounds how far a list may run; more than this is a
// numbered document pasted into the task, not a list of asks.
const checklistMaxItem = 12

// checklistItemLen caps how much of each item is quoted back.
const checklistItemLen = 140

// inlineMarker matches "(1)" at the start of the task or after whitespace, so
// "f(1)" and "1/sqrt(2)" are not read as items.
var inlineMarker = regexp.MustCompile(`(?:^|\s)\((\d{1,2})\)\s`)

// lineMarker matches "1." or "1)" at the start of a line.
var lineMarker = regexp.MustCompile(`(?m)^\s*(\d{1,2})[.)]\s`)

// checklistItems returns the task's numbered asks, in order, or nil when the
// task has no list of two or more items numbered 1, 2, 3...
func checklistItems(task string) []string {
	for _, re := range []*regexp.Regexp{inlineMarker, lineMarker} {
		if items := splitNumbered(task, re); len(items) >= 2 {
			return items
		}
	}
	return nil
}

func splitNumbered(task string, re *regexp.Regexp) []string {
	locs := re.FindAllStringSubmatchIndex(task, -1)
	// Keep only the run that counts 1, 2, 3... from the first "1".
	var marks [][]int
	for _, l := range locs {
		n, _ := strconv.Atoi(task[l[2]:l[3]])
		if n == len(marks)+1 && n <= checklistMaxItem {
			marks = append(marks, l)
		}
	}
	if len(marks) < 2 {
		return nil
	}
	items := make([]string, len(marks))
	for i, l := range marks {
		end := len(task)
		if i+1 < len(marks) {
			end = marks[i+1][0]
		}
		text := strings.Join(strings.Fields(task[l[1]:end]), " ")
		if len(text) > checklistItemLen {
			text = text[:checklistItemLen] + "…"
		}
		items[i] = text
	}
	return items
}

// answerCoversAll reports whether an answer refers to every item by number,
// as "(2)", "2." or "2)" - a model that tracked the list says so item by item.
func answerCoversAll(answer string, n int) bool {
	for i := 1; i <= n; i++ {
		num := strconv.Itoa(i)
		ref := regexp.MustCompile(`(?:^|\s|\*)(?:\(` + num + `\)|` + num + `[.)])`)
		if !ref.MatchString(answer) {
			return false
		}
	}
	return true
}

// checklistNudge quotes the task's items back and asks for each one.
func checklistNudge(items []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Before you finish: the task listed %d numbered items, and your answer "+
		"does not say what happened to each one:\n", len(items))
	for i, it := range items {
		fmt.Fprintf(&b, "  (%d) %s\n", i+1, it)
	}
	b.WriteString("\nCheck each item against the files now. Do any that are not done. " +
		"Then answer with one line per item: (1) done, or why not; (2) ...")
	return b.String()
}
