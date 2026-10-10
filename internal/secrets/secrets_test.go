package secrets

import (
	"strings"
	"testing"
)

func TestScanFindsKnownSecretShapes(t *testing.T) {
	cases := map[string]string{
		"GitHub token":              "token ghp_" + strings.Repeat("a", 36) + " here",
		"GitHub fine-grained token": "github_pat_" + strings.Repeat("B", 70),
		"AWS access key":            "key AKIA" + strings.Repeat("Q", 16),
		"private key":               "-----BEGIN RSA PRIVATE KEY-----\nMIIE...",
		"Slack token":               "xoxb-1234567890-abcdefghij",
		"Anthropic API key":         "sk-ant-api03-" + strings.Repeat("z", 30),
		"Google API key":            "AIza" + strings.Repeat("x", 35),
	}
	for kind, text := range cases {
		fs := Scan(text)
		if len(fs) != 1 || fs[0].Kind != kind {
			t.Errorf("%s: got %+v", kind, fs)
		}
	}
}

func TestScanIgnoresOrdinaryText(t *testing.T) {
	for _, text := range []string{
		"Use ghp_ prefix for tokens", "AKIA is the prefix", "sk-ant-short",
		"func readToken() string { return os.Getenv(\"GITHUB_TOKEN\") }",
		"-----BEGIN PUBLIC KEY-----",
	} {
		if fs := Scan(text); len(fs) != 0 {
			t.Errorf("%q: false positive %+v", text, fs)
		}
	}
}

func TestRedactionHidesTheSecret(t *testing.T) {
	tok := "ghp_" + strings.Repeat("s", 36)
	fs := Scan(tok)
	if len(fs) != 1 || strings.Contains(fs[0].Redacted, tok[8:]) {
		t.Fatalf("not redacted: %+v", fs)
	}
}

func TestKinds(t *testing.T) {
	fs := Scan("ghp_" + strings.Repeat("a", 36) + " ghp_" + strings.Repeat("b", 36) + " AKIA" + strings.Repeat("C", 16))
	if got := Kinds(fs); got != "GitHub token, AWS access key" {
		t.Errorf("Kinds = %q", got)
	}
}
