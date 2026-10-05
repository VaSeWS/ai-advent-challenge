package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runWeek05(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runWeek05(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printWeek05Usage(os.Stdout)
		return nil
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		if len(args) > 1 {
			return fmt.Errorf("help: unexpected arguments: %s", strings.Join(args[1:], " "))
		}
		printWeek05Usage(os.Stdout)
		return nil
	}
	switch args[0] {
	case "corpus":
		return RunCorpus(ctx, args[1:])
	case "index":
		return RunIndex(ctx, args[1:])
	case "evaluate":
		return RunEvaluate(ctx, args[1:])
	case "chat":
		return RunChat(ctx, args[1:])
	default:
		return fmt.Errorf("unknown action %q (want corpus, index, evaluate, chat; use help)", args[0])
	}
}

func printWeek05Usage(out io.Writer) {
	fmt.Fprintln(out, "Usage: week-05 <action> [flags]")
	fmt.Fprintln(out, "Actions:")
	fmt.Fprintln(out, "  corpus     fetch or report a revision-pinned GT:NH wiki snapshot")
	fmt.Fprintln(out, "  index      build or inspect the SQLite/sqlite-vec index")
	fmt.Fprintln(out, "  evaluate   compare the approved question set or inspect a saved run")
	fmt.Fprintln(out, "  chat       open the persistent grounded comparison UI")
	fmt.Fprintln(out, "  help       show this help")
	fmt.Fprintln(out, "Use '<action> -h' to list that action's flags and defaults.")
}
