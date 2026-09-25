package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Edit replaces an exact string in a file.
type Edit struct{}

func (Edit) Name() string   { return "edit_file" }
func (Edit) Mutating() bool { return true }
func (Edit) Description() string {
	return "Replace an exact string in a file. By default old_string must appear EXACTLY ONCE - " +
		"include surrounding lines to make it unique. To rename something that appears several times, " +
		"set replace_all to true. Read the file first to get the text right."
}

func (Edit) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":       {"type": "string", "description": "Path relative to the project root"},
    "old_string": {"type": "string", "description": "Exact text to replace, must be unique unless replace_all is true"},
    "new_string": {"type": "string", "description": "Replacement text"},
    "replace_all": {"type": "boolean", "description": "Replace every occurrence instead of requiring exactly one. Use for renames."}
  },
  "required": ["path", "old_string", "new_string"]
}`)
}

func (Edit) Run(ctx context.Context, args json.RawMessage, env Env) (string, error) {
	var a struct {
		Path       string `json:"path"`
		Old        string `json:"old_string"`
		New        string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Old == "" {
		return "", fmt.Errorf("old_string must not be empty; use write_file to create a file")
	}
	abs, err := resolve(env, a.Path)
	if err != nil {
		return "", err
	}

	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	content := string(b)

	n := strings.Count(content, a.Old)
	if n == 0 {
		return "", fmt.Errorf("old_string not found in %s%s", a.Path, diagnoseMismatch(content, a.Old))
	}
	// Ambiguity is an error by default, not a coin flip: replacing the wrong
	// occurrence is a silent corruption the model has no way to notice. But
	// refusing with no alternative made renames impossible — measured, the
	// agent renamed a definition, could not touch its two call sites, and fell
	// back to a malformed sed. replace_all is the deliberate way through.
	if n > 1 && !a.ReplaceAll {
		return "", fmt.Errorf("old_string appears %d times in %s - add surrounding context to target one, "+
			"or set replace_all true to change all %d", n, a.Path, n)
	}

	if err := leakedMarkup("new_string", a.New, content); err != nil {
		return "", err
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	count := 1
	if a.ReplaceAll {
		count = -1
	}
	updated := strings.Replace(content, a.Old, a.New, count)
	if err := os.WriteFile(abs, []byte(updated), info.Mode().Perm()); err != nil {
		return "", err
	}

	delta := (strings.Count(a.New, "\n") - strings.Count(a.Old, "\n")) * n
	if !a.ReplaceAll {
		n = 1
	}
	plural := ""
	if n != 1 {
		plural = "s"
	}
	return fmt.Sprintf("edited %s (%d replacement%s, %+d lines)", rel(env, abs), n, plural, delta), nil
}

// diagnoseMismatch explains WHY an exact match failed, when the reason is
// discoverable.
//
// Indentation is the overwhelmingly common cause: a model reproduces a tab-
// indented Go file with spaces and the edit silently fails. "read the file and
// match it exactly" is useless advice when the model believes it did.
func diagnoseMismatch(content, old string) string {
	// A line break where the file has the two characters \ and n. Source code
	// is full of escaped newlines inside strings, and a model that writes
	// "Causal\nOrder" into a JSON argument sends a real line break. Measured
	// 2026-09-24: qwen3-nothink:8b, told exactly which line held
	// 'Causal\nOrder', retried the same edit until it was aborted - the
	// generic advice below never names the one thing that was wrong.
	if strings.Contains(old, "\n") && !strings.Contains(content, old) {
		if escaped := strings.ReplaceAll(old, "\n", `\n`); strings.Contains(content, escaped) {
			return " - the file has a backslash followed by n (the two characters \\ and n, " +
				"an escape inside a string), where your old_string has a real line break. " +
				"In the JSON arguments write it as \\\\n so it arrives as \\n, " +
				"and do the same in new_string."
		}
	}
	norm := func(s string) string {
		var b strings.Builder
		for _, line := range strings.Split(s, "\n") {
			b.WriteString(strings.TrimSpace(line))
			b.WriteByte('\n')
		}
		return b.String()
	}

	if strings.Contains(norm(content), strings.TrimRight(norm(old), "\n")) {
		// Naming the indent style was not enough: the model retried with two
		// tabs where the file had one. Show the actual bytes instead, with
		// whitespace made visible, so there is nothing left to guess.
		if exact := locateSimilar(content, old); exact != "" {
			return " - the text matches except for whitespace. The file contains exactly this " +
				"(→ is a tab, · is a space):\n" + exact +
				"\nCopy it verbatim, converting → back to tab characters."
		}
		indent := "tabs"
		if usesSpaces(content) {
			indent = "spaces"
		}
		return fmt.Sprintf(" - the text matches except for leading whitespace. "+
			"This file is indented with %s; copy the indentation exactly.", indent)
	}
	if strings.Contains(content, strings.TrimSpace(old)) {
		return " - the text matches once surrounding whitespace is ignored. Include the exact leading and trailing whitespace."
	}
	// Not a whitespace problem, so the model's text is wrong in substance -
	// usually a block spliced together from two similar places. Telling it to
	// "read the file" only buys another read of a window it already has. Show
	// the real text where its first line actually occurs instead.
	if exact := locateSimilar(content, old); exact != "" {
		return " - the text differs by more than whitespace. The file contains exactly this " +
			"where your first line occurs (\u2192 is a tab, \u00b7 is a space):\n" + exact +
			"\nCopy it verbatim, converting \u2192 back to tab characters."
	}
	return " - read the file and copy the text exactly, including indentation."
}

// usesSpaces reports whether indented lines start with a space rather than a tab.
func usesSpaces(content string) bool {
	var tabs, spaces int
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "\t") {
			tabs++
		} else if strings.HasPrefix(line, "  ") {
			spaces++
		}
	}
	return spaces > tabs
}

// locateSimilar finds the region of content matching old apart from whitespace
// and renders it with whitespace made visible.
func locateSimilar(content, old string) string {
	want := strings.TrimSpace(strings.Split(strings.TrimSpace(old), "\n")[0])
	if want == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	wantLines := len(strings.Split(strings.TrimSpace(old), "\n"))

	for i, line := range lines {
		if strings.TrimSpace(line) != want {
			continue
		}
		end := i + wantLines
		if end > len(lines) {
			end = len(lines)
		}
		var b strings.Builder
		for _, l := range lines[i:end] {
			b.WriteString("    ")
			b.WriteString(visibleWhitespace(l))
			b.WriteByte('\n')
		}
		return strings.TrimRight(b.String(), "\n")
	}
	return ""
}

// visibleWhitespace renders leading tabs and spaces as printable characters.
func visibleWhitespace(line string) string {
	i := 0
	for i < len(line) && (line[i] == '\t' || line[i] == ' ') {
		i++
	}
	prefix := strings.ReplaceAll(line[:i], "\t", "\u2192")
	prefix = strings.ReplaceAll(prefix, " ", "\u00b7")
	return prefix + line[i:]
}
