// Package extract pulls fields out of a log message with regular
// expressions, so values buried in free text can be filtered and aggregated
// like any other field.
//
// Patterns are RE2 (Go's regexp), which has no backtracking: a pattern
// cannot blow up on hostile input. Each rule names the fields it produces
// through capture group names, and rules are tried in order until one
// matches.
package extract

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/freezxp/syslogc/backend/internal/logentry"
)

// Limits on what a source may configure.
const (
	MaxRules         = 16
	MaxPatternLen    = 4096
	MaxFieldsPerRule = 32
)

// Rule is one pattern and the fields it produces.
type Rule struct {
	// Name identifies the rule in metrics and errors.
	Name string
	// Contains is an optional literal that must appear in the message before
	// the regular expression is tried. It is a cheap filter for sources that
	// carry several log shapes.
	Contains string
	// Prefix is prepended to every field name the rule produces.
	Prefix string
	re     *regexp.Regexp
	names  []string
}

// Extractor applies rules to an entry.
type Extractor struct {
	rules []Rule
}

// Config describes one rule before compilation. The JSON names match the
// rule as it is written in a source's configuration, because presets are
// served in this shape and pasted straight into a source.
type Config struct {
	Name     string `json:"name,omitempty"`
	Contains string `json:"contains,omitempty"`
	Regex    string `json:"regex"`
	Prefix   string `json:"prefix,omitempty"`
}

// New compiles rules. Every rule must have at least one named capture group,
// because unnamed groups produce no field.
func New(cfgs []Config) (*Extractor, error) {
	if len(cfgs) > MaxRules {
		return nil, fmt.Errorf("at most %d extract rules are allowed", MaxRules)
	}
	e := &Extractor{}
	for i, c := range cfgs {
		name := c.Name
		if name == "" {
			name = fmt.Sprintf("rule-%d", i+1)
		}
		if len(c.Regex) > MaxPatternLen {
			return nil, fmt.Errorf("extract %s: pattern longer than %d bytes", name, MaxPatternLen)
		}
		re, err := regexp.Compile(c.Regex)
		if err != nil {
			return nil, fmt.Errorf("extract %s: %w", name, err)
		}
		named := 0
		for _, g := range re.SubexpNames() {
			if g == "" {
				continue
			}
			if err := validFieldName(g); err != nil {
				return nil, fmt.Errorf("extract %s: capture group %q: %w", name, g, err)
			}
			named++
		}
		if named == 0 {
			return nil, fmt.Errorf("extract %s: the pattern has no named capture groups, so it produces no fields", name)
		}
		if named > MaxFieldsPerRule {
			return nil, fmt.Errorf("extract %s: %d capture groups, at most %d are allowed", name, named, MaxFieldsPerRule)
		}
		if c.Prefix != "" {
			if err := validFieldName(strings.TrimSuffix(c.Prefix, ".")); err != nil {
				return nil, fmt.Errorf("extract %s: prefix %q: %w", name, c.Prefix, err)
			}
		}
		e.rules = append(e.rules, Rule{Name: name, Contains: c.Contains, Prefix: c.Prefix, re: re, names: re.SubexpNames()})
	}
	return e, nil
}

// Rules returns the compiled rule names, in order.
func (e *Extractor) Rules() []string {
	if e == nil {
		return nil
	}
	out := make([]string, 0, len(e.rules))
	for _, r := range e.rules {
		out = append(out, r.Name)
	}
	return out
}

// Apply adds fields from the first matching rule to the entry and returns
// that rule's name, or "" when nothing matched. Empty captures are skipped:
// an absent optional value should not create an empty field.
func (e *Extractor) Apply(entry *logentry.Entry) string {
	if e == nil || len(e.rules) == 0 || entry.Message == "" {
		return ""
	}
	for i := range e.rules {
		r := &e.rules[i]
		if r.Contains != "" && !strings.Contains(entry.Message, r.Contains) {
			continue
		}
		m := r.re.FindStringSubmatch(entry.Message)
		if m == nil {
			continue
		}
		for gi, value := range m {
			name := r.names[gi]
			if name == "" || value == "" {
				continue
			}
			entry.AddField(r.Prefix+name, value)
		}
		return r.Name
	}
	return ""
}

// validFieldName rejects names that would be unusable as log fields.
func validFieldName(name string) error {
	switch {
	case name == "":
		return errors.New("empty")
	case len(name) > 128:
		return errors.New("longer than 128 characters")
	case strings.HasPrefix(name, "_"):
		return errors.New("names starting with _ are reserved")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.':
		default:
			return fmt.Errorf("unexpected character %q (use letters, digits, _ and .)", r)
		}
	}
	return nil
}
