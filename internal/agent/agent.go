// Package agent runs the plan → tool → observe loop.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dcoldeira/froe/internal/perms"
	"github.com/dcoldeira/froe/internal/provider"
	"github.com/dcoldeira/froe/internal/registry"
	"github.com/dcoldeira/froe/internal/tools"
)

// DefaultMaxTurns bounds the loop. A model that has not finished in this many
// tool calls is usually stuck in a cycle, and on local hardware every wasted
// turn costs real minutes.
const DefaultMaxTurns = 25

// maxIdenticalCalls is how often the same tool call may repeat before the run
// is abandoned.
//
// Grammar-constrained decoding guarantees a WELL-FORMED call, not a SENSIBLE
// one. Measured 2026-09-11: Qwen2.5-Coder-1.5B emitted valid JSON five times in
// a row for a file that did not exist, never adapting to the error. Without this
// the loop burns every remaining turn — minutes, on local hardware — repeating a
// call that has already failed.
const maxIdenticalCalls = 3

// maxExploredEntries bounds the "already explored" reminder so it stays a
// quick scan rather than becoming the thing that pushes real content out of
// the context budget. Oldest entries drop first - what got investigated an
// hour of turns ago matters less than what just happened.
const maxExploredEntries = 12

// callAttempt records how many times an identical tool call has recurred and
// whether the most recent one failed, so the stuck-loop message can say what
// actually happened instead of assuming failure.
type callAttempt struct {
	count  int
	failed bool
}

// defaultToolResultShare is the fraction of the context window any single tool
// result may occupy.
//
// Without a cap, one read_file can end the session: measured 2026-09-11, a
// 234 KB STATUS.md came back as ~78k tokens against an 8192-token window and
// the next request failed at 83433. The cap belongs HERE rather than in each
// tool, because grep, bash and git_log can all produce unbounded output and
// only the agent knows the window it has to fit in.
const defaultToolResultShare = 4 // one quarter

// fruitlessLimit scales the dead-end allowance to the turn budget.
//
// A flat 5 is right for a 25-turn run and far too lax for a short one:
// measured 2026-09-15, `froe locate` (6 turns) spent turns 1-4 globbing for a
// path named in the issue that does not exist — four dead ends inside a
// five-dead-end allowance, so nothing stopped it — and had two turns left for
// the actual work. It answered correctly but found one of three sites.
//
// A third of the budget, never fewer than two: one miss is ordinary, two is a
// pattern, and on a short command that is already most of the run.
func (a *Agent) fruitlessLimit(maxTurns int) int {
	n := maxTurns / 3
	if n < 2 {
		n = 2
	}
	if n > maxFruitlessSearches {
		n = maxFruitlessSearches
	}
	return n
}

// noMatchKey groups every fruitless result from one tool under a single
// counter. A "nothing found" message quotes the pattern that failed, so keying
// on the text itself can never group two differently-worded misses.
func noMatchKey(tool string) string { return tool + "\x00\x00no-match" }

// maxFruitlessSearches is the CEILING on how many times a search tool may find
// NOTHING before the run is abandoned; fruitlessLimit scales it down for short
// budgets. Deliberately looser than maxIdenticalCalls: trying a
// few patterns that miss is ordinary exploration, so punishing it at 3 would
// repeat the 2026-09-12 overcorrection. Five consecutive dead ends is not
// exploration - measured on the third issue-657 run, which reworded a glob
// for a nonexistent path across a dozen turns and never stopped.
const maxFruitlessSearches = 5

// contextReserveShare is the fraction of the context window fitContext keeps
// free for the reply and template overhead, on top of whatever clampResult
// already trimmed off each individual result.
//
// clampResult only bounds a SINGLE tool result; it cannot see the running
// total. Three individually-capped results can still add up to more than the
// window holds. Observed 2026-09-13: a web-search task on an 8192-token
// Bonsai load crashed three tool calls in with LM Studio's chat template
// raising "No user query found in messages" - the engine, forced to fit an
// overlong prompt, had dropped the earliest messages (including the user's
// own question) before applying the template.
const contextReserveShare = 4 // one quarter

// droppedToolResult replaces a tool result fitContext has shrunk to make
// room. Distinct from clampResult's truncation notice so a message already
// shrunk once is never mistaken for one that still has content to give up.
const droppedToolResult = "[an earlier tool result was dropped here to fit the context window]"

// estimateTokens is the same rough chars-per-token conversion clampResult
// uses (internal/agent/agent.go's clampResult assumes ~3 chars/token). Good
// enough for a budget; not a real tokenizer.
func estimateTokens(s string) int { return len(s) / 3 }

// EventKind discriminates the agent's output stream.
type EventKind int

const (
	KindText EventKind = iota
	KindReasoning
	KindToolStart
	KindToolResult
	KindToolDenied
	KindTurn
	KindDone
	KindError
)

// Event is one item of agent progress.
type Event struct {
	Kind    EventKind
	Text    string
	Tool    string
	Args    string
	Result  string
	Err     error
	Turn    int
	Metrics *Metrics
}

// Metrics aggregates a whole run, not a single call.
type Metrics struct {
	Turns            int
	ToolCalls        int
	ToolErrors       int
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
	Elapsed          time.Duration

	// FilesChanged lists every path a mutating tool wrote, in the order first
	// touched. A run that aborts still leaves those edits on disk, so the
	// caller must be able to say which files it left behind - an abort that
	// reports only "stuck" reads as if nothing happened.
	FilesChanged []string
}

// Agent ties a model, a tool set and a permission gate into a loop.
type Agent struct {
	Provider provider.Provider
	Model    registry.Model
	Tools    *tools.Registry
	Gate     perms.Asker
	Env      tools.Env
	MaxTurns int
	// System is prepended to the conversation. The react protocol description
	// is appended to it automatically when that strategy is in use.
	System string
	// Context is the project map and instructions, built once per run and
	// placed in the system message so it is prefilled once rather than
	// re-sent as a user turn each iteration.
	Context string
	// History is prior conversation, replayed so a resumed session continues
	// where it left off. The system message is rebuilt each run rather than
	// stored, so a changed repo map or new memories take effect on resume.
	History []provider.Message

	// transcript accumulates everything this run added, for persistence.
	transcript []provider.Message
	// blocked holds tools that cannot be approved in this session at all.
	blocked map[string]bool
	// MaxToolResultTokens caps a single tool result. Zero derives it from the
	// model's context window.
	MaxToolResultTokens int
}

// Transcript returns the messages this run produced, excluding the system
// message and replayed history.
func (a *Agent) Transcript() []provider.Message { return a.transcript }

// unavailable records a tool that can never be approved in this session, so
// later calls are refused immediately rather than re-asking a gate that has
// already said no.
func (a *Agent) unavailable(name string) {
	if a.blocked == nil {
		a.blocked = map[string]bool{}
	}
	a.blocked[name] = true
}

// Run executes the loop until the model stops calling tools, the turn limit is
// reached, or ctx is cancelled. The returned channel closes when the run ends.
//
// images is variadic so every existing call site (Run(ctx, task)) keeps
// compiling unchanged; pass provider.ImageContent values from
// provider.LoadImageFile to attach pictures to this turn.
func (a *Agent) Run(ctx context.Context, task string, images ...provider.ImageContent) <-chan Event {
	out := make(chan Event, 32)
	go a.run(ctx, task, images, out)
	return out
}

func (a *Agent) run(ctx context.Context, task string, images []provider.ImageContent, out chan<- Event) {
	defer close(out)

	start := time.Now()
	var m Metrics

	emit := func(e Event) bool {
		select {
		case out <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}
	fail := func(err error) {
		emit(Event{Kind: KindError, Err: err})
	}

	if len(images) > 0 && !a.Model.Vision {
		fail(fmt.Errorf("%s does not support images - it will not see %d attached file(s)", a.Model.ID, len(images)))
		return
	}

	maxTurns := a.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}
	strategy := a.strategy()
	useReact := strategy == registry.ToolReact
	useGrammar := strategy == registry.ToolGrammar

	system := a.System
	if a.Context != "" {
		system += "\n\n" + a.Context
	}
	switch {
	case useReact:
		if system != "" {
			system += "\n\n"
		}
		system += reactPrompt(a.Tools)
	case useGrammar:
		if system != "" {
			system += "\n\n"
		}
		system += grammarPrompt(a.Tools)
	}

	// Fingerprints of calls already attempted, to catch a stuck model. Tracks
	// outcome as well as count: a repeat that keeps FAILING and a repeat that
	// keeps SUCCEEDING are both stuck (the second just means the model lost
	// track and is making no progress, e.g. re-reading a file whose content
	// cannot change), but the abort message must say which actually happened
	// rather than claiming a failure that never occurred.
	attempts := map[string]callAttempt{}

	// resultRepeats catches the same non-progress by a different route:
	// arguments that differ just enough to dodge the fingerprint above, but
	// whose result is byte-identical to one already seen. Observed
	// 2026-09-13: a model relisted the same directory as "CV/*", "**/CV*" and
	// "CV*" in turn - three different fingerprints, one real answer, no
	// progress - and only got caught when it finally repeated "CV/*"
	// verbatim a fourth time. Scoped to non-mutating tools only: a bash
	// command's own output ("(no output)", exit codes) repeats constantly
	// across genuinely different, useful commands and would false-positive
	// here.
	resultRepeats := map[string]int{}

	// explored is a running, deduplicated log of successful non-mutating calls,
	// re-surfaced to the model every turn (see below) instead of relying on it
	// to notice the repeat itself by scanning its own history. The reactive
	// fingerprint/resultRepeats detectors above catch a stuck model AFTER the
	// fact; this is the proactive half - telling it what it already knows
	// before it re-asks. Keyed on the same fingerprint as `attempts` so a
	// reworded-but-identical call (different glob pattern, same directory)
	// still shows up once results land, not just once arguments match.
	var explored []string
	exploredSeen := map[string]bool{}

	// resultCache holds the real result of a non-mutating call's first
	// success, keyed by the same fingerprint. A repeat of that exact call
	// never reaches the tool (no disk/network work, and it cannot return a
	// different answer than last time) and never reaches the model as full
	// content either - measured 2026-09-15: telling the model in a SEPARATE
	// reminder message that it already made a call did not stop it repeating
	// that call verbatim. Replacing the repeat's own result with a terse
	// "nothing new" notice is a stronger signal because it arrives exactly
	// where the model is looking for new information, not off to the side.
	resultCache := map[string]string{}

	// editedSinceCheck is true while a file has been written and no command has
	// run since. A model that finishes in that state has declared the task done
	// without looking - measured 2026-09-24 on 07-multi-site: five models, five
	// failures, and not one ran the module the task said must still import. One
	// had replaced a header with "" and called it finished; a single run of the
	// module would have shown it. checkNudged keeps the push to once per run.
	editedSinceCheck, checkNudged := false, false

	a.transcript = nil

	msgs := make([]provider.Message, 0, 4+len(a.History))
	protect := map[int]bool{}
	if system != "" {
		msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: system})
		protect[0] = true
	}
	msgs = append(msgs, a.History...)

	taskIdx := len(msgs)
	userMsg := provider.Message{Role: provider.RoleUser, Content: task, Images: images}
	msgs = append(msgs, userMsg)
	protect[taskIdx] = true
	a.transcript = append(a.transcript, userMsg)

	// abort ends a run that is going nowhere. It asks for an answer BEFORE
	// reporting the failure, for the same reason the turn limit does: by the
	// time a loop is detectable the transcript usually holds what the user
	// asked for. Measured 2026-09-16, `froe locate` on the 09-locate-wrong-path
	// eval: two runs of three produced a complete, correct answer; the third
	// read the right file, repeated that read, and was aborted with nothing -
	// 89 seconds spent, the answer discarded.
	abort := func(err error) {
		a.finalAnswer(ctx, msgs, &m, emit)
		m.Elapsed = time.Since(start)
		emit(Event{Kind: KindError, Metrics: &m, Err: err})
	}

	for turn := 1; turn <= maxTurns; turn++ {
		if ctx.Err() != nil {
			return
		}
		m.Turns = turn
		if !emit(Event{Kind: KindTurn, Turn: turn}) {
			return
		}

		a.fitContext(msgs, protect)

		// Appended to a copy, never to msgs itself: it must stay at the tail
		// every turn (recency matters for a small model's attention) and must
		// never accumulate as permanent history the way a nudge message does.
		reqMsgs := msgs
		if len(explored) > 0 {
			reqMsgs = append(append([]provider.Message(nil), msgs...), provider.Message{
				Role: provider.RoleUser,
				Content: "Already explored, do not repeat unless you need a different " +
					"part of the same file/output:\n- " + strings.Join(explored, "\n- "),
			})
		}
		req := provider.RequestFor(a.Model, reqMsgs)
		req.MaxTokens = 2048
		req.Temperature = 0.2
		switch {
		case useGrammar:
			// Constrain the sampler so malformed tool calls are unreachable
			// rather than merely discouraged.
			if req.Extra == nil {
				req.Extra = map[string]any{}
			}
			req.Extra["response_format"] = grammarResponseFormat(a.Tools)
		case !useReact:
			req.Tools = a.Tools.Defs()
		}

		events, err := a.Provider.Chat(ctx, req)
		if err != nil {
			fail(err)
			return
		}

		var (
			text  strings.Builder
			calls []provider.ToolCall
		)
		for ev := range events {
			switch ev.Kind {
			case provider.KindText:
				text.WriteString(ev.Text)
				// Under the grammar strategy the streamed text IS the protocol
				// envelope, not prose. Emitting it would show the user raw JSON;
				// the answer is emitted once it has been parsed.
				if !useGrammar {
					if !emit(Event{Kind: KindText, Text: ev.Text}) {
						return
					}
				}
			case provider.KindReasoning:
				if !emit(Event{Kind: KindReasoning, Text: ev.Text}) {
					return
				}
			case provider.KindToolCall:
				calls = append(calls, *ev.ToolCall)
			case provider.KindError:
				fail(ev.Err)
				return
			case provider.KindDone:
				if ev.Metrics != nil {
					m.PromptTokens += ev.Metrics.PromptTokens
					m.CompletionTokens += ev.Metrics.CompletionTokens
					m.ReasoningTokens += ev.Metrics.ReasoningTokens
				}
			}
		}

		answer := text.String()

		// Under the grammar strategy the whole reply is one constrained object.
		if useGrammar {
			parsedAnswer, parsed, gerr := parseGrammar(answer)
			if gerr != nil {
				fail(gerr)
				return
			}
			answer, calls = parsedAnswer, parsed
			if answer != "" {
				if !emit(Event{Kind: KindText, Text: answer}) {
					return
				}
			}
		}

		// With the react strategy the calls are in the text, not the API field.
		if useReact {
			clean, parsed, perr := parseReact(answer)
			if perr != nil {
				// Feed the parse failure back. This retry loop is the whole
				// reason react is viable for a small model at all.
				m.ToolErrors++
				msgs = append(msgs,
					provider.Message{Role: provider.RoleAssistant, Content: answer},
					provider.Message{Role: provider.RoleUser, Content: "That tool block could not be parsed: " + perr.Error()})
				if !emit(Event{Kind: KindToolResult, Tool: "(parse)", Result: perr.Error()}) {
					return
				}
				continue
			}
			answer, calls = clean, parsed
		}

		if len(calls) == 0 && editedSinceCheck && !checkNudged && turn < maxTurns &&
			asksForCheck(task) && a.canRun("bash") {
			// Finished with edits nobody has run. Send it back once to run the
			// check the task itself asked for. Not on the last turn: that would
			// turn a finished run into an out-of-turns failure.
			checkNudged = true
			nudge := verifyNudge(m.FilesChanged, checkSentence(task))
			done := provider.Message{Role: provider.RoleAssistant, Content: answer}
			push := provider.Message{Role: provider.RoleUser, Content: nudge}
			msgs = append(msgs, done, push)
			a.transcript = append(a.transcript, done, push)
			if !emit(Event{Kind: KindToolResult, Tool: "(verify)", Result: nudge}) {
				return
			}
			continue
		}

		if len(calls) == 0 {
			a.transcript = append(a.transcript,
				provider.Message{Role: provider.RoleAssistant, Content: answer})
			m.Elapsed = time.Since(start)
			emit(Event{Kind: KindDone, Text: answer, Metrics: &m})
			return
		}

		assistantMsg := provider.Message{Role: provider.RoleAssistant, Content: answer, ToolCalls: calls}
		msgs = append(msgs, assistantMsg)
		a.transcript = append(a.transcript, assistantMsg)

		for _, call := range calls {
			fingerprint := call.Name + "\x00" + call.Arguments
			prior := attempts[fingerprint]

			if prior.count >= maxIdenticalCalls {
				outcome := "made no progress - each call succeeded but nothing changed"
				if prior.failed {
					outcome = "kept failing"
				}
				abort(fmt.Errorf(
					"stuck: %s was called %d times with identical arguments and %s",
					call.Name, prior.count, outcome))
				return
			} else if prior.count == 2 {
				// One nudge before giving up. Naming the repetition explicitly
				// is far more actionable than repeating the same error text,
				// and the wording must match what actually happened last time
				// - claiming a failure that did not occur just confuses a
				// model already struggling to track its own history.
				hint := fmt.Sprintf(
					"You already called %s with exactly these arguments and it succeeded. "+
						"The result will not change - stop repeating it and use what you already have.", call.Name)
				if prior.failed {
					hint = fmt.Sprintf(
						"You already called %s with exactly these arguments and it failed. "+
							"Do not repeat it. Use glob to discover the real paths first.", call.Name)
				}
				msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: hint})
			}

			var result string
			var denied bool
			if _, ok := resultCache[fingerprint]; ok && prior.count > 0 {
				emit(Event{Kind: KindToolStart, Tool: call.Name, Args: call.Arguments})
				result = fmt.Sprintf(
					"(identical to a call you already made to %s - the result is unchanged and is "+
						"not repeated here. Use what you already have, or change the arguments if you "+
						"need something different.)", call.Name)
				emit(Event{Kind: KindToolResult, Tool: call.Name, Result: result})
			} else {
				result, denied = a.runTool(ctx, call, emit)
				if ctx.Err() != nil {
					return
				}
			}
			m.ToolCalls++
			if denied {
				m.ToolErrors++
			}
			attempts[fingerprint] = callAttempt{count: prior.count + 1, failed: denied}

			if !denied {
				if t, ok := a.Tools.Get(call.Name); ok && t.Mutating() {
					m.recordChange(callPath(call.Arguments))
				}
				// Any command that ran counts as a check: a failing test is a
				// non-zero exit, which bash reports as output, not as denial.
				if call.Name == "bash" {
					editedSinceCheck = false
				} else if callPath(call.Arguments) != "" {
					if t, ok := a.Tools.Get(call.Name); ok && t.Mutating() {
						editedSinceCheck = true
					}
				}
			}

			// Only the first time this exact fingerprint is seen counts toward
			// resultRepeats - repeats of it are already handled by the cache
			// short-circuit above (with its own "identical call" wording).
			// This one exists purely for the gap that leaves: differently-
			// worded calls landing on the same output.
			if !denied && prior.count == 0 {
				if t, ok := a.Tools.Get(call.Name); ok && !t.Mutating() {
					resultCache[fingerprint] = result

					if !exploredSeen[fingerprint] {
						exploredSeen[fingerprint] = true
						explored = append(explored, exploredEntry(call))
						if len(explored) > maxExploredEntries {
							explored = explored[len(explored)-maxExploredEntries:]
						}
					}

					// A "nothing found" result quotes the pattern that failed,
					// so every reworded miss is a different string and keying
					// on the text alone can never group them. Collapse them to
					// one key, on a longer leash: probing a few wrong patterns
					// is ordinary exploration, hunting for a path that does not
					// exist is not.
					//
					// This composes with the cache short-circuit above rather
					// than overlapping it: a VERBATIM repeat never reaches the
					// tool at all now, so what still arrives here is only the
					// differently-worded miss this key was written for.
					key, limit := result, maxIdenticalCalls
					fruitless := tools.IsNoMatch(result)
					if fruitless {
						key, limit = noMatchKey(call.Name), a.fruitlessLimit(maxTurns)
					} else if n := resultRepeats[noMatchKey(call.Name)]; n > 0 {
						// A search that FOUND something breaks the streak.
						//
						// The counter spans the whole run, so without this a
						// miss either side of a hit reads as two consecutive
						// dead ends. Measured live 2026-09-16, froe locate on
						// a real production issue: glob missed, glob found seven
						// files, glob missed - and the run aborted at turn 4 of
						// 6 saying "no new information is being found", which
						// was plainly untrue.
						//
						// Forgiveness is one per find, not a reset: a model
						// genuinely lost among reworded misses still converges
						// on the limit, while one that is finding things as it
						// goes is left alone and bounded by the turn budget
						// instead.
						resultRepeats[noMatchKey(call.Name)] = n - 1
					}
					rk := key
					if !fruitless {
						rk = call.Name + "\x00" + key
					}
					resultRepeats[rk]++
					if n := resultRepeats[rk]; n >= limit {
						err := fmt.Errorf(
							"stuck: %s returned the same result %d times despite differently worded arguments - no new information is being found",
							call.Name, n)
						if fruitless {
							err = fmt.Errorf(
								"stuck: %s found nothing %d times with differently worded arguments - "+
									"what you are looking for does not exist under that name. "+
									"Work from what you have already found",
								call.Name, n)
						}
						abort(err)
						return
					}
				}
			}

			toolMsg := provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, Content: result}
			msgs = append(msgs, toolMsg)
			a.transcript = append(a.transcript, toolMsg)
		}
	}

	// Out of turns, but the transcript usually HOLDS the answer by now.
	//
	// Measured 2026-09-15: `froe locate` on a real production issue found all
	// three edit sites and the lookalike in a single grep on turn 4, then spent
	// turns 5-8 re-running searches it had already done and exited with
	// nothing. Throwing that away is the worst of both worlds - the user waited
	// for the work and got none of it.
	//
	// So spend one final call with NO tools offered, asking for an answer from
	// what is already known. The run is still reported as unfinished, because
	// it is; the difference is that whatever was found comes back with it.
	a.finalAnswer(ctx, msgs, &m, emit)

	m.Elapsed = time.Since(start)
	emit(Event{Kind: KindError, Err: &MaxTurnsError{Turns: maxTurns}, Metrics: &m})
}

// checkWords are what a task says when it expects its result to be checked:
// "run the tests", "must still build", "import cleanly". The verify push is
// gated on them because it costs a whole turn - on a slow local model, tens of
// seconds - and a task that asked for no check should not pay for one.
var checkWords = []string{"test", "build", "compile", "import", "lint", "verify", "confirm"}

// asksForCheck reports whether a task asks for its result to be checked.
func asksForCheck(task string) bool { return checkSentence(task) != "" }

// checkSentence returns the task's own sentence asking for a check, or "".
//
// The push quotes it back rather than suggesting checks in general. Measured
// 2026-09-24 on 07-multi-site: told to run "the tests, the build, or importing
// the module", qwen3-nothink:8b ran unittest discovery, then a package build,
// then pip, then sudo apt-get - three runs of three - and never once imported
// the module the task named.
func checkSentence(task string) string {
	for _, line := range strings.Split(task, "\n") {
		for _, sent := range strings.SplitAfter(line, ". ") {
			l := strings.ToLower(sent)
			for _, w := range checkWords {
				if strings.Contains(l, w) {
					return strings.TrimSpace(sent)
				}
			}
		}
	}
	return ""
}

// canRun reports whether a tool exists in this run and has not been refused
// for the session - pushing a model to run a check it cannot run wastes a turn.
func (a *Agent) canRun(name string) bool {
	_, ok := a.Tools.Get(name)
	return ok && !a.blocked[name]
}

// verifyNudge is sent when a run finishes with edits that were never run.
func verifyNudge(changed []string, check string) string {
	what := "files"
	if len(changed) > 0 {
		what = strings.Join(changed, ", ")
	}
	return fmt.Sprintf("You changed %s but ran nothing afterwards. The task says: %q "+
		"Run the one command that checks exactly that, now, with bash, and fix anything "+
		"it reports. Then give your final answer.", what, check)
}

// MaxTurnsError ends a run that spent its whole turn budget.
//
// It is a type rather than a formatted string because running out of turns is
// not always a failure: finalAnswer has already run by the time it is emitted,
// so the answer usually comes back with it. A caller that can judge its own
// output - froe locate verifies that the places it cites exist - should be
// able to exit on the quality of the answer rather than on the reason the loop
// stopped.
type MaxTurnsError struct{ Turns int }

func (e *MaxTurnsError) Error() string {
	return fmt.Sprintf("stopped after %d turns without finishing", e.Turns)
}

// finalAnswer asks for a summary with no tools available, so the model cannot
// start another search it has no budget for.
func (a *Agent) finalAnswer(ctx context.Context, msgs []provider.Message, m *Metrics, emit func(Event) bool) {
	if ctx.Err() != nil {
		return
	}
	msgs = trimDanglingToolCalls(msgs)
	msgs = append(msgs, provider.Message{
		Role: provider.RoleUser,
		Content: "You are out of tool calls. Do not ask for any more. " +
			"Answer now, in the required format, using only what you have already found. " +
			"If something is still unknown, say which part is unknown rather than guessing.",
	})

	req := provider.RequestFor(a.Model, msgs)
	req.MaxTokens = 1024
	req.Temperature = 0.2
	// No req.Tools and no grammar: the only thing it can produce is prose.

	events, err := a.Provider.Chat(ctx, req)
	if err != nil {
		return
	}
	for ev := range events {
		switch ev.Kind {
		case provider.KindText:
			if !emit(Event{Kind: KindText, Text: ev.Text}) {
				return
			}
		case provider.KindDone:
			if ev.Metrics != nil {
				m.PromptTokens += ev.Metrics.PromptTokens
				m.CompletionTokens += ev.Metrics.CompletionTokens
			}
		}
	}
}

// trimDanglingToolCalls drops a trailing assistant message whose tool calls
// never got their results.
//
// A run abandoned mid-turn leaves exactly that, and an OpenAI-compatible
// backend rejects a request where a tool_call has no matching tool message -
// which would turn the last-chance answer into a second failure. The findings
// worth answering from are in the tool results BEFORE this point anyway.
func trimDanglingToolCalls(msgs []provider.Message) []provider.Message {
	answered := map[string]bool{}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == provider.RoleTool {
			answered[msgs[i].ToolCallID] = true
			continue
		}
		if msgs[i].Role != provider.RoleAssistant || len(msgs[i].ToolCalls) == 0 {
			return msgs
		}
		for _, c := range msgs[i].ToolCalls {
			if !answered[c.ID] {
				return msgs[:i]
			}
		}
		return msgs
	}
	return msgs
}

// runTool gates and executes one call, returning the text to feed back.
//
// Errors are returned as tool output rather than aborting the run: the model
// needs to see what went wrong in order to correct itself, and a failed tool
// call is a normal part of the loop rather than an exceptional condition.
// recordChange notes a written path once, preserving first-touched order.
func (m *Metrics) recordChange(path string) {
	if path == "" {
		return
	}
	for _, p := range m.FilesChanged {
		if p == path {
			return
		}
	}
	m.FilesChanged = append(m.FilesChanged, path)
}

// callPath pulls the "path" argument out of a tool call. Every mutating tool
// names its target that way; anything else is reported as an unnamed change
// rather than guessed at.
func callPath(args string) string {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return ""
	}
	return a.Path
}

func (a *Agent) runTool(ctx context.Context, call provider.ToolCall, emit func(Event) bool) (string, bool) {
	t, ok := a.Tools.Get(call.Name)
	if !ok {
		msg := fmt.Sprintf("no such tool %q. Available: %s", call.Name, strings.Join(a.Tools.Names(), ", "))
		emit(Event{Kind: KindToolResult, Tool: call.Name, Result: msg})
		return msg, true
	}

	if a.blocked[call.Name] {
		msg := call.Name + " is unavailable for this session, as already reported. Do not call it again."
		emit(Event{Kind: KindToolDenied, Tool: call.Name, Result: msg})
		return msg, true
	}

	emit(Event{Kind: KindToolStart, Tool: call.Name, Args: call.Arguments})

	if t.Mutating() {
		switch a.Gate.Ask(perms.Request{
			Tool:    call.Name,
			Summary: summarise(call),
			Detail:  detail(call),
		}) {
		case perms.Deny:
			msg := "the user declined this action"
			emit(Event{Kind: KindToolDenied, Tool: call.Name, Result: msg})
			return msg, true
		case perms.DenyPermanently:
			// Say plainly that retrying is futile. A refusal that sounds
			// transient invites the model to try again, every turn, forever.
			msg := fmt.Sprintf("%s is UNAVAILABLE for this whole session and will never succeed. "+
				"Do not call it again. Complete the task without it, or explain what you cannot verify.", call.Name)
			emit(Event{Kind: KindToolDenied, Tool: call.Name, Result: msg})
			a.unavailable(call.Name)
			return msg, true
		}
	}

	result, err := t.Run(ctx, json.RawMessage(call.Arguments), a.Env)
	if err != nil {
		msg := "error: " + err.Error()
		emit(Event{Kind: KindToolResult, Tool: call.Name, Result: msg})
		return msg, true
	}
	result = a.clampResult(call.Name, result)
	emit(Event{Kind: KindToolResult, Tool: call.Name, Result: result})
	return result, false
}

// fitContext keeps msgs under the model's context window by shrinking the
// oldest tool results first, in place, until the total fits or nothing is
// left that can be shrunk. protect names indices that must never be touched -
// the system message and the run's own task message. Losing either is what
// broke the LM Studio template in the first place (see contextReserveShare),
// so no budget overage is worth risking them, even a small one.
func (a *Agent) fitContext(msgs []provider.Message, protect map[int]bool) {
	ctxMax := a.Model.CtxMax
	if ctxMax <= 0 {
		ctxMax = 8192
	}
	budget := ctxMax - ctxMax/contextReserveShare
	if budget <= 0 {
		return
	}

	total := 0
	for _, m := range msgs {
		total += estimateTokens(m.Content)
	}

	for total > budget {
		// Drop the BIGGEST droppable tool result, not the oldest.
		//
		// Oldest-first looks fair and is actively harmful: findings arrive
		// small and early, bulk arrives large and late. Measured 2026-09-15 on
		// `froe locate` against a real production issue — a ~100-token grep on
		// turn 4 held the whole answer (the real path and both line numbers),
		// then turns 5 and 6 read 100 and 200 lines of code. Oldest-first
		// evicted the grep to save 100 tokens while keeping thousands of tokens
		// of surrounding code, so the final answer was written without it and
		// fell back to the path in the issue — which does not exist — with an
		// invented line number.
		//
		// Biggest-first frees the most space per drop AND keeps the dense,
		// high-value results. It also needs fewer drops, so fewer rewrites of
		// the prompt prefix.
		shrink, largest := -1, 0
		for i, m := range msgs {
			if protect[i] || m.Role != provider.RoleTool || m.Content == droppedToolResult {
				continue
			}
			if n := estimateTokens(m.Content); n > largest {
				shrink, largest = i, n
			}
		}
		if shrink == -1 {
			return // nothing left to give up without breaking the invariant
		}
		total -= estimateTokens(msgs[shrink].Content) - estimateTokens(droppedToolResult)
		msgs[shrink].Content = droppedToolResult
	}
}

// exploredEntry formats a tool call for the "already explored" reminder.
// Raw JSON args are shown as-is - condensed enough for a path or a glob
// pattern, the common case, without a bespoke pretty-printer per tool.
func exploredEntry(call provider.ToolCall) string {
	args := strings.TrimSpace(call.Arguments)
	if args == "" || args == "{}" {
		return call.Name
	}
	const maxArgsLen = 120
	if len(args) > maxArgsLen {
		args = args[:maxArgsLen] + "…"
	}
	return call.Name + " " + args
}

// clampResult bounds a tool result to the context window.
//
// The truncation is REPORTED to the model, and says how to get the rest. A
// silently shortened file is worse than a refused one: the model believes it
// has read the whole thing and reasons from a fragment.
func (a *Agent) clampResult(tool, result string) string {
	limit := a.MaxToolResultTokens
	if limit <= 0 {
		ctxMax := a.Model.CtxMax
		if ctxMax <= 0 {
			ctxMax = 8192
		}
		limit = ctxMax / defaultToolResultShare
	}

	maxChars := limit * 3
	if len(result) <= maxChars {
		return result
	}

	// Cut on a line boundary so the model never sees half a line of code.
	cut := strings.LastIndex(result[:maxChars], "\n")
	if cut < maxChars/2 {
		cut = maxChars
	}
	return result[:cut] + fmt.Sprintf(
		"\n\n[%s output truncated: %d of %d bytes shown, the rest exceeds the context window. "+
			"Narrow the request - use offset/limit on read_file, a tighter grep pattern, or a smaller range.]",
		tool, cut, len(result))
}

// strategy returns the tool-calling strategy for this model, falling back to
// react when the registry says nothing — the assumption that costs the least
// when wrong.
func (a *Agent) strategy() registry.ToolStrategy {
	switch a.Model.ToolStrategy {
	case registry.ToolNative, registry.ToolGrammar:
		return a.Model.ToolStrategy
	default:
		return registry.ToolReact
	}
}

// summarise renders a one-line description of a pending call for the prompt.
func summarise(call provider.ToolCall) string {
	var a map[string]any
	_ = json.Unmarshal([]byte(call.Arguments), &a)
	switch call.Name {
	case "bash":
		return fmt.Sprintf("run: %v", a["command"])
	case "write_file":
		return fmt.Sprintf("write %v", a["path"])
	case "edit_file":
		return fmt.Sprintf("edit %v", a["path"])
	default:
		return call.Name
	}
}

// detail renders what will actually happen, so approval is informed.
func detail(call provider.ToolCall) string {
	var a map[string]any
	_ = json.Unmarshal([]byte(call.Arguments), &a)
	switch call.Name {
	case "bash":
		if c, ok := a["command"].(string); ok {
			return c
		}
	case "edit_file":
		old, _ := a["old_string"].(string)
		neu, _ := a["new_string"].(string)
		return diff(old, neu)
	case "write_file":
		if c, ok := a["content"].(string); ok {
			return preview(c, 20)
		}
	}
	return ""
}

// diff renders a minimal -/+ view. Not a real diff algorithm: edit_file
// replaces one exact string, so showing both sides is the whole change.
func diff(old, neu string) string {
	var b strings.Builder
	for _, l := range strings.Split(preview(old, 10), "\n") {
		b.WriteString("- " + l + "\n")
	}
	for _, l := range strings.Split(preview(neu, 10), "\n") {
		b.WriteString("+ " + l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func preview(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n… %d more lines", len(lines)-maxLines)
}
