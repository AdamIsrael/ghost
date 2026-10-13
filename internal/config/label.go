package config

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// LabelFor returns the review label that applies to v: the view's own label
// if set ("" meaning none), otherwise review_label. "" means no label gate.
func (c *Config) LabelFor(v View) string {
	if v.Label != nil {
		return *v.Label
	}
	return c.ReviewLabel
}

// checkLabel enforces GitHub's label limits. "" is valid: it turns the label off.
func checkLabel(s string) error {
	switch {
	case s == "":
		return nil
	case strings.TrimSpace(s) != s:
		return errors.New("must not start or end with spaces")
	case utf8.RuneCountInString(s) > 50:
		return errors.New("must be at most 50 characters")
	case strings.Contains(s, ","):
		return errors.New("must not contain commas")
	}
	return nil
}
