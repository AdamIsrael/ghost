// Package difflines finds the lines a unified diff shows on its new side:
// the lines GitHub lets a review comment attach to.
package difflines

import (
	"strconv"
	"strings"
)

// Map holds, per file, the new-side line ranges covered by the diff's hunks
// (added and context lines).
type Map map[string][][2]int

// Parse reads a git unified diff with a/ and b/ prefixes. Deleted files and
// binary files have no new-side lines. Inside a hunk it counts the lines the
// header promises, so content lines that look like headers ("+++ x" is an
// added line starting "++ x") are never taken for one.
func Parse(diff string) Map {
	m := Map{}
	var path string
	oldLeft, newLeft := 0, 0 // lines left in the current hunk
	for _, line := range strings.Split(diff, "\n") {
		if oldLeft > 0 || newLeft > 0 {
			switch {
			case strings.HasPrefix(line, "+"):
				newLeft--
			case strings.HasPrefix(line, "-"):
				oldLeft--
			case strings.HasPrefix(line, " "), line == "":
				oldLeft--
				newLeft--
			}
			// "\ No newline at end of file" and anything else: no change.
			continue
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			path = ""
		case strings.HasPrefix(line, "+++ "):
			path = newPath(strings.TrimPrefix(line, "+++ "))
		case strings.HasPrefix(line, "@@ "):
			oldN, newStart, newN, ok := hunk(line)
			if !ok {
				continue
			}
			oldLeft, newLeft = oldN, newN
			if path != "" && newN > 0 {
				m[path] = append(m[path], [2]int{newStart, newStart + newN - 1})
			}
		}
	}
	return m
}

// Has reports whether line of path is on the diff's new side.
func (m Map) Has(path string, line int) bool {
	for _, r := range m[path] {
		if line >= r[0] && line <= r[1] {
			return true
		}
	}
	return false
}

// newPath turns a "+++" target into the file path; "" for /dev/null.
func newPath(s string) string {
	s = strings.TrimRight(s, "\t")
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			s = u
		}
	}
	if s == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(s, "b/")
}

// hunk parses "@@ -a,b +c,d @@": the old count, and the new start and count.
// A missing count means 1.
func hunk(header string) (oldN, newStart, newN int, ok bool) {
	fields := strings.Fields(header)
	if len(fields) < 3 || !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return 0, 0, 0, false
	}
	_, oldN, ok1 := span(strings.TrimPrefix(fields[1], "-"))
	newStart, newN, ok2 := span(strings.TrimPrefix(fields[2], "+"))
	return oldN, newStart, newN, ok1 && ok2
}

func span(spec string) (start, count int, ok bool) {
	s, c, hasCount := strings.Cut(spec, ",")
	start, err := strconv.Atoi(s)
	if err != nil {
		return 0, 0, false
	}
	count = 1
	if hasCount {
		if count, err = strconv.Atoi(c); err != nil {
			return 0, 0, false
		}
	}
	return start, count, true
}
