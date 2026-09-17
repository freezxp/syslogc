// Package filter defines the backend-neutral filter AST (ADR-0006). Storage
// adapters compile it to their native query language; nothing else builds
// native query text from user input.
package filter

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// Op is a filter operator.
type Op string

const (
	And        Op = "and"
	Or         Op = "or"
	Not        Op = "not"
	Text       Op = "text"
	Eq         Op = "eq"
	Ne         Op = "ne"
	Contains   Op = "contains"
	StartsWith Op = "starts_with"
	Regex      Op = "regex"
	Gt         Op = "gt"
	Gte        Op = "gte"
	Lt         Op = "lt"
	Lte        Op = "lte"
	CIDR       Op = "cidr"
	In         Op = "in"
	NotIn      Op = "not_in"
	Exists     Op = "exists"
	NotExists  Op = "not_exists"
)

// Limits bound AST size.
const (
	MaxDepth      = 16
	MaxNodes      = 500
	MaxArgs       = 100
	MaxValues     = 1000
	MaxFieldBytes = 256
	MaxValueBytes = 4096
	MaxRegexBytes = 1024
)

// Expr is one AST node. Exactly the fields relevant to Op are set.
type Expr struct {
	Op     Op       `json:"op"`
	Args   []*Expr  `json:"args,omitempty"`
	Arg    *Expr    `json:"arg,omitempty"`
	Field  string   `json:"field,omitempty"`
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
}

// UnmarshalJSON accepts numeric and boolean JSON values for `value`/`values`
// and stores them as strings.
func (e *Expr) UnmarshalJSON(b []byte) error {
	var raw struct {
		Op     Op                `json:"op"`
		Args   []*Expr           `json:"args"`
		Arg    *Expr             `json:"arg"`
		Field  string            `json:"field"`
		Value  json.RawMessage   `json:"value"`
		Values []json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	e.Op, e.Args, e.Arg, e.Field = raw.Op, raw.Args, raw.Arg, raw.Field
	if len(raw.Value) > 0 {
		v, err := scalar(raw.Value)
		if err != nil {
			return fmt.Errorf("value: %w", err)
		}
		e.Value = v
	}
	for i, rv := range raw.Values {
		v, err := scalar(rv)
		if err != nil {
			return fmt.Errorf("values[%d]: %w", i, err)
		}
		e.Values = append(e.Values, v)
	}
	return nil
}

func scalar(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	switch raw[0] {
	case '"':
		var s string
		err := json.Unmarshal(raw, &s)
		return s, err
	case '[', '{':
		return "", errors.New("must be a string, number or boolean")
	}
	return string(raw), nil
}

// ValidationError describes an invalid AST node by JSON pointer.
type ValidationError struct {
	Pointer string
	Message string
}

func (e *ValidationError) Error() string { return e.Pointer + ": " + e.Message }

// Validate checks structure and limits. A nil expression is valid (match all).
func Validate(e *Expr) error {
	if e == nil {
		return nil
	}
	nodes := 0
	return validate(e, "/filter", 1, &nodes)
}

func validate(e *Expr, ptr string, depth int, nodes *int) error {
	if e == nil {
		return &ValidationError{ptr, "expression is null"}
	}
	*nodes++
	if *nodes > MaxNodes {
		return &ValidationError{ptr, fmt.Sprintf("filter has more than %d nodes", MaxNodes)}
	}
	if depth > MaxDepth {
		return &ValidationError{ptr, fmt.Sprintf("filter is nested deeper than %d levels", MaxDepth)}
	}
	fail := func(format string, args ...any) error { return &ValidationError{ptr, fmt.Sprintf(format, args...)} }

	switch e.Op {
	case And, Or:
		if len(e.Args) == 0 || len(e.Args) > MaxArgs {
			return fail("%s needs 1–%d arguments", e.Op, MaxArgs)
		}
		for i, a := range e.Args {
			if err := validate(a, fmt.Sprintf("%s/args/%d", ptr, i), depth+1, nodes); err != nil {
				return err
			}
		}
		return nil
	case Not:
		return validate(e.Arg, ptr+"/arg", depth+1, nodes)
	case Text:
		if e.Value == "" || len(e.Value) > MaxValueBytes {
			return fail("text value must be 1–%d bytes", MaxValueBytes)
		}
		return nil
	}

	if err := validateField(e.Field); err != nil {
		return &ValidationError{ptr + "/field", err.Error()}
	}
	switch e.Op {
	case Eq, Ne, Contains, StartsWith:
		if len(e.Value) > MaxValueBytes {
			return fail("value longer than %d bytes", MaxValueBytes)
		}
		if (e.Op == Contains || e.Op == StartsWith) && e.Value == "" {
			return fail("%s needs a non-empty value", e.Op)
		}
	case Regex:
		if e.Value == "" || len(e.Value) > MaxRegexBytes {
			return fail("regex must be 1–%d bytes", MaxRegexBytes)
		}
		if _, err := regexp.Compile(e.Value); err != nil {
			return fail("invalid regular expression: %v", err)
		}
	case Gt, Gte, Lt, Lte:
		if _, err := strconv.ParseFloat(e.Value, 64); err != nil {
			return fail("%s needs a numeric value", e.Op)
		}
	case CIDR:
		if _, err := netip.ParsePrefix(e.Value); err != nil {
			return fail("invalid CIDR %q", e.Value)
		}
	case In, NotIn:
		if len(e.Values) == 0 || len(e.Values) > MaxValues {
			return fail("%s needs 1–%d values", e.Op, MaxValues)
		}
		for _, v := range e.Values {
			if len(v) > MaxValueBytes {
				return fail("value longer than %d bytes", MaxValueBytes)
			}
		}
	case Exists, NotExists:
	case "":
		return fail("op is required")
	default:
		return fail("unknown op %q", e.Op)
	}
	return nil
}

func validateField(f string) error {
	switch {
	case f == "":
		return errors.New("field is required")
	case len(f) > MaxFieldBytes:
		return fmt.Errorf("field name longer than %d bytes", MaxFieldBytes)
	case strings.ContainsAny(f, "\x00\n\r"):
		return errors.New("field name contains control characters")
	}
	return nil
}

// AndAll combines expressions with AND, skipping nils.
func AndAll(exprs ...*Expr) *Expr {
	var args []*Expr
	for _, e := range exprs {
		if e != nil {
			args = append(args, e)
		}
	}
	switch len(args) {
	case 0:
		return nil
	case 1:
		return args[0]
	}
	return &Expr{Op: And, Args: args}
}

// ValidateField reports whether a field name may be used in a query.
func ValidateField(f string) error { return validateField(f) }
