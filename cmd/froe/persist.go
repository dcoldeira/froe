package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/dcoldeira/froe/internal/agent"
	"github.com/dcoldeira/froe/internal/provider"
	"github.com/dcoldeira/froe/internal/session"
)

// openStore opens the session database, tolerating failure.
//
// Persistence is a convenience, not a prerequisite: a read-only home directory
// or a locked database should degrade to a stateless run with a warning, never
// stop the user working.
func openStore(quiet bool) *session.Store {
	st, err := session.Open("")
	if err != nil {
		if !quiet {
			fmt.Fprintf(os.Stderr, "  (memory disabled: %v)\n", err)
		}
		return nil
	}
	return st
}

// toStored converts provider messages for persistence.
func toStored(msgs []provider.Message) []session.StoredMessage {
	out := make([]session.StoredMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, session.StoredMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCalls:  session.EncodeToolCalls(m.ToolCalls),
			ToolCallID: m.ToolCallID,
		})
	}
	return out
}

// fromStored rebuilds provider messages from persisted history.
func fromStored(msgs []session.StoredMessage) []provider.Message {
	out := make([]provider.Message, 0, len(msgs))
	for _, m := range msgs {
		pm := provider.Message{
			Role:       provider.Role(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		if m.ToolCalls != "" {
			var calls []provider.ToolCall
			if err := json.Unmarshal([]byte(m.ToolCalls), &calls); err == nil {
				pm.ToolCalls = calls
			}
		}
		out = append(out, pm)
	}
	return out
}

// saveRun persists a run's transcript and metrics.
func saveRun(st *session.Store, sess *session.Session, a *agent.Agent, m *agent.Metrics, model string) {
	if st == nil || sess == nil {
		return
	}
	if msgs := a.Transcript(); len(msgs) > 0 {
		if err := st.AppendMessages(sess.ID, toStored(msgs)); err != nil {
			fmt.Fprintf(os.Stderr, "  (could not save conversation: %v)\n", err)
		}
	}
	if m != nil {
		_ = st.SaveRun(session.RunRecord{
			SessionID: sess.ID, Model: model,
			Turns: m.Turns, ToolCalls: m.ToolCalls, ToolErrors: m.ToolErrors,
			PromptTokens: m.PromptTokens, CompletionTokens: m.CompletionTokens,
			ReasoningTokens: m.ReasoningTokens, Elapsed: m.Elapsed,
		})
	}
}

// trimHistory bounds replayed context.
//
// A long conversation eventually exceeds the window, and on a 32K local model
// that happens quickly. Dropping the OLDEST turns keeps the recent exchange,
// which is what the next turn actually depends on.
func trimHistory(msgs []provider.Message, maxChars int) []provider.Message {
	total := 0
	cut := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		total += len(msgs[i].Content) + len(msgs[i].ToolCallID)
		for _, tc := range msgs[i].ToolCalls {
			total += len(tc.Arguments) + len(tc.Name)
		}
		if total > maxChars {
			cut = i + 1
			break
		}
	}
	if cut == 0 {
		return msgs
	}
	// Never begin history with an orphaned tool result: it references a call
	// the model can no longer see, and some backends reject it outright.
	for cut < len(msgs) && msgs[cut].Role == provider.RoleTool {
		cut++
	}
	return msgs[cut:]
}
