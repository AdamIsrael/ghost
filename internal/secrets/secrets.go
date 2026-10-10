// Package secrets detects credential-shaped strings in text Ghost is about to
// post, as a last line of defence against an agent leaking one.
package secrets

import (
	"regexp"
	"strings"
)

type Finding struct {
	Kind     string
	Redacted string
}

var patterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`)},
	{"GitHub fine-grained token", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{60,}`)},
	{"AWS access key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"Slack token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`)},
	{"Anthropic API key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`)},
}

func Scan(text string) []Finding {
	var out []Finding
	for _, p := range patterns {
		for _, m := range p.re.FindAllString(text, -1) {
			out = append(out, Finding{Kind: p.kind, Redacted: redact(m)})
		}
	}
	return out
}

func redact(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:6] + "…"
}

// Kinds lists the distinct kinds found, in pattern order.
func Kinds(fs []Finding) string {
	seen := map[string]bool{}
	var kinds []string
	for _, p := range patterns {
		for _, f := range fs {
			if f.Kind == p.kind && !seen[f.Kind] {
				seen[f.Kind] = true
				kinds = append(kinds, f.Kind)
			}
		}
	}
	return strings.Join(kinds, ", ")
}
