package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// maxReadBytes bounds a single read as a backstop; the agent applies the real,
// context-aware cap. 256KB was the original value and was far too generous —
// a 234KB file is ~78k tokens, which overflows any local model's window on its
// own. 48KB is roughly 16k tokens: still large, but survivable.
const maxReadBytes = 48 * 1024

// Read returns the contents of a file, optionally a line range.
type Read struct{}

func (Read) Name() string   { return "read_file" }
func (Read) Mutating() bool { return false }
func (Read) Description() string {
	return "Read a file from the project. Returns numbered lines. " +
		"Prefer a line range for large files - reading a whole file can exhaust the context window."
}

func (Read) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":   {"type": "string", "description": "Path relative to the project root"},
    "offset": {"type": "integer", "minimum": 1, "maximum": 1000000, "description": "First line to read, 1-based"},
    "limit":  {"type": "integer", "minimum": 1, "maximum": 5000, "description": "Maximum number of lines"}
  },
  "required": ["path"]
}`)
}

func (Read) Run(ctx context.Context, args json.RawMessage, env Env) (string, error) {
	var a struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	abs, err := resolve(env, a.Path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory; use glob to list it", a.Path)
	}

	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if a.Offset < 1 {
		a.Offset = 1
	}

	var (
		b       strings.Builder
		lineNo  int
		emitted int
		bytes   int
		trunc   bool
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lineNo++
		if lineNo < a.Offset {
			continue
		}
		if a.Limit > 0 && emitted >= a.Limit {
			trunc = true
			break
		}
		line := sc.Text()
		bytes += len(line) + 1
		if bytes > maxReadBytes {
			trunc = true
			break
		}
		// The separator must not be a tab. With "%6d\t%s" a tab-indented line
		// renders as "     9\t\treturn x" and the model cannot tell which tabs
		// belong to the gutter and which to the code — measured, it retried an
		// edit with two tabs where the file had one, twice, and gave up. A
		// vertical bar is unambiguous: everything after it is the file, byte
		// for byte.
		fmt.Fprintf(&b, "%6d|%s\n", lineNo, line)
		emitted++
	}
	if err := sc.Err(); err != nil {
		return "", err
	}

	if emitted == 0 {
		return fmt.Sprintf("(%s is empty or offset %d is past the end)", rel(env, abs), a.Offset), nil
	}
	if trunc {
		// Telling the model where it stopped lets it continue deliberately
		// rather than assume it saw the whole file.
		fmt.Fprintf(&b, "\n(truncated at line %d - call read_file again with offset=%d)", lineNo, lineNo)
	}
	return b.String(), nil
}
