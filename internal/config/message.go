package config

import (
	"strings"
	"text/template"
)

// MessageData holds the variables available to review_footer and
// approval_message templates.
type MessageData struct {
	Agent   string
	Model   string
	Repo    string
	Number  int
	HeadSHA string // short (7 characters)
}

// RenderMessage executes a review_footer or approval_message template. An
// empty template renders as "" (the message is off).
func RenderMessage(text string, d MessageData) (string, error) {
	if text == "" {
		return "", nil
	}
	tmpl, err := template.New("message").Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, d); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

var sampleMessage = MessageData{Agent: "claude", Model: "claude-opus-5-5", Repo: "owner/repo", Number: 1, HeadSHA: "0123456"}
