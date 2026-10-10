package engine

import (
	"fmt"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
)

// RenderPrompt fills the agent's prompt template with pr's details. The
// caller appends Ghost's fixed sections with agent.BuildPrompt.
func RenderPrompt(cfg *config.Config, agentName string, pr github.PRDetail) (string, error) {
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
	return rendered, nil
}
