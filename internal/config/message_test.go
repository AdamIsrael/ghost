package config

import (
	"strings"
	"testing"
)

func TestMessagesOffByDefault(t *testing.T) {
	cfg := Default()
	if cfg.ReviewFooter != "" || cfg.ApprovalMessage != "" {
		t.Errorf("defaults: footer %q, approval %q", cfg.ReviewFooter, cfg.ApprovalMessage)
	}
	if s, err := RenderMessage("", MessageData{Agent: "a"}); err != nil || s != "" {
		t.Errorf("empty template = %q, %v", s, err)
	}
}

func TestRenderMessage(t *testing.T) {
	d := MessageData{Agent: "sec", Model: "m1", Repo: "o/r", Number: 7, HeadSHA: "abc1234"}
	got, err := RenderMessage("{{.Agent}} {{.Model}} {{.Repo}}#{{.Number}} {{.HeadSHA}}", d)
	if err != nil || got != "sec m1 o/r#7 abc1234" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestValidateMessages(t *testing.T) {
	ok := mustParse(t, "review_footer = \"by {{.Agent}}\"\napproval_message = \"LGTM {{.HeadSHA}}\"\n"+minimal)
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"review_footer": "review_footer", "approval_message": "approval_message"} {
		for _, bad := range []string{"{{.Agent", "{{.Title}}"} {
			cfg := mustParse(t, key+" = \""+bad+"\"\n"+minimal)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s = %q: err = %v", key, bad, err)
			}
		}
	}
}
