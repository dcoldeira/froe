package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dcoldeira/froe/internal/config"
	"github.com/dcoldeira/froe/internal/provider"
	"github.com/dcoldeira/froe/internal/registry"
	"github.com/dcoldeira/froe/internal/resolve"
)

// runAsk is a single model turn: no tools, no agent loop. That arrives in
// Phase 3. This exists to prove the provider layer end to end.
//
// stdout carries the answer and nothing else, so `froe ask ... | pbcopy`
// works. Everything diagnostic goes to stderr.
func runAsk(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		model     = fs.String("model", "", "model id (default: best available by role)")
		maxTokens = fs.Int("max-tokens", 1024, "cap on generated tokens")
		temp      = fs.Float64("temp", 0.2, "sampling temperature")
		think     = fs.Int("think", -1, "thinking token budget; -1 uses the model's registry default, 0 asks for no reasoning")
		system    = fs.String("system", "", "system prompt")
		reasoning = fs.Bool("reasoning", false, "show the model's hidden reasoning")
		quiet     = fs.Bool("quiet", false, "suppress the metrics line")
	)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: froe ask [flags] <prompt>\n\nflags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		// Allow piping: `cat bug.txt | froe ask`
		if stat, err := os.Stdin.Stat(); err == nil && stat.Mode()&os.ModeCharDevice == 0 {
			b, _ := io.ReadAll(os.Stdin)
			prompt = strings.TrimSpace(string(b))
		}
	}
	if prompt == "" {
		fs.Usage()
		return errors.New("no prompt given")
	}

	cat, err := registry.Load(config.Dir())
	if err != nil {
		return err
	}
	choice, err := resolve.Pick(ctx, cat, *model)
	if err != nil {
		return err
	}
	p, err := provider.New(choice.Runtime, choice.Model)
	if err != nil {
		return err
	}

	msgs := make([]provider.Message, 0, 2)
	if *system != "" {
		msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: *system})
	}
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: prompt})

	req := provider.RequestFor(choice.Model, msgs)
	req.MaxTokens = *maxTokens
	req.Temperature = *temp
	if *think >= 0 {
		budget := *think
		req.ThinkingBudget = &budget
	}

	st := newStyle(os.Stderr)
	if !*quiet {
		fmt.Fprintf(os.Stderr, "%s %s via %s\n",
			st.dim("→"), st.bold(choice.Model.ID), st.dim(choice.Runtime.Name))
	}

	events, err := p.Chat(ctx, req)
	if err != nil {
		return err
	}
	return drain(ctx, events, st, *reasoning, *quiet)
}

// drain renders the event stream.
func drain(ctx context.Context, events <-chan provider.Event, st style, showReasoning, quiet bool) error {
	var (
		reasoningChunks int
		wroteAnswer     bool
	)

	for {
		select {
		case <-ctx.Done():
			// Ctrl-C: the HTTP request is already aborted by the cancelled ctx.
			fmt.Fprintln(os.Stderr, st.dim("\ncancelled"))
			return nil

		case ev, ok := <-events:
			if !ok {
				if wroteAnswer {
					fmt.Println()
				}
				return nil
			}

			switch ev.Kind {
			case provider.KindText:
				if reasoningChunks > 0 && !showReasoning && !wroteAnswer && st.on {
					// Clear the live thinking indicator before the answer lands.
					fmt.Fprint(os.Stderr, "\r\033[K")
				}
				wroteAnswer = true
				fmt.Print(ev.Text)

			case provider.KindReasoning:
				reasoningChunks++
				if showReasoning {
					fmt.Fprint(os.Stderr, st.dim(ev.Text))
				} else if !quiet && st.on {
					// Only on a terminal: \r overwrites in place there, but in a
					// pipe or log it just repeats the line hundreds of times.
					fmt.Fprintf(os.Stderr, "\r%s", st.dim(fmt.Sprintf("thinking… %d chunks", reasoningChunks)))
				}

			case provider.KindToolCall:
				// No agent loop yet; surfacing it beats dropping it silently.
				fmt.Fprintf(os.Stderr, "%s %s(%s)\n",
					st.yellow("tool call:"), ev.ToolCall.Name, ev.ToolCall.Arguments)

			case provider.KindError:
				if wroteAnswer {
					fmt.Println()
				}
				return ev.Err

			case provider.KindDone:
				if wroteAnswer {
					fmt.Println()
				}
				if !quiet && ev.Metrics != nil {
					fmt.Fprintln(os.Stderr, metricsLine(st, *ev.Metrics))
				}
				return nil
			}
		}
	}
}

// metricsLine formats the per-call timing record. Estimated token counts are
// marked, never presented as measured.
func metricsLine(st style, m provider.Metrics) string {
	var b strings.Builder
	b.WriteString(st.dim("  "))

	if m.PromptTokens > 0 || m.CompletionTokens > 0 {
		tokens := fmt.Sprintf("%d→%d tokens", m.PromptTokens, m.CompletionTokens)
		if m.ReasoningTokens > 0 {
			tokens += fmt.Sprintf(" (%d reasoning)", m.ReasoningTokens)
		}
		if m.Estimated {
			tokens += " ~estimated"
		}
		b.WriteString(st.dim(tokens + " · "))
	}
	if gen := m.GenTokensPerSec(); gen > 0 {
		b.WriteString(st.dim(fmt.Sprintf("%.1f tok/s · ", gen)))
	}
	if m.TTFT > 0 {
		b.WriteString(st.dim(fmt.Sprintf("ttft %.1fs · ", m.TTFT.Seconds())))
	}
	b.WriteString(st.dim(fmt.Sprintf("total %.1fs", m.Total.Seconds())))
	return b.String()
}
