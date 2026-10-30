// Package prbody turns a pull request body (GitHub Markdown, which mixes in
// HTML) into short wrapped lines for the terminal: formatting becomes plain
// text, links keep only their text, images and comments go, and each
// <details> section collapses to its summary.
//
// The lines are not safe to print as they are: the body is untrusted, so the
// caller still strips control characters.
package prbody

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"golang.org/x/net/html"
)

type Kind int

const (
	Plain   Kind = iota
	Heading      // a heading's text
	Muted        // code blocks and rules
	Summary      // a collapsed <details> section: "▸ " + its summary
)

type Line struct {
	Kind Kind
	Text string
}

var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// MaxBody is the most of a body Render reads (GitHub's own limit is 65536
// characters). goldmark is slow on some hostile input (~2s at this size),
// so callers should render off the UI thread.
const MaxBody = 64 << 10

// Render returns body as lines at most width cells wide (code lines may be
// longer; the caller cuts them). Blank lines separate blocks.
func Render(body string, width int) []Line {
	cut := false
	if len(body) > MaxBody {
		body, cut = body[:MaxBody], true
		if i := strings.LastIndexByte(body, '\n'); i > 0 {
			body = body[:i]
		}
		body = strings.ToValidUTF8(body, "")
	}
	lines := render(body, width)
	if cut {
		lines = append(lines, Line{Plain, ""}, Line{Muted, "… (description cut; o opens the PR)"})
	}
	return lines
}

func render(body string, width int) (lines []Line) {
	defer func() {
		if recover() != nil { // never lose the description over a parser bug
			lines = nil
			for _, l := range strings.Split(ansi.Wrap(strings.TrimSpace(body), max(width, 1), " "), "\n") {
				lines = append(lines, Line{Plain, l})
			}
		}
	}()
	return convert(body, width)
}

// convert is render without the fallback (tests and fuzzing call it).
func convert(body string, width int) []Line {
	src := []byte(body)
	w := &writer{width: max(width, 1), src: src}
	_ = ast.Walk(md.Parser().Parse(text.NewReader(src)), w.walk)
	w.endBlock()
	return w.lines
}

type writer struct {
	src   []byte
	width int
	lines []Line

	buf        strings.Builder // the block being collected
	kind       Kind
	first      string // prefix for the block's first line ("• ")
	rest       string // prefix for its other lines
	blankAfter bool   // the last block ended; separate the next one

	quote   int   // blockquote depth
	lists   []int // per open list: the next number, or -1 if bulleted
	tight   []bool
	details int  // open <details> depth; > 0 hides everything
	summary bool // inside the outermost <summary>
	summed  bool // the outermost <details> has shown its summary
	sumText strings.Builder
	skip    int // inside <script>/<style>/<template>
}

func (w *writer) hidden() bool { return (w.details > 0 && !w.summary) || w.skip > 0 }

func (w *writer) text(s string) {
	if w.summary && w.details == 1 {
		w.sumText.WriteString(s)
		return
	}
	if !w.hidden() {
		w.buf.WriteString(s)
	}
}

// maxIndent caps the nesting shown, so deep nesting can't push the text off
// the screen.
const maxIndent = 8

func (w *writer) indent() string {
	return strings.Repeat("│ ", min(w.quote, maxIndent)) + strings.Repeat("  ", min(max(len(w.lists)-1, 0), maxIndent))
}

// emit adds lines, preceded by a blank one if a block ended before them.
func (w *writer) emit(ls ...Line) {
	if len(ls) == 0 {
		return
	}
	if w.blankAfter && len(w.lines) > 0 {
		prev := w.lines[len(w.lines)-1].Kind
		if !(prev == Summary && ls[0].Kind == Summary) {
			w.lines = append(w.lines, Line{Plain, strings.TrimRight(w.indent(), " ")})
		}
	}
	w.blankAfter = false
	w.lines = append(w.lines, ls...)
}

// flush wraps the collected text into lines.
func (w *writer) flush() {
	s := strings.Join(strings.Fields(w.buf.String()), " ")
	w.buf.Reset()
	if s == "" {
		w.first = w.rest
		return
	}
	first, rest := w.indent()+w.first, w.indent()+w.rest
	w.first = w.rest
	room := max(w.width-ansi.StringWidth(rest), 10)
	var ls []Line
	for i, l := range strings.Split(ansi.Wrap(s, room, " "), "\n") {
		p := rest
		if i == 0 {
			p = first
		}
		ls = append(ls, Line{w.kind, p + l})
	}
	w.emit(ls...)
}

// endBlock finishes the current block; the next one starts after a blank line.
func (w *writer) endBlock() {
	w.flush()
	w.kind, w.first, w.rest = Plain, "", ""
	w.blankAfter = true
}

func (w *writer) walk(n ast.Node, entering bool) (ast.WalkStatus, error) {
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		if !entering {
			w.flush()
			if !w.inTightList() {
				w.blankAfter = true
			}
		}
	case *ast.Heading:
		if entering {
			w.endBlock()
			w.kind = Heading
		} else {
			w.endBlock()
		}
	case *ast.ThematicBreak:
		if entering && !w.hidden() {
			w.endBlock()
			w.emit(Line{Muted, w.indent() + strings.Repeat("─", min(w.width, 40))})
			w.blankAfter = true
		}
	case *ast.Blockquote:
		w.endBlock()
		if entering {
			w.quote++
		} else {
			w.quote--
		}
	case *ast.List:
		w.endBlock()
		if entering {
			next := -1
			if n.IsOrdered() {
				next = n.Start
			}
			if _, nested := n.Parent().(*ast.ListItem); nested && w.inTightList() {
				w.blankAfter = false
			}
			w.lists = append(w.lists, next)
			w.tight = append(w.tight, n.IsTight)
		} else {
			w.lists, w.tight = w.lists[:len(w.lists)-1], w.tight[:len(w.tight)-1]
		}
	case *ast.ListItem:
		w.flush()
		if entering {
			if n.PreviousSibling() != nil && w.inTightList() {
				w.blankAfter = false
			}
			marker := "• "
			if i := len(w.lists) - 1; w.lists[i] >= 0 {
				marker = fmt.Sprintf("%d. ", w.lists[i])
				w.lists[i]++
			}
			w.first, w.rest = marker, strings.Repeat(" ", ansi.StringWidth(marker))
		} else {
			w.first, w.rest = "", ""
		}
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		if entering && !w.hidden() {
			w.endBlock()
			var ls []Line
			lines := n.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i)
				ls = append(ls, Line{Muted, w.indent() + "  " + strings.TrimRight(string(seg.Value(w.src)), "\r\n")})
			}
			w.emit(ls...)
			w.blankAfter = true
		}
		return ast.WalkSkipChildren, nil
	case *ast.Text:
		if entering {
			w.text(unescape(n.Segment.Value(w.src)))
			if n.HardLineBreak() || n.SoftLineBreak() {
				w.flush() // GitHub shows a body's newlines as line breaks
			}
		}
	case *ast.String:
		if entering {
			w.text(string(n.Value))
		}
	case *ast.CodeSpan:
		if entering {
			for c := n.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					w.text(string(t.Segment.Value(w.src)))
				}
			}
		}
		return ast.WalkSkipChildren, nil
	case *ast.AutoLink:
		if entering {
			w.text(string(n.Label(w.src)))
		}
	case *ast.Image:
		return ast.WalkSkipChildren, nil
	case *ast.RawHTML:
		if entering {
			var b bytes.Buffer
			for i := 0; i < n.Segments.Len(); i++ {
				seg := n.Segments.At(i)
				b.Write(seg.Value(w.src))
			}
			w.html(b.Bytes())
		}
	case *ast.HTMLBlock:
		if entering {
			var b bytes.Buffer
			lines := n.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i)
				b.Write(seg.Value(w.src))
			}
			if n.HasClosure() {
				b.Write(n.ClosureLine.Value(w.src))
			}
			w.html(b.Bytes())
			w.endBlock()
		}
		return ast.WalkSkipChildren, nil
	case *east.TaskCheckBox:
		if entering {
			if n.IsChecked {
				w.text("[x] ")
			} else {
				w.text("[ ] ")
			}
		}
	case *east.TableRow, *east.TableHeader:
		if !entering {
			w.flush()
		}
	case *east.TableCell:
		if !entering && n.NextSibling() != nil {
			w.text(" │ ")
		}
	case *east.Table:
		if !entering {
			w.endBlock()
		}
	}
	return ast.WalkContinue, nil
}

func (w *writer) inTightList() bool { return len(w.tight) > 0 && w.tight[len(w.tight)-1] }

// html handles a run of raw HTML: tags become the matching structure, text
// is kept, and <details> hides everything but its first <summary>.
func (w *writer) html(b []byte) {
	z := html.NewTokenizer(bytes.NewReader(b))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return
		case html.TextToken:
			if w.skip == 0 {
				w.text(string(z.Text())) // entities are decoded
			}
		case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
			name, _ := z.TagName()
			w.tag(string(name), tt == html.EndTagToken, tt == html.SelfClosingTagToken)
		}
	}
}

func (w *writer) tag(name string, end, selfClosing bool) {
	switch name {
	case "details":
		if selfClosing {
			return
		}
		if end {
			if w.details == 1 && !w.summed {
				w.emit(Line{Summary, w.indent() + "▸ Details"})
			}
			w.details = max(w.details-1, 0)
			if w.details == 0 {
				w.summary = false
			}
			return
		}
		if w.details == 0 {
			w.endBlock()
			w.summed = false
		}
		w.details++
	case "summary":
		if w.details != 1 {
			return
		}
		if !end {
			w.summary = true
			w.sumText.Reset()
			return
		}
		if w.summary {
			w.summary = false
			s := strings.Join(strings.Fields(w.sumText.String()), " ")
			if s == "" {
				s = "Details"
			}
			w.emit(Line{Summary, w.indent() + "▸ " + s})
			w.summed = true
		}
	case "script", "style", "template":
		if selfClosing {
			return
		}
		if end {
			w.skip = max(w.skip-1, 0)
		} else {
			w.skip++
		}
	case "br":
		if !w.hidden() {
			w.flush()
		}
	case "hr":
		if !w.hidden() {
			w.endBlock()
			w.emit(Line{Muted, w.indent() + strings.Repeat("─", min(w.width, 40))})
			w.blankAfter = true
		}
	case "h1", "h2", "h3", "h4", "h5", "h6":
		if w.hidden() {
			return
		}
		w.endBlock()
		if !end {
			w.kind = Heading
		}
	case "p", "div", "blockquote", "pre", "table", "ul", "ol":
		if !w.hidden() {
			w.endBlock()
		}
	case "li", "tr":
		if w.hidden() {
			return
		}
		w.flush()
		if name == "li" {
			w.first, w.rest = "• ", "  "
			if end {
				w.first, w.rest = "", ""
			}
		}
	case "td", "th":
		if end && !w.hidden() {
			w.text(" ")
		}
	}
}

// unescape resolves Markdown backslash escapes and HTML entities in text.
func unescape(b []byte) string {
	return html.UnescapeString(string(util.UnescapePunctuations(b)))
}
