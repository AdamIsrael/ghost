package prbody

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func texts(ls []Line) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Text)
	}
	return out
}

func check(t *testing.T, body string, width int, want ...string) {
	t.Helper()
	got := texts(render(body, width))
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("render(%q):\n got: %q\nwant: %q", body, got, want)
	}
}

// GitHub #20: the Dependabot body from the issue.
func TestDependabot(t *testing.T) {
	b, err := os.ReadFile("testdata/dependabot.md")
	if err != nil {
		t.Fatal(err)
	}
	got := render(string(b), 72)
	want := []Line{
		{Plain, "Bumps zizmorcore/zizmor-action from 0.6.3 to 0.8.0."},
		{Plain, ""},
		{Summary, "▸ Release notes"},
		{Summary, "▸ Commits"},
		{Plain, ""},
		{Plain, "Dependabot will resolve any conflicts with this PR as long as you don't"},
		{Plain, "alter it yourself. You can also trigger a rebase manually by commenting"},
		{Plain, "@dependabot rebase."},
		{Plain, ""},
		{Muted, strings.Repeat("─", 40)},
		{Plain, ""},
		{Summary, "▸ Dependabot commands and options"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%q", len(got), len(want), texts(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestInline(t *testing.T) {
	check(t, "See [the docs](https://x.test/d) and https://x.test/a, ![badge](https://x.test/b.svg) **bold** _it_ `code` &amp; done \\*not em\\*", 200,
		"See the docs and https://x.test/a, bold it code & done *not em*")
	check(t, `<p>Fish <a href="https://x.test">&amp; chips</a><img src="x.png"> <code>a&lt;b</code></p>`, 80,
		"Fish & chips a<b")
	check(t, "keep\n<!-- hidden -->\n\n[//]: # (hidden too)\n\nend", 80, "keep", "", "end")
	check(t, "one<br>two<br/>three", 80, "one", "two", "three")
	check(t, "<script>alert(1)</script><style>p{}</style>shown", 80, "shown")
}

func TestHeadings(t *testing.T) {
	got := render("# Title\n\ntext\n\n<h2>HTML title</h2>\n\nmore", 80)
	want := []Line{{Heading, "Title"}, {Plain, ""}, {Plain, "text"}, {Plain, ""}, {Heading, "HTML title"}, {Plain, ""}, {Plain, "more"}}
	if len(got) != len(want) {
		t.Fatalf("got %q", texts(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLists(t *testing.T) {
	check(t, "Intro\n\n- a\n- b\n  - nested\n- [x] done\n\n1. first\n2. second\n\n<ul><li>html one</li><li>html two</li></ul>after", 80,
		"Intro", "", "• a", "• b", "  • nested", "• [x] done", "", "1. first", "2. second", "", "• html one", "• html two", "", "after")
	check(t, "- a long item that wraps onto a second line here", 20,
		"• a long item that", "  wraps onto a", "  second line here")
}

func TestQuoteAndCode(t *testing.T) {
	check(t, "> quoted text\n\n```go\nx := <b>1</b>\n```\n\n    indented", 80,
		"│ quoted text", "", "  x := <b>1</b>", "", "  indented")
}

func TestDetails(t *testing.T) {
	// Markdown inside <details> (after a blank line) is hidden too, and so
	// is a nested <details>; one with no <summary> gets a stand-in.
	body := "<details>\n<summary>Outer <b>notes</b></summary>\n\n- hidden item\n\n# hidden heading\n\n<details><summary>inner</summary>x</details>\n\n</details>\n\nvisible\n\n<details>no summary</details>"
	check(t, body, 80, "▸ Outer notes", "", "visible", "", "▸ Details")
}

func TestEmpty(t *testing.T) {
	if got := render("  \n\n", 80); len(got) != 0 {
		t.Errorf("got %q", texts(got))
	}
	if got := render("<details><summary></summary>only hidden</details>", 80); len(got) != 1 || got[0].Text != "▸ Details" {
		t.Errorf("got %q", texts(got))
	}
}

func TestRenderCutsHugeBodies(t *testing.T) {
	body := strings.Repeat("word ", MaxBody/5) + "\nlast line"
	got := Render(body, 80)
	if l := got[len(got)-1]; l.Kind != Muted || !strings.Contains(l.Text, "description cut") {
		t.Errorf("last line = %+v, want the cut note", l)
	}
	for _, l := range got {
		if strings.Contains(l.Text, "last line") {
			t.Error("text past the cap was rendered")
		}
	}
	if got := Render("short", 80); len(got) != 1 {
		t.Errorf("short body: %q", texts(got))
	}
}

func FuzzRender(f *testing.F) {
	b, _ := os.ReadFile("testdata/dependabot.md")
	for _, s := range []string{string(b), "<details><summary>a</details></summary>", "- a\n  > b\n    ```\n<details>", "<table><tr><td>a</td></tr></table>", "| a | b |\n|---|---|\n| 1 | 2 |"} {
		f.Add(s, 30)
	}
	f.Fuzz(func(t *testing.T, body string, width int) {
		width = width%200 + 1
		if width < 1 {
			width += 200
		}
		for _, l := range convert(body, width) {
			if l.Kind != Muted && l.Kind != Summary && ansi.StringWidth(l.Text) > max(width, 10)+2*strings.Count(l.Text, "│ ")+40 {
				t.Fatalf("line wider than %d: %q", width, l.Text)
			}
		}
	})
}
