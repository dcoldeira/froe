package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// bashTimeout bounds a command. A hung command otherwise blocks the agent loop
// indefinitely with no way for the model to recover.
const bashTimeout = 2 * time.Minute

// maxBashOutput bounds captured output in bytes.
const maxBashOutput = 32 * 1024

// deniedPatterns are refused regardless of configuration.
//
// This is the hard denylist from docs/ARCHITECTURE.md §6. It is deliberately
// short: it covers commands that are catastrophic and irreversible, not
// commands that are merely risky. Everything else is the permission layer's
// job. A long denylist creates false confidence — this one exists so that a
// confused model cannot destroy the machine in a single call.
var deniedPatterns = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`\brm\s+(-[a-zA-Z]*\s+)*-?[a-zA-Z]*[rf][a-zA-Z]*\s+(-[a-zA-Z]+\s+)*/(\s|$)`),
		"recursive delete of /"},
	{regexp.MustCompile(`\brm\s+-[a-zA-Z]*r[a-zA-Z]*f|\brm\s+-[a-zA-Z]*f[a-zA-Z]*r`),
		"rm -rf (use a specific path with write_file or edit_file instead)"},
	{regexp.MustCompile(`\bgit\s+push\b.*(--force|-f)\b.*\b(main|master)\b|\bgit\s+push\b.*\b(main|master)\b.*(--force|-f)\b`),
		"force-push to a default branch"},
	{regexp.MustCompile(`\bmkfs\b|\bdd\s+.*of=/dev/|>\s*/dev/[sh]d[a-z]`),
		"raw device write"},
	{regexp.MustCompile(`\b(shutdown|reboot|halt|poweroff)\b`),
		"shutting down the machine"},
	{regexp.MustCompile(`:\(\)\s*\{.*\|.*&.*\}\s*;`),
		"fork bomb"},
	{regexp.MustCompile(`\bcurl\b[^|]*\|\s*(ba)?sh|\bwget\b[^|]*\|\s*(ba)?sh`),
		"piping a download straight into a shell"},
}

// Bash runs a shell command in the project root.
type Bash struct{}

func (Bash) Name() string   { return "bash" }
func (Bash) Mutating() bool { return true }
func (Bash) Description() string {
	return "Run a shell command in the project root. Use for builds, tests, git and other tooling. " +
		"Prefer grep and glob for searching - they are faster and cheaper than shelling out."
}

func (Bash) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "Shell command to run"}
  },
  "required": ["command"]
}`)
}

// Denied reports whether a command is refused outright, and why.
// Exported so the permission layer can explain a refusal before prompting.
func Denied(command string) (string, bool) {
	normalised := strings.Join(strings.Fields(command), " ")
	for _, d := range deniedPatterns {
		if d.re.MatchString(normalised) {
			return d.reason, true
		}
	}
	return "", false
}

func (Bash) Run(ctx context.Context, args json.RawMessage, env Env) (string, error) {
	var a struct {
		Command string `json:"command"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	if reason, denied := Denied(a.Command); denied {
		return "", fmt.Errorf("refused: %s", reason)
	}

	root, err := resolve(env, ".")
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, bashTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", a.Command)
	cmd.Dir = root

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()

	text := out.String()
	if len(text) > maxBashOutput {
		text = text[:maxBashOutput] + fmt.Sprintf("\n(output truncated at %d bytes)", maxBashOutput)
	}
	text = strings.TrimRight(text, "\n")

	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("command timed out after %s\n%s", bashTimeout, text)
	}
	if runErr != nil {
		// A non-zero exit is information the model needs, not a tool failure:
		// returning it as output lets the agent react to a failing test.
		if ee, ok := runErr.(*exec.ExitError); ok {
			if text == "" {
				text = "(no output)"
			}
			return fmt.Sprintf("exit status %d\n%s", ee.ExitCode(), text), nil
		}
		return "", runErr
	}
	if text == "" {
		return "(no output)", nil
	}
	return text, nil
}
