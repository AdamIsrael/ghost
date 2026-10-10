// Package theme loads TUI color themes: embedded built-ins plus user theme
// files. A theme is a set of color roles; the TUI decides what each role
// colors.
package theme

import (
	"embed"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Palette is one theme. Every role is optional: empty means the terminal's
// own color (or plain/bold/reverse for structure).
type Palette struct {
	ID     string `toml:"-"`
	Source string `toml:"-"` // "built-in" or the file it was loaded from

	Name       string `toml:"name"`
	Background string `toml:"background"`
	Foreground string `toml:"foreground"`
	Muted      string `toml:"muted"`
	Border     string `toml:"border"`
	Accent     string `toml:"accent"`
	OnAccent   string `toml:"on_accent"`
	Heading    string `toml:"heading"`
	Selection  string `toml:"selection"`
	Success    string `toml:"success"`
	Warning    string `toml:"warning"`
	Error      string `toml:"error"`
	Attention  string `toml:"attention"`
	Special    string `toml:"special"`
	Info       string `toml:"info"`
}

type role struct{ name, color string }

func (p Palette) roles() []role {
	return []role{
		{"background", p.Background}, {"foreground", p.Foreground}, {"muted", p.Muted},
		{"border", p.Border}, {"accent", p.Accent}, {"on_accent", p.OnAccent},
		{"heading", p.Heading}, {"selection", p.Selection}, {"success", p.Success},
		{"warning", p.Warning}, {"error", p.Error}, {"attention", p.Attention},
		{"special", p.Special}, {"info", p.Info},
	}
}

//go:embed builtin/*.toml
var builtinFS embed.FS

// BuiltinOrder is the order built-ins are listed and cycled in.
var BuiltinOrder = []string{
	"dark", "light", "high-contrast", "mono",
	"tokyo-night", "tokyo-night-storm", "tokyo-night-day",
	"catppuccin-mocha", "catppuccin-latte", "dracula", "nord", "gruvbox-dark",
	"solarized-dark", "solarized-light",
}

// Registry holds the available themes in display order.
type Registry struct {
	byID  map[string]Palette
	order []string
}

func (r *Registry) Get(id string) (Palette, bool) {
	p, ok := r.byID[id]
	return p, ok
}

func (r *Registry) IDs() []string { return append([]string(nil), r.order...) }

// Load returns the built-ins plus every *.toml file in userDir (which may be
// empty or missing). A user file named like a built-in replaces it in place;
// other user themes follow the built-ins, sorted by id. Any invalid file is
// an error naming it.
func Load(userDir string) (*Registry, error) {
	r := &Registry{byID: map[string]Palette{}}
	for _, id := range BuiltinOrder {
		data, err := builtinFS.ReadFile("builtin/" + id + ".toml")
		if err != nil {
			return nil, err
		}
		p, err := parse(id, "built-in", data)
		if err != nil {
			return nil, err
		}
		r.byID[id] = p
		r.order = append(r.order, id)
	}
	if userDir == "" {
		return r, nil
	}
	paths, err := filepath.Glob(filepath.Join(userDir, "*.toml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		id := strings.TrimSuffix(filepath.Base(path), ".toml")
		p, err := parse(id, path, data)
		if err != nil {
			return nil, err
		}
		if _, exists := r.byID[id]; !exists {
			r.order = append(r.order, id)
		}
		r.byID[id] = p
	}
	return r, nil
}

func parse(id, source string, data []byte) (Palette, error) {
	var p Palette
	md, err := toml.Decode(string(data), &p)
	if err != nil {
		return Palette{}, fmt.Errorf("theme %s: %w", source, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return Palette{}, fmt.Errorf("theme %s: unknown keys: %s", source, strings.Join(keys, ", "))
	}
	for _, r := range p.roles() {
		if err := checkColor(r.color); err != nil {
			return Palette{}, fmt.Errorf("theme %s: %s: %w", source, r.name, err)
		}
	}
	p.ID, p.Source = id, source
	if p.Name == "" {
		p.Name = id
	}
	return p, nil
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func checkColor(c string) error {
	if c == "" || hexColor.MatchString(c) {
		return nil
	}
	if n, err := strconv.Atoi(c); err == nil && n >= 0 && n <= 255 {
		return nil
	}
	return fmt.Errorf("%q is not a #rrggbb color or an ANSI index 0-255", c)
}

func luminance(hex string) float64 {
	channel := func(s string) float64 {
		v, _ := strconv.ParseUint(s, 16, 8)
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(hex[1:3]) + 0.7152*channel(hex[3:5]) + 0.0722*channel(hex[5:7])
}

// Contrast returns the WCAG contrast ratio of two #rrggbb colors. ok is false
// unless both are hex (ANSI colors depend on the terminal's palette).
func Contrast(a, b string) (float64, bool) {
	if !hexColor.MatchString(a) || !hexColor.MatchString(b) {
		return 0, false
	}
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05), true
}

// ReadabilityProblems applies the readability rule. Themes without a
// background use the terminal's, so they can't be checked.
func (p Palette) ReadabilityProblems() []string {
	if p.Background == "" {
		return nil
	}
	var out []string
	check := func(name, fg, bg string, min float64) {
		if c, ok := Contrast(fg, bg); ok && c < min {
			out = append(out, fmt.Sprintf("%s contrast %.2f is below %.1f", name, c, min))
		}
	}
	check("foreground", p.Foreground, p.Background, 4.5)
	check("muted", p.Muted, p.Background, 4.5)
	for _, r := range []role{{"accent", p.Accent}, {"heading", p.Heading}, {"success", p.Success},
		{"warning", p.Warning}, {"error", p.Error}, {"attention", p.Attention}, {"special", p.Special}, {"info", p.Info}} {
		check(r.name, r.color, p.Background, 3.0)
	}
	check("on_accent", p.OnAccent, p.Accent, 4.5)
	check("foreground on selection", p.Foreground, p.Selection, 4.5)
	return out
}
