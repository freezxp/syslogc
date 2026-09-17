package victorialogs

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

// This file is the ONLY place that turns user-controlled values into
// LogsQL. Every field name and value is emitted as a quoted LogsQL string.

// fieldAliases maps API field names to VictoriaLogs reserved names.
var fieldAliases = map[string]string{
	"message":   fieldMsg,
	"timestamp": fieldTime,
}

func storageField(name string) string {
	if a, ok := fieldAliases[name]; ok {
		return a
	}
	return name
}

// quote returns a LogsQL double-quoted string literal.
func quote(s string) string { return strconv.Quote(s) }

// CompileFilter compiles an AST to a LogsQL filter expression. A nil
// expression compiles to "*" (match all).
func CompileFilter(e *filter.Expr) (string, error) {
	if e == nil {
		return "*", nil
	}
	if err := filter.Validate(e); err != nil {
		return "", err
	}
	var b strings.Builder
	if err := compile(&b, e); err != nil {
		return "", err
	}
	return b.String(), nil
}

func compile(b *strings.Builder, e *filter.Expr) error {
	f := quote(storageField(e.Field))
	switch e.Op {
	case filter.And, filter.Or:
		sep := " AND "
		if e.Op == filter.Or {
			sep = " OR "
		}
		b.WriteByte('(')
		for i, a := range e.Args {
			if i > 0 {
				b.WriteString(sep)
			}
			if err := compile(b, a); err != nil {
				return err
			}
		}
		b.WriteByte(')')
	case filter.Not:
		b.WriteString("NOT (")
		if err := compile(b, e.Arg); err != nil {
			return err
		}
		b.WriteByte(')')
	case filter.Text:
		// Case-insensitive phrase filter on the message field.
		fmt.Fprintf(b, "%s:i(%s)", quote(fieldMsg), quote(e.Value))
	case filter.Eq:
		fmt.Fprintf(b, "%s:=%s", f, quote(e.Value))
	case filter.Ne:
		fmt.Fprintf(b, "NOT %s:=%s", f, quote(e.Value))
	case filter.Contains:
		// Case-insensitive substring match via an escaped regexp.
		fmt.Fprintf(b, "%s:~%s", f, quote("(?i)"+regexp.QuoteMeta(e.Value)))
	case filter.StartsWith:
		fmt.Fprintf(b, "%s:~%s", f, quote("^"+regexp.QuoteMeta(e.Value)))
	case filter.Regex:
		fmt.Fprintf(b, "%s:~%s", f, quote(e.Value))
	case filter.Gt, filter.Gte, filter.Lt, filter.Lte:
		op := map[filter.Op]string{filter.Gt: ">", filter.Gte: ">=", filter.Lt: "<", filter.Lte: "<="}[e.Op]
		n, err := strconv.ParseFloat(e.Value, 64)
		if err != nil {
			return fmt.Errorf("%s needs a numeric value", e.Op)
		}
		fmt.Fprintf(b, "%s:%s%s", f, op, strconv.FormatFloat(n, 'f', -1, 64))
	case filter.CIDR:
		fn := "ipv4_range"
		if strings.Contains(e.Value, ":") {
			return errors.New("cidr: IPv6 ranges are not supported by VictoriaLogs")
		}
		fmt.Fprintf(b, "%s:%s(%s)", f, fn, quote(e.Value))
	case filter.In, filter.NotIn:
		if e.Op == filter.NotIn {
			b.WriteString("NOT ")
		}
		b.WriteString(f)
		b.WriteString(":in(")
		for i, v := range e.Values {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(quote(v))
		}
		b.WriteByte(')')
	case filter.Exists:
		fmt.Fprintf(b, "%s:*", f)
	case filter.NotExists:
		fmt.Fprintf(b, "NOT %s:*", f)
	default:
		return fmt.Errorf("unsupported op %q", e.Op)
	}
	return nil
}

// SplitNative splits native LogsQL into its filter part and its pipe part at
// the first top-level '|' outside string literals. pipes excludes the
// leading '|'; both parts are trimmed.
func SplitNative(text string) (filterPart, pipes string, err error) {
	var quoteCh byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quoteCh != 0 {
			switch {
			case c == '\\' && quoteCh != '`':
				i++
			case c == quoteCh:
				quoteCh = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			quoteCh = c
		case '|':
			filterPart, pipes = strings.TrimSpace(text[:i]), strings.TrimSpace(text[i+1:])
			if pipes == "" {
				return "", "", errors.New("expected a pipe after '|'")
			}
			return filterPart, pipes, nil
		}
	}
	if quoteCh != 0 {
		return "", "", errors.New("unterminated string literal")
	}
	return strings.TrimSpace(text), "", nil
}

// buildSelection returns the LogsQL filter for a selection (AST AND native
// filter part) and the native pipe part.
func buildSelection(sel storage.Selection) (filterExpr, pipes string, err error) {
	ast, err := CompileFilter(sel.Filter)
	if err != nil {
		return "", "", err
	}
	native := ""
	if sel.Native != nil {
		if sel.Native.Dialect != dialectLogsQL {
			return "", "", fmt.Errorf("%w: %q", storage.ErrUnsupportedDialect, sel.Native.Dialect)
		}
		native, pipes, err = SplitNative(sel.Native.Text)
		if err != nil {
			return "", "", &storage.QueryError{StatusCode: 400, Message: err.Error(), UserFacing: true}
		}
	}
	switch {
	case native == "" || native == "*":
		return ast, pipes, nil
	case ast == "*":
		return "(" + native + ")", pipes, nil
	}
	return ast + " AND (" + native + ")", pipes, nil
}

func escapeRegexp(s string) string { return regexp.QuoteMeta(s) }
