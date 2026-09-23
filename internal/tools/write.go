package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Write creates or replaces a file.
type Write struct{}

func (Write) Name() string   { return "write_file" }
func (Write) Mutating() bool { return true }
func (Write) Description() string {
	return "Create a file or replace its entire contents. " +
		"To change part of an existing file use edit_file instead - it is safer and cheaper."
}

func (Write) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":    {"type": "string", "description": "Path relative to the project root"},
    "content": {"type": "string", "description": "Full file contents"}
  },
  "required": ["path", "content"]
}`)
}

func (Write) Run(ctx context.Context, args json.RawMessage, env Env) (string, error) {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	abs, err := resolve(env, a.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, []byte(a.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d bytes)", rel(env, abs), len(a.Content)), nil
}
