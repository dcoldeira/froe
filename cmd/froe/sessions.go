package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dcoldeira/froe/internal/repo"
)

func runSessions(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		all   = fs.Bool("all", false, "list sessions from every project")
		limit = fs.Int("limit", 15, "how many to show")
		show  = fs.String("show", "", "print the transcript of a session id")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	store := openStore(false)
	if store == nil {
		return errors.New("no session database")
	}
	defer store.Close()

	st := newStyle(os.Stdout)

	if *show != "" {
		msgs, err := store.Messages(*show)
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return fmt.Errorf("session %q has no messages", *show)
		}
		for _, m := range msgs {
			label := st.bold(m.Role)
			body := strings.TrimSpace(m.Content)
			if m.ToolCalls != "" {
				body += " " + st.dim(m.ToolCalls)
			}
			if body == "" {
				continue
			}
			fmt.Printf("%s: %s\n\n", label, body)
		}
		return nil
	}

	project := ""
	if !*all {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		project = repo.FindRoot(cwd)
	}

	list, err := store.List(project, *limit)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(os.Stderr, st.dim("  no sessions yet — run `froe chat` or `froe do`"))
		return nil
	}

	for _, s := range list {
		title := s.Title
		if title == "" {
			title = st.dim("(untitled)")
		}
		fmt.Printf("%s  %s  %s  %s\n",
			s.ID, st.dim(humanAge(s.UpdatedAt)),
			st.dim(fmt.Sprintf("%2d msgs", s.Messages)), title)
		if *all {
			fmt.Printf("    %s\n", st.dim(s.Project))
		}
	}
	fmt.Fprintf(os.Stderr, "\n%s\n", st.dim("  resume with: froe chat -resume <id>   (or -resume last)"))
	return nil
}

func runMemory(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("memory", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		add    = fs.String("add", "", "remember a fact about this project")
		forget = fs.Int64("forget", 0, "delete a memory by id")
		all    = fs.Bool("all", false, "show memories from every project")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	store := openStore(false)
	if store == nil {
		return errors.New("no session database")
	}
	defer store.Close()

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root := repo.FindRoot(cwd)
	st := newStyle(os.Stdout)

	switch {
	case *add != "":
		if err := store.Remember(root, *add, "user"); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, st.dim("  remembered"))
		return nil
	case *forget > 0:
		if err := store.Forget(*forget); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, st.dim("  forgotten"))
		return nil
	}

	project := root
	if *all {
		project = ""
	}
	mems, err := store.Memories(project, 200)
	if err != nil {
		return err
	}
	if len(mems) == 0 {
		fmt.Fprintf(os.Stderr, "%s\n", st.dim("  nothing remembered for "+root))
		return nil
	}
	for _, m := range mems {
		fmt.Printf("%s  %s %s\n", st.dim(fmt.Sprintf("%3d", m.ID)), m.Text,
			st.dim("("+m.Source+", "+humanAge(m.CreatedAt)+")"))
	}
	return nil
}

// humanAge renders a timestamp as a short relative age.
func humanAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	}
}
