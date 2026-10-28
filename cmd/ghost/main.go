// Command ghost watches GitHub pull requests and runs AI code reviews.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cli/go-gh/v2/pkg/browser"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/engine"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/logging"
	"github.com/AdamIsrael/ghost/internal/paths"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/preflight"
	subproc "github.com/AdamIsrael/ghost/internal/run"
	"github.com/AdamIsrael/ghost/internal/store"
	"github.com/AdamIsrael/ghost/internal/theme"
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
	addLabel    func(ctx context.Context, repo string, number int, label string) error
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
		addLabel: func(ctx context.Context, repo string, number int, label string) error {
			c, err := github.NewDefault()
			if err != nil {
				return err
			}
			return c.AddLabel(ctx, repo, number, label)
		},
	}
}

// Set at release build time by GoReleaser (-ldflags "-X main.version=…").
var (
	version = "dev"
	commit  = "none"
)

var readBuildInfo = debug.ReadBuildInfo

// versionString reports the release version, or for `go install …@vX` builds
// the module version and VCS revision Go embeds.
func versionString() string {
	v, c := version, commit
	if v == "dev" {
		if info, ok := readBuildInfo(); ok {
			if mv := info.Main.Version; mv != "" && mv != "(devel)" {
				v = mv
			}
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 7 {
					c = s.Value[:7]
				}
			}
		}
	}
	return fmt.Sprintf("ghost %s (commit %s)", v, c)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, defaultDeps()))
}

func run(args []string, stdout, stderr io.Writer, d deps) int {
	fs := flag.NewFlagSet("ghost", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: ghost [flags] [init | review <pr> [--agent NAME] [--dry-run | --show-prompt]]")
		fs.PrintDefaults()
	}
	cfgPath := fs.String("config", paths.ConfigFile(), "path to config.toml")
	dbPath := fs.String("db", paths.DataFile(), "path to SQLite database")
	check := fs.Bool("check", false, "verify git, gh and claude, then exit")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, versionString())
		return 0
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
		return runReview(paths.ExpandHome(*cfgPath), paths.ExpandHome(*dbPath), fs.Args()[1:], stdout, stderr, d)
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

	return startGhost(*cfgPath, *dbPath, stdout, stderr, d, nil)
}

// oneOff is a PR named on the command line (ghost review <pr>): Ghost opens
// on a tab for it and starts reviewing it.
type oneOff struct {
	repo   string
	number int
	agent  string // "" = default_agent
}

// oneOffView is the session-only tab for a ghost review PR, named after the
// repository and number ("my-repo#7"), unique among views.
func oneOffView(pr github.PR, once *oneOff, views []config.View) config.View {
	repo := once.repo
	if pr.Repo != "" {
		repo = pr.Repo // GitHub's canonical name
	}
	short := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		short = repo[i+1:]
	}
	name := fmt.Sprintf("%s#%d", short, once.number)
	for taken := true; taken; {
		taken = false
		for _, v := range views {
			if v.Name == name {
				name += " (review)"
				taken = true
				break
			}
		}
	}
	return config.View{Name: name, PR: fmt.Sprintf("%s#%d", repo, once.number), Source: "session"}
}

// startGhost runs the TUI with the engine and poller. once, if set, adds a
// tab for that PR and starts reviewing it (ghost review <pr>).
func startGhost(cfgPath, dbPath string, stdout, stderr io.Writer, d deps, once *oneOff) int {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	if err := cfg.ValidateThemes(); err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	themes, err := theme.Load(cfg.ThemesDir())
	if err != nil { // already checked by ValidateThemes; kept for safety
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	if once != nil && once.agent != "" {
		if _, ok := cfg.Agents[once.agent]; !ok {
			fmt.Fprintf(stderr, "ghost: unknown agent %q\n", once.agent)
			return 1
		}
	}
	st, err := store.Open(dbPath)
	if err != nil {
		if once != nil && errors.Is(err, store.ErrLocked) {
			fmt.Fprintf(stderr, "ghost: %v\nGhost is already open: switch to it and press r on %s#%d there.\n", err, once.repo, once.number)
			return 1
		}
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	defer st.Close()

	logPath := cfg.LogPath()
	logger, logFile, err := logging.Open(logPath)
	if err != nil {
		// A log is for debugging; don't refuse to start without one.
		fmt.Fprintf(stderr, "ghost: can't write the log %s: %v; continuing without it\n", logPath, err)
		logger, logFile, _ = logging.Open("")
	}
	defer logFile.Close()
	logger.Info("started", "version", versionString(), "config", cfgPath, "db", dbPath, "log", logPath)
	defer logger.Info("stopped")

	opts := tui.Options{
		Views:           cfg.Views,
		MaxDailyCostUSD: cfg.MaxDailyCostUSD,
		Checks:          preflight.Run(context.Background(), d.runner),
		OpenURL:         d.openURL,
		Theme:           cfg.Theme,
		NoColor:         os.Getenv("NO_COLOR") != "",
		ConfigPath:      cfgPath,
		Themes:          themes,
		ReviewLabel:     cfg.ReviewLabel,
		Log:             logger,
		MaxPRsPerView:   cfg.MaxPRsPerView,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := engine.New(cfg, engine.Deps{
		Store: st, GetPR: d.getPR, Checkout: d.checkout, NewReviewer: d.newReviewer,
		PostReview: d.postReview, HasReview: d.hasReview, Approve: d.approve, Log: logger,
		AddLabel: d.addLabel,
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
	views := cfg.Views
	if once != nil {
		// Fetch the PR first: a bad link fails here, before the TUI opens.
		pctx, pcancel := context.WithTimeout(ctx, 30*time.Second)
		pr, err := d.getPR(pctx, once.repo, once.number)
		pcancel()
		if err != nil {
			fmt.Fprintln(stderr, "ghost:", err)
			return 1
		}
		tab := oneOffView(pr.PR, once, views)
		views = append(append([]config.View(nil), views...), tab)
		opts.Views, opts.ActiveView = views, tab.Name
		if err := eng.ReviewNow(pr.PR, once.agent); err != nil {
			logger.Warn("review not started", "pr", tab.PR, "err", err)
		}
	}
	var p *poller.Poller
	if s, err := d.newSearcher(); err != nil {
		opts.PollDisabled = "GitHub unavailable: " + err.Error()
	} else {
		// Started even with no views: views can be added from the TUI.
		p = poller.New(s, views, cfg.PollInterval.Duration, cfg.PRLimitFor)
		go p.Run(ctx)
		opts.Results, opts.PollNow = p.Results(), p.PollNow
	}
	for name := range cfg.Agents {
		opts.Agents = append(opts.Agents, name)
	}
	sort.Strings(opts.Agents)
	viewsPath := config.ViewsPath(cfgPath)
	opts.SaveViews = func(views []config.View) error {
		// The one-off ghost review tab lives in the TUI only: never
		// validated as a search or written to views.toml.
		var saved []config.View
		for _, v := range views {
			if v.Source != "session" {
				saved = append(saved, v)
			}
		}
		next := cfg
		next.Views = saved
		if err := next.ValidateViews(); err != nil {
			return err
		}
		var managed []config.View
		for _, v := range saved {
			if v.Source != "config.toml" {
				managed = append(managed, v)
			}
		}
		if err := config.SaveViews(viewsPath, managed); err != nil {
			return err
		}
		cfg.Views = saved
		eng.SetViews(views)
		if p != nil {
			p.SetViews(views)
		}
		return nil
	}
	if err := d.runTUI(tui.New(opts)); err != nil {
		logger.Error("tui failed", "err", err)
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
	cfg, err := config.Load(cfgPath)
	if err != nil {
		ok = false
		fmt.Fprintf(stdout, "✗ %-7s %v\n", "config", err)
	} else {
		fmt.Fprintf(stdout, "✓ %-7s %s\n", "config", cfgPath)
		if p := cfg.LogPath(); p == "" {
			fmt.Fprintf(stdout, "✓ %-7s %s\n", "log", "off (log_file = \"\")")
		} else {
			fmt.Fprintf(stdout, "✓ %-7s %s\n", "log", p)
		}
		if cfg.ReviewLabel == "" {
			fmt.Fprintf(stdout, "✓ %-7s %s\n", "label", "off (PRs are reviewed without a label)")
		} else {
			fmt.Fprintf(stdout, "✓ %-7s %s\n", "label", cfg.ReviewLabel+" (added before review; removing it opts a PR out)")
		}
		if err := cfg.ValidateThemes(); err != nil {
			ok = false
			fmt.Fprintf(stdout, "✗ %-7s %v\n", "theme", err)
		} else if themes, err := theme.Load(cfg.ThemesDir()); err == nil {
			if p, found := themes.Get(cfg.Theme); found && p.Source != "built-in" {
				for _, problem := range p.ReadabilityProblems() {
					fmt.Fprintf(stdout, "! %-7s %s: %s\n", "theme", p.ID, problem)
				}
			}
		}
	}
	if !ok {
		return 1
	}
	return 0
}
