package config

import (
	_ "embed"
	"strings"
	"text/template"
)

// DefaultPrompt is used by agents that set neither prompt nor prompt_file.
//
//go:embed default_prompt.md
var DefaultPrompt string

// PromptData holds the variables available to review prompt templates.
type PromptData struct {
	Repo         string
	Number       int
	Title        string
	Author       string
	Body         string
	BaseRef      string
	HeadRef      string
	HeadSHA      string
	URL          string
	ChangedFiles []string
}

// RenderPrompt executes a review prompt template against d.
func RenderPrompt(text string, d PromptData) (string, error) {
	tmpl, err := template.New("prompt").Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}
