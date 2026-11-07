// Command froe is a local-first agentic coding CLI: the terminal client, and
// the binary the Neovim plugin drives over JSON-RPC (`froe rpc`).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"golang.org/x/term"

	"github.com/dcoldeira/froe/internal/doctor"
	"github.com/dcoldeira/froe/internal/resolve"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `froe - local-first agentic coding CLI

usage:
  froe [flags]                interactive session with memory (same as froe chat)
  froe chat [flags]           interactive session with memory
  froe do  [flags] <task>     run the agent loop with tools
  froe locate [flags] <issue> find WHERE in the code something lives (read-only)
  froe ask [flags] <prompt>   ask a model a single question
  froe map [flags]            print the project map the agent sees
  froe sessions [flags]       list or replay past conversations
  froe memory [flags]         durable facts about this project
  froe commit [flags]         write a commit message from the staged diff
  froe doctor [--json]        report what this machine can actually run
  froe rpc                    JSON-RPC over stdio (used by the Neovim plugin)
  froe version

run "froe do -h" or "froe ask -h" for flags.

docs: https://github.com/dcoldeira/froe
`

func main() {
	// A runtime froe starts can take a while (a cold colibri reads its model
	// from disk); say so instead of sitting silent. In Neovim, stderr shows as
	// a notification.
	resolve.Starting = func(runtime, model string, argv []string) {
		fmt.Fprintf(os.Stderr, "froe: %s is not running - starting it for %s (%s); a cold start can take a minute\n",
			runtime, model, argv[0])
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "froe:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	tty := term.IsTerminal(int(os.Stdin.Fd()))
	if len(args) == 0 && !tty {
		fmt.Print(usage)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if chatByDefault(args, tty) {
		return runChat(ctx, args)
	}

	switch args[0] {
	case "rpc", "--rpc":
		return runRPC(ctx, args[1:])
	case "chat":
		return runChat(ctx, args[1:])
	case "sessions":
		return runSessions(ctx, args[1:])
	case "memory":
		return runMemory(ctx, args[1:])
	case "do":
		return runDo(ctx, args[1:])
	case "commit":
		return runCommit(ctx, args[1:])
	case "locate":
		return runLocate(ctx, args[1:])
	case "ask":
		return runAsk(ctx, args[1:])
	case "map":
		return runMap(ctx, args[1:])
	case "doctor":
		return runDoctor(ctx, args[1:])
	case "version", "--version", "-v":
		fmt.Println("froe", version)
		return nil
	case "help", "--help", "-h":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

// chatByDefault reports whether the command line means "open chat": bare
// `froe` in a terminal, or `froe` followed straight by chat's flags
// (`froe -resume last`, `froe -no-context`), the way `claude` opens its
// session. The flags that mean something of their own (help, version, rpc)
// keep it.
func chatByDefault(args []string, tty bool) bool {
	if len(args) == 0 {
		return tty
	}
	switch args[0] {
	case "-h", "--help", "-v", "--version", "--rpc":
		return false
	}
	return strings.HasPrefix(args[0], "-")
}

func runDoctor(ctx context.Context, args []string) error {
	asJSON := len(args) > 0 && args[0] == "--json"

	rep, err := doctor.Run(ctx)
	if err != nil {
		return err
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	doctor.Render(os.Stdout, rep)
	return nil
}
