// Command ghost watches GitHub pull requests and runs AI code reviews.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/adamisrael/ghost/internal/config"
	"github.com/adamisrael/ghost/internal/paths"
	"github.com/adamisrael/ghost/internal/preflight"
	"github.com/adamisrael/ghost/internal/store"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, preflight.ExecRunner))
}

func run(args []string, stdout, stderr io.Writer, runner preflight.Runner) int {
	fs := flag.NewFlagSet("ghost", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: ghost [flags] [init]")
		fs.PrintDefaults()
	}
	cfgPath := fs.String("config", paths.ConfigFile(), "path to config.toml")
	dbPath := fs.String("db", paths.DataFile(), "path to SQLite database")
	check := fs.Bool("check", false, "verify git, gh and claude, then exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	isInit := fs.Arg(0) == "init"
	if isInit {
		// flag stops at the first non-flag; parse what follows "init" too.
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return 2
		}
	}
	if fs.NArg() > 0 || (isInit && *check) {
		fs.Usage()
		return 2
	}
	*cfgPath, *dbPath = paths.ExpandHome(*cfgPath), paths.ExpandHome(*dbPath)

	switch {
	case isInit:
		return runInit(*cfgPath, stdout, stderr)
	case *check:
		return runCheck(*cfgPath, stdout, runner)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	defer st.Close()
	fmt.Fprintf(stdout, "ghost: config OK (%d agents, %d views); database at %s. TUI arrives in M2.\n",
		len(cfg.Agents), len(cfg.Views), *dbPath)
	return 0
}

func runInit(cfgPath string, stdout, stderr io.Writer) int {
	created, err := config.WriteDefaults(cfgPath)
	for _, p := range created {
		fmt.Fprintln(stdout, "created", p)
	}
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	if len(created) == 0 {
		fmt.Fprintln(stdout, "nothing to do: config already exists at", cfgPath)
	}
	return 0
}

func runCheck(cfgPath string, stdout io.Writer, runner preflight.Runner) int {
	results := preflight.Run(context.Background(), runner)
	ok := preflight.AllOK(results)
	for _, r := range results {
		mark := "✓"
		if !r.OK {
			mark = "✗"
		}
		fmt.Fprintf(stdout, "%s %-7s %s\n", mark, r.Name, r.Detail)
		if r.Fix != "" {
			fmt.Fprintf(stdout, "          fix: %s\n", r.Fix)
		}
	}
	if _, err := config.Load(cfgPath); err != nil {
		ok = false
		fmt.Fprintf(stdout, "✗ %-7s %v\n", "config", err)
	} else {
		fmt.Fprintf(stdout, "✓ %-7s %s\n", "config", cfgPath)
	}
	if !ok {
		return 1
	}
	return 0
}
