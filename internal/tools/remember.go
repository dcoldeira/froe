package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// maxMemoryLength bounds one fact. Memory is for durable, reusable knowledge,
// not for pasting a file into the database.
const maxMemoryLength = 500

// Remember stores a durable fact about the project.
//
// The point is knowledge that is expensive to rediscover and cheap to state:
// "CSS tests use the ramp field, not stage", "this repo indents with tabs".
// Facts the code already states plainly are not worth remembering — the agent
// can read those — so the description steers away from them.
type Remember struct{}

func (Remember) Name() string { return "remember" }

// Mutating is true: this writes to a store that outlives the session, and the
// user should get a say in what their agent decides to believe permanently.
func (Remember) Mutating() bool { return true }

func (Remember) Description() string {
	return "Save a durable fact about this project for future sessions. " +
		"Use it for non-obvious knowledge that was expensive to work out - conventions, gotchas, " +
		"where things live. Do NOT use it for things readable from the code, or for anything " +
		"specific to the current task."
}

func (Remember) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "fact": {"type": "string", "description": "One self-contained sentence. It must make sense with no other context."}
  },
  "required": ["fact"]
}`)
}

func (Remember) Run(ctx context.Context, args json.RawMessage, env Env) (string, error) {
	var a struct {
		Fact string `json:"fact"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	fact := strings.TrimSpace(a.Fact)
	if fact == "" {
		return "", fmt.Errorf("fact is required")
	}
	if len(fact) > maxMemoryLength {
		return "", fmt.Errorf("fact is %d characters; keep it under %d and state one thing",
			len(fact), maxMemoryLength)
	}
	if env.Memory == nil {
		return "", fmt.Errorf("memory is disabled for this run, so nothing was saved")
	}
	if err := env.Memory.Remember(env.ProjectKey(), fact, "agent"); err != nil {
		return "", err
	}
	return "remembered: " + fact, nil
}
