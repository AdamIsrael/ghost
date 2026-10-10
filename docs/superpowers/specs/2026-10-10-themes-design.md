# Ghost Themes — Design

## Context
The operator finds the TUI hard to read (originally: dim ANSI 8 gray on a dark terminal) and wants more color and more themes, especially Tokyo Night. M7's first pass (branch `m7-themes`, unmerged) added five fixed themes (`auto`, `dark`, `light`, `high-contrast`, `mono`), the `theme` config key, the `t` key and `NO_COLOR`. This design replaces its fixed style set with **color roles** and **theme files**, and ships 14 built-ins.

Decisions (operator, 2026-10-10): color carries meaning (mockup B/C: status, active view, headings, +/− counts, cost, warnings; soft row highlight); extra themes come from theme files; built-ins include Tokyo Night (night, storm, day), Catppuccin (Mocha, Latte), Dracula, Nord, Gruvbox dark, Solarized (dark, light), plus the existing dark, light, high-contrast and mono.

## Color roles
A theme is a set of named roles. Every role is optional; an empty role means "the terminal's own color" (or plain/bold/reverse for structure).

| Role | Used for |
|---|---|
| `background`, `foreground` | Whole-screen colors while Ghost runs (via Bubble Tea's view colors). Omit to keep the terminal's. |
| `muted` | Key hints, URLs, timestamps, empty states, draft status |
| `border` | The list/detail divider |
| `accent` | Active view tab background; headings fall back to it |
| `on_accent` | Text on the active tab (needed when `accent` is set) |
| `heading` | `── Section ──` headings and the detail title |
| `selection` | Background of the selected PR row (empty → reverse video) |
| `success` | Ready status, CI passing, `+` additions, "Approved" approval line |
| `warning` | Waiting on CI, spend amount, approval waiting |
| `error` | CI failing, `−` deletions, warnings/errors in the status bar, blocked/failed |
| `attention` | Changes requested |
| `special` | Approved status, pending review/approval headings |
| `info` | Authors, links to people, "posted" audit kind |

Status → role: Ready=success · Draft=muted · Waiting on CI=warning · CI failing=error · Changes requested=attention · Approved=special.

## Theme files
TOML, one theme per file. Colors are `"#rrggbb"` (truecolor, automatically reduced on 256/16-color terminals) or an ANSI index `"0"`–`"255"` (follows the terminal's palette).

```toml
# ~/.config/ghost/themes/my-theme.toml
name = "My theme"          # display name; the file stem is the id used in config
background = "#1a1b26"
foreground = "#c0caf5"
muted = "#737aa2"
border = "#3b4261"
accent = "#7aa2f7"
on_accent = "#1a1b26"
heading = "#7aa2f7"
selection = "#292e42"
success = "#9ece6a"
warning = "#e0af68"
error = "#f7768e"
attention = "#ff9e64"
special = "#bb9af7"
info = "#7dcfff"
```

- Built-ins are embedded TOML files in the same format (`internal/theme/builtin/*.toml`), so copying one is the starting point for a custom theme.
- User themes: `<config dir>/themes/<id>.toml`. A user file with a built-in's id overrides it.
- Any invalid theme file (unknown key, bad color, unreadable TOML) is a startup error naming the file, selected or not — the same strictness as config.toml, so mistakes surface immediately.
- `theme = "<id>"` in config.toml; `auto` keeps its meaning (terminal background → `dark` or `light`, neutral until it answers). `NO_COLOR` → `mono`.

## Built-ins (ids)
`dark`, `light`, `high-contrast`, `mono` (ANSI/terminal colors; no background) and `tokyo-night`, `tokyo-night-storm`, `tokyo-night-day`, `catppuccin-mocha`, `catppuccin-latte`, `dracula`, `nord`, `gruvbox-dark`, `solarized-dark`, `solarized-light` (truecolor, with background). Palettes use each project's published colors; where a project's "comment" color is too dim for UI text, `muted` uses its next-brighter gray so it meets the contrast rule below.

`dark` and `light` gain role colors (ANSI) so they're no longer near-monochrome, as in mockup B.

## Readability rule
For every theme with a `background`: `foreground` and `muted` must have a WCAG contrast ratio ≥ 4.5 against it; `accent`, `heading` and the status roles ≥ 3.0 (WCAG's threshold for UI components — they color icons and short labels); `on_accent` ≥ 4.5 against `accent`; `foreground` ≥ 4.5 against `selection`. Built-ins are tested; where a published palette color fails, the built-in uses the nearest shade of the same hue that passes and says so in its header. User themes aren't enforced, but `ghost --check` warns when the selected user theme fails.

## TUI behavior
- `t` cycles all themes: built-ins in the order above, then user themes by id. The flash names the config line to keep it.
- Rendering reads styles from the active theme only (no package-level mutable styles).

## Out of scope
base16/iTerm imports, a `ghost theme export` command, per-role bold/italic settings, editing themes in the TUI, separate `auto` dark/light theme choices.

## Testing
Role parsing and validation (hex, ANSI, empty, unknown key, bad file path in error); user overrides built-in; all built-ins load; contrast test; status→role mapping in rendered rows (by inspecting styles); `t` cycles built-ins then user themes; `NO_COLOR` and `auto` unchanged; theme with background sets the view colors.
