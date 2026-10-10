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
	"github.com/AdamIsrael/ghost/internal/github"
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
	prompt, err := renderReviewPrompt(&cfg, name, pr)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	if *showPrompt {
		fmt.Fprintln(stdout, prompt)
		return 0
	}

	reviewer, err := d.newReviewer(name, agentCfg)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	fmt.Fprintf(stderr, "Checking out %s#%d at %.7s…\n", repo, number, pr.HeadSHA)
	dir, cleanup, err := d.checkout(ctx, repo, number, pr.BaseRef, pr.HeadSHA)
	defer cleanup()
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	fmt.Fprintf(stderr, "Running %s (cap $%.2f)…\n", name, cfg.MaxCostPerReviewUSD)
	res, err := reviewer.Review(ctx, agent.ReviewRequest{Dir: dir, Prompt: prompt, MaxCostUSD: cfg.MaxCostPerReviewUSD})
	if err != nil {
		fmt.Fprintf(stderr, "ghost: review failed after spending $%.2f: %v\n", res.CostUSD, err)
		return 1
	}
	printReview(stdout, pr, name, res)
	return 0
}

func renderReviewPrompt(cfg *config.Config, agentName string, pr github.PRDetail) (string, error) {
	text, err := cfg.AgentPrompt(agentName)
	if err != nil {
		return "", err
	}
	rendered, err := config.RenderPrompt(text, config.PromptData{
		Repo: pr.Repo, Number: pr.Number, Title: pr.Title, Author: pr.Author, Body: pr.Body,
		BaseRef: pr.BaseRef, HeadRef: pr.HeadRef, HeadSHA: pr.HeadSHA, URL: pr.URL,
		ChangedFiles: pr.Files,
	})
	if err != nil {
		return "", fmt.Errorf("agents.%s prompt: %w", agentName, err)
	}
	return agent.BuildPrompt(rendered), nil
}

func printReview(w io.Writer, pr github.PRDetail, agentName string, res agent.ReviewResult) {
	fmt.Fprintf(w, "%s#%d  %s\n", pr.Repo, pr.Number, pr.Title)
	fmt.Fprintf(w, "Agent: %s   Verdict: %s   Cost: $%.2f\n\n", agentName, res.Verdict, res.CostUSD)
	fmt.Fprintf(w, "%s\n\n", res.Summary)
	if len(res.Comments) == 0 {
		fmt.Fprintln(w, "No inline comments.")
	}
	for _, c := range res.Comments {
		fmt.Fprintf(w, "%s:%d [%s]\n  %s\n", c.Path, c.Line, c.Severity, c.Body)
	}
	fmt.Fprintln(w, "\n(dry run: nothing was posted to GitHub)")
}
