// Command ghost watches GitHub pull requests and runs AI code reviews.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cli/go-gh/v2/pkg/browser"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/engine"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/paths"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/preflight"
	subproc "github.com/AdamIsrael/ghost/internal/run"
	"github.com/AdamIsrael/ghost/internal/store"
	"github.com/AdamIsrael/ghost/internal/tui"
	"github.com/AdamIsrael/ghost/internal/workspace"
)

// deps are the side-effecting collaborators run needs; tests replace them.
type deps struct {
	runner      preflight.Runner
	newSearcher func() (poller.Searcher, error)
	runTUI      func(tea.Model) error
	openURL     func(string) error
	getPR       func(ctx context.Context, repo string, number int) (github.PRDetail, error)
	checkout    func(ctx context.Context, repo string, number int, baseRef, headSHA string) (workspace.Workspace, func(), error)
	newReviewer func(name string, a config.Agent) (agent.Reviewer, error)
	postReview  func(ctx context.Context, repo string, number int, p github.ReviewPost) (github.PostResult, error)
	hasReview   func(ctx context.Context, repo string, number int, marker string) (bool, error)
	approve     func(ctx context.Context, repo string, number int, commitID, body string) error
}

func defaultDeps() deps {
	return deps{
		runner:      preflight.ExecRunner,
		newSearcher: func() (poller.Searcher, error) { return github.NewDefault() },
		runTUI: func(m tea.Model) error {
			_, err := tea.NewProgram(m).Run()
			return err
		},
		// Discard browser launcher output so it can't corrupt the TUI.
		openURL: browser.New("", io.Discard, io.Discard).Browse,
		getPR: func(ctx context.Context, repo string, number int) (github.PRDetail, error) {
			c, err := github.NewDefault()
			if err != nil {
				return github.PRDetail{}, err
			}
			return c.GetPR(ctx, repo, number)
		},
		checkout: workspace.New(paths.CacheDir(), subproc.Exec).Checkout,
		newReviewer: func(name string, a config.Agent) (agent.Reviewer, error) {
			return agent.New(name, a, subproc.Exec)
		},
		postReview: func(ctx context.Context, repo string, number int, p github.ReviewPost) (github.PostResult, error) {
			c, err := github.NewDefault()
			if err != nil {
				return github.PostResult{}, err
			}
			return c.PostReview(ctx, repo, number, p)
		},
		hasReview: func(ctx context.Context, repo string, number int, marker string) (bool, error) {
			c, err := github.NewDefault()
			if err != nil {
				return false, err
			}
			return c.HasReview(ctx, repo, number, marker)
		},
		approve: func(ctx context.Context, repo string, number int, commitID, body string) error {
			c, err := github.NewDefault()
			if err != nil {
				return err
			}
			return c.Approve(ctx, repo, number, commitID, body)
		},
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, defaultDeps()))
}

func run(args []string, stdout, stderr io.Writer, d deps) int {
	fs := flag.NewFlagSet("ghost", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: ghost [flags] [init | review <pr> [--agent NAME] (--dry-run | --show-prompt)]")
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
	if fs.Arg(0) == "review" {
		if *check {
			fs.Usage()
			return 2
		}
		return runReview(paths.ExpandHome(*cfgPath), fs.Args()[1:], stdout, stderr, d)
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
		return runCheck(*cfgPath, stdout, d.runner)
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

	opts := tui.Options{
		Views:           cfg.Views,
		MaxDailyCostUSD: cfg.MaxDailyCostUSD,
		Checks:          preflight.Run(context.Background(), d.runner),
		OpenURL:         d.openURL,
		Theme:           cfg.Theme,
		NoColor:         os.Getenv("NO_COLOR") != "",
		ConfigPath:      *cfgPath,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := engine.New(cfg, engine.Deps{
		Store: st, GetPR: d.getPR, Checkout: d.checkout, NewReviewer: d.newReviewer,
		PostReview: d.postReview, HasReview: d.hasReview, Approve: d.approve,
	})
	engineDone := make(chan struct{})
	go func() { eng.Run(ctx); close(engineDone) }()
	// On return: stop the engine and give in-flight work a moment to record
	// its outcome before the deferred store close.
	defer func() {
		cancel()
		select {
		case <-engineDone:
		case <-time.After(10 * time.Second):
		}
	}()
	opts.Engine = eng
	var p *poller.Poller
	if s, err := d.newSearcher(); err != nil {
		opts.PollDisabled = "GitHub unavailable: " + err.Error()
	} else {
		// Started even with no views: views can be added from the TUI.
		p = poller.New(s, cfg.Views, cfg.PollInterval.Duration)
		go p.Run(ctx)
		opts.Results, opts.PollNow = p.Results(), p.PollNow
	}
	for name := range cfg.Agents {
		opts.Agents = append(opts.Agents, name)
	}
	sort.Strings(opts.Agents)
	viewsPath := config.ViewsPath(*cfgPath)
	opts.SaveViews = func(views []config.View) error {
		next := cfg
		next.Views = views
		if err := next.Validate(); err != nil {
			return err
		}
		var managed []config.View
		for _, v := range views {
			if v.Source != "config.toml" {
				managed = append(managed, v)
			}
		}
		if err := config.SaveViews(viewsPath, managed); err != nil {
			return err
		}
		cfg.Views = views
		eng.SetViews(views)
		if p != nil {
			p.SetViews(views)
		}
		return nil
	}
	if err := d.runTUI(tui.New(opts)); err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
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
