#!/bin/sh
# Regenerates the README screenshots in docs/screenshots/ from the real TUI
# with made-up data (internal/tui/screenshot_test.go).
# Needs freeze: go install github.com/charmbracelet/freeze@v0.2.2
# freeze reads stdin when it isn't a terminal, hence </dev/null below.
set -eu
cd "$(dirname "$0")/.."
out=docs/screenshots
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
GHOST_SCREENSHOT_DIR="$tmp" go test ./internal/tui -run '^TestScreenshot$' -count=1
for f in "$tmp"/*.ansi; do
	name=$(basename "$f" .ansi)
	bg=$(sed -n 's/^background = "\(.*\)"/\1/p' "internal/theme/builtin/$name.toml")
	freeze "$f" --language ansi --background "$bg" --window --padding 20 \
		--border.radius 8 --font.size 14 -o "$out/$name.svg" </dev/null
done
