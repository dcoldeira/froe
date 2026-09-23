// Package repo builds the context a model sees about a project: which files
// exist, what they define, and any project instructions.
//
// This is the answer to docs/DECISIONS.md D4 — on CPU-bound hardware prefill
// dominates, and one 2000-line file is ~25k tokens and minutes of silence. The
// model gets a structural map and asks for the files it actually needs.
package repo

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Symbol is one declaration found in a file.
type Symbol struct {
	Kind string // func, type, class, const, var
	Name string
	Line int
}

// langRules are the extraction patterns for one language.
//
// These are REGEXES, not a parser. A tree-sitter grammar per language would be
// more accurate, but it needs cgo, which forfeits the single static binary and
// trivial cross-compilation that D1 chose Go for. The map is a hint that helps
// the model pick files to read - a missed symbol costs one extra read_file,
// where a cgo toolchain costs every build on every machine. Revisit if the
// misses prove expensive in practice.
type langRules struct {
	name  string
	rules []symbolRule
}

type symbolRule struct {
	kind string
	re   *regexp.Regexp
	// group is the capture index holding the symbol name.
	group int
}

var languages = map[string]langRules{
	".go": {"go", []symbolRule{
		{"func", regexp.MustCompile(`^func\s+(?:\([^)]*\)\s+)?([A-Za-z_]\w*)`), 1},
		{"type", regexp.MustCompile(`^type\s+([A-Za-z_]\w*)`), 1},
		{"const", regexp.MustCompile(`^const\s+([A-Za-z_]\w*)`), 1},
		{"var", regexp.MustCompile(`^var\s+([A-Za-z_]\w*)`), 1},
	}},
	".py": {"python", []symbolRule{
		{"class", regexp.MustCompile(`^class\s+([A-Za-z_]\w*)`), 1},
		{"func", regexp.MustCompile(`^(?:async\s+)?def\s+([A-Za-z_]\w*)`), 1},
		// Methods are indented; keeping them distinct avoids burying the class.
		{"method", regexp.MustCompile(`^\s{1,8}(?:async\s+)?def\s+([A-Za-z_]\w*)`), 1},
	}},
	".dart": {"dart", []symbolRule{
		{"class", regexp.MustCompile(`^(?:abstract\s+|sealed\s+|final\s+)?class\s+([A-Za-z_]\w*)`), 1},
		{"mixin", regexp.MustCompile(`^mixin\s+([A-Za-z_]\w*)`), 1},
		{"enum", regexp.MustCompile(`^enum\s+([A-Za-z_]\w*)`), 1},
		{"func", regexp.MustCompile(`^(?:[A-Za-z_][\w<>,\s\[\]?]*\s+)?([A-Za-z_]\w*)\s*\([^;]*\)\s*(?:async\s*)?\{`), 1},
	}},
	".ts":  tsRules("typescript"),
	".js":  tsRules("javascript"),
	".tsx": tsRules("typescript"),
	".jsx": tsRules("javascript"),
	".rs": {"rust", []symbolRule{
		{"func", regexp.MustCompile(`^(?:pub\s+)?(?:async\s+)?fn\s+([A-Za-z_]\w*)`), 1},
		{"struct", regexp.MustCompile(`^(?:pub\s+)?struct\s+([A-Za-z_]\w*)`), 1},
		{"enum", regexp.MustCompile(`^(?:pub\s+)?enum\s+([A-Za-z_]\w*)`), 1},
		{"trait", regexp.MustCompile(`^(?:pub\s+)?trait\s+([A-Za-z_]\w*)`), 1},
	}},
	".sh": {"shell", []symbolRule{
		{"func", regexp.MustCompile(`^(?:function\s+)?([A-Za-z_]\w*)\s*\(\)\s*\{`), 1},
	}},
	".sql": {"sql", []symbolRule{
		{"table", regexp.MustCompile(`(?i)^create\s+table\s+(?:if\s+not\s+exists\s+)?` + "`?" + `([A-Za-z_]\w*)`), 1},
	}},
}

func tsRules(name string) langRules {
	return langRules{name, []symbolRule{
		{"class", regexp.MustCompile(`^(?:export\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][\w$]*)`), 1},
		{"func", regexp.MustCompile(`^(?:export\s+)?(?:async\s+)?function\s+\*?([A-Za-z_$][\w$]*)`), 1},
		{"const", regexp.MustCompile(`^(?:export\s+)?(?:const|let)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?\(`), 1},
		{"interface", regexp.MustCompile(`^(?:export\s+)?interface\s+([A-Za-z_$][\w$]*)`), 1},
		{"type", regexp.MustCompile(`^(?:export\s+)?type\s+([A-Za-z_$][\w$]*)`), 1},
	}}
}

// Supported reports whether a file extension yields symbols.
func Supported(path string) bool {
	_, ok := languages[strings.ToLower(filepath.Ext(path))]
	return ok
}

// Language returns the human name for a path's language, or "".
func Language(path string) string {
	if l, ok := languages[strings.ToLower(filepath.Ext(path))]; ok {
		return l.name
	}
	return ""
}

// maxSymbolsPerFile caps how much any single file contributes. A generated file
// with 900 declarations must not crowd out the rest of the repository.
const maxSymbolsPerFile = 40

// ExtractSymbols pulls top-level declarations from source text.
func ExtractSymbols(path, content string) []Symbol {
	lang, ok := languages[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return nil
	}

	var out []Symbol
	seen := map[string]bool{}
	for i, line := range strings.Split(content, "\n") {
		if len(out) >= maxSymbolsPerFile {
			break
		}
		// Comment lines produce phantom symbols in every language here.
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		for _, r := range lang.rules {
			m := r.re.FindStringSubmatch(line)
			if m == nil || len(m) <= r.group {
				continue
			}
			name := m[r.group]
			// Control-flow keywords match the Dart/TS function patterns.
			if isKeyword(name) {
				continue
			}
			key := r.kind + " " + name
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Symbol{Kind: r.kind, Name: name, Line: i + 1})
			break
		}
	}
	return out
}

// isKeyword filters control-flow words that the looser function patterns match.
var keywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"return": true, "else": true, "do": true, "try": true, "with": true,
}

func isKeyword(s string) bool { return keywords[s] }
