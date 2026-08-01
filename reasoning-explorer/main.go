package main

import (
	"fmt"
	"os"
)

var phases = map[string]func() error{
	"phase1": phase1,
	"phase2": phase2,
	"phase3": phase3,
	"phase4": phase4,
	"phase5": phase5,
	"phase6": phase6,
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run . <phase1|phase2|phase3|phase4|phase5>")
		os.Exit(2)
	}
	run, ok := phases[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}
