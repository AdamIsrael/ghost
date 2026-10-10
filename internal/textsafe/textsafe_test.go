package textsafe

import "testing"

func TestClean(t *testing.T) {
	cases := map[string]string{
		"a\r\nb":                 "a\nb",
		"x\ry":                   "xy",
		"\x1b[31mred\x1b[0m":     "[31mred[0m",
		"\x1b]52;c;ZXZpbA==\x07": "]52;c;ZXZpbA==",
		"tab\there":              "tab    here",
		"c1\u009bx":              "c1x",
		"del\x7f!":               "del!",
		"ok ✓ 日本":                "ok ✓ 日本",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLine(t *testing.T) {
	if got := Line("a\nb\r\nc"); got != "a b c" {
		t.Errorf("Line = %q", got)
	}
}
