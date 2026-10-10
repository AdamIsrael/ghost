package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/engine"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/textsafe"
)

func runReview(cfgPath string, args []string, stdout, stderr io.Writer, d deps) int {
	fs := flag.NewFlagSet("ghost review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agentName := fs.String("agent", "", "agent from config (default: default_agent)")
	dryRun := fs.Bool("dry-run", false, "run the review and print it; post nothing")
	showPrompt := fs.Bool("show-prompt", false, "print the rendered prompt and exit; runs nothing")
	// Accept the PR ref before or after flags.
	var ref string
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		ref, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if ref == "" && fs.NArg() == 1 {
		ref = fs.Arg(0)
	} else if fs.NArg() > 0 || ref == "" {
		fmt.Fprintln(stderr, "usage: ghost review <pr-url | owner/repo#N> [--agent NAME] (--dry-run | --show-prompt)")
		return 2
	}
	if *dryRun == *showPrompt {
		fmt.Fprintln(stderr, "ghost review: pass exactly one of --dry-run or --show-prompt (posting reviews arrives in M4)")
		return 2
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	name := *agentName
	if name == "" {
		name = cfg.DefaultAgent
	}
	agentCfg, ok := cfg.Agents[name]
	if !ok {
		fmt.Fprintf(stderr, "ghost: unknown agent %q\n", name)
		return 1
	}
	repo, number, err := github.ParsePRRef(ref)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	pr, err := d.getPR(ctx, repo, number)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	if pr.Repo != "" {
		repo = pr.Repo // GitHub's canonical name, so CLI/cli and cli/cli share a clone
	}
	rendered, err := engine.RenderPrompt(&cfg, name, pr)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	if *showPrompt {
		fmt.Fprintln(stdout, textsafe.Clean(agent.BuildPrompt(rendered, "<context>/diff.patch", "<context>/commits.txt")))
		return 0
	}

	reviewer, err := d.newReviewer(name, agentCfg)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	fmt.Fprintf(stderr, "Checking out %s#%d at %.7s…\n", repo, number, pr.HeadSHA)
	ws, cleanup, err := d.checkout(ctx, repo, number, pr.BaseRef, pr.HeadSHA)
	defer cleanup()
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	fmt.Fprintf(stderr, "Running %s (cap $%.2f)…\n", name, cfg.MaxCostPerReviewUSD)
	res, err := reviewer.Review(ctx, agent.ReviewRequest{
		Dir:        ws.Dir,
		ContextDir: ws.ContextDir,
		Prompt:     agent.BuildPrompt(rendered, ws.DiffPath, ws.CommitsPath),
		MaxCostUSD: cfg.MaxCostPerReviewUSD,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ghost: review failed after spending $%.2f: %v\n", res.CostUSD, textsafe.Line(err.Error()))
		return 1
	}
	printReview(stdout, pr, name, res)
	return 0
}

// printReview writes the review to the terminal. PR text and agent output
// are untrusted, so everything is passed through textsafe.
func printReview(w io.Writer, pr github.PRDetail, agentName string, res agent.ReviewResult) {
	fmt.Fprintf(w, "%s#%d  %s\n", textsafe.Line(pr.Repo), pr.Number, textsafe.Line(pr.Title))
	fmt.Fprintf(w, "Agent: %s   Verdict: %s   Cost: $%.2f\n\n", agentName, textsafe.Line(string(res.Verdict)), res.CostUSD)
	fmt.Fprintf(w, "%s\n\n", textsafe.Clean(res.Summary))
	if len(res.Comments) == 0 {
		fmt.Fprintln(w, "No inline comments.")
	}
	for _, c := range res.Comments {
		fmt.Fprintf(w, "%s:%d [%s]\n  %s\n", textsafe.Line(c.Path), c.Line, textsafe.Line(c.Severity), textsafe.Clean(c.Body))
	}
	fmt.Fprintln(w, "\n(dry run: nothing was posted to GitHub)")
}
