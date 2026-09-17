package victorialogs

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

func parseExpr(t *testing.T, js string) *filter.Expr {
	t.Helper()
	var e filter.Expr
	if err := json.Unmarshal([]byte(js), &e); err != nil {
		t.Fatalf("unmarshal %s: %v", js, err)
	}
	return &e
}

func TestCompileFilter(t *testing.T) {
	tests := []struct {
		ast  string
		want string
	}{
		{`{"op":"text","value":"connection refused"}`, `"_msg":i("connection refused")`},
		{`{"op":"eq","field":"hostname","value":"fw01"}`, `"hostname":="fw01"`},
		{`{"op":"ne","field":"hostname","value":"fw01"}`, `NOT "hostname":="fw01"`},
		{`{"op":"contains","field":"message","value":"a.b*"}`, `"_msg":~"(?i)a\\.b\\*"`},
		{`{"op":"starts_with","field":"app_name","value":"ssh"}`, `"app_name":~"^ssh"`},
		{`{"op":"regex","field":"app_name","value":"^(sshd|cron)$"}`, `"app_name":~"^(sshd|cron)$"`},
		{`{"op":"gte","field":"severity_code","value":3}`, `"severity_code":>=3`},
		{`{"op":"lt","field":"http.status","value":"499.5"}`, `"http.status":<499.5`},
		{`{"op":"cidr","field":"source_ip","value":"10.0.0.0/8"}`, `"source_ip":ipv4_range("10.0.0.0/8")`},
		{`{"op":"in","field":"severity","values":["error","critical"]}`, `"severity":in("error","critical")`},
		{`{"op":"not_in","field":"severity","values":["info"]}`, `NOT "severity":in("info")`},
		{`{"op":"exists","field":"vpn_name"}`, `"vpn_name":*`},
		{`{"op":"not_exists","field":"vpn_name"}`, `NOT "vpn_name":*`},
		{`{"op":"and","args":[{"op":"eq","field":"a","value":"1"},{"op":"or","args":[{"op":"exists","field":"b"},{"op":"not","arg":{"op":"text","value":"x"}}]}]}`,
			`("a":="1" AND ("b":* OR NOT ("_msg":i("x"))))`},
		{`{"op":"eq","field":"weird \"key\" | :","value":"\") OR hostname:* OR (\""}`, `"weird \"key\" | :":="\") OR hostname:* OR (\""`},
	}
	for _, tt := range tests {
		got, err := CompileFilter(parseExpr(t, tt.ast))
		if err != nil {
			t.Errorf("CompileFilter(%s): %v", tt.ast, err)
			continue
		}
		if got != tt.want {
			t.Errorf("CompileFilter(%s)\n got: %s\nwant: %s", tt.ast, got, tt.want)
		}
	}
	if got, _ := CompileFilter(nil); got != "*" {
		t.Errorf("nil filter = %q", got)
	}
}

func TestCompileFilterRejectsInvalid(t *testing.T) {
	for _, js := range []string{
		`{"op":"eq","value":"x"}`,
		`{"op":"gt","field":"a","value":"abc"}`,
		`{"op":"regex","field":"a","value":"("}`,
		`{"op":"cidr","field":"a","value":"nope"}`,
		`{"op":"cidr","field":"a","value":"2001:db8::/32"}`,
		`{"op":"and","args":[]}`,
		`{"op":"bogus","field":"a"}`,
		`{"op":"in","field":"a","values":[]}`,
		`{"op":"not"}`,
	} {
		if _, err := CompileFilter(parseExpr(t, js)); err == nil {
			t.Errorf("CompileFilter(%s) accepted invalid input", js)
		}
	}
	var deep strings.Builder
	for range filter.MaxDepth + 1 {
		deep.WriteString(`{"op":"not","arg":`)
	}
	deep.WriteString(`{"op":"exists","field":"a"}`)
	deep.WriteString(strings.Repeat("}", filter.MaxDepth+1))
	if _, err := CompileFilter(parseExpr(t, deep.String())); err == nil {
		t.Error("over-deep filter accepted")
	}
}

func TestSplitNative(t *testing.T) {
	tests := []struct{ in, filter, pipes string }{
		{`hostname:fw01`, `hostname:fw01`, ``},
		{`error | stats count()`, `error`, `stats count()`},
		{`_msg:~"a|b" | top 5 by (x)`, `_msg:~"a|b"`, `top 5 by (x)`},
		{`"escaped \" | quote" | limit 1`, `"escaped \" | quote"`, `limit 1`},
		{"`raw | x` and 'single | y'", "`raw | x` and 'single | y'", ``},
	}
	for _, tt := range tests {
		f, p, err := SplitNative(tt.in)
		if err != nil || f != tt.filter || p != tt.pipes {
			t.Errorf("SplitNative(%q) = %q, %q, %v", tt.in, f, p, err)
		}
	}
	for _, bad := range []string{`"unterminated | x`, `error |`, `error |  `} {
		if _, _, err := SplitNative(bad); err == nil {
			t.Errorf("SplitNative(%q) accepted", bad)
		}
	}
}

func TestBuildSelection(t *testing.T) {
	b := &Backend{}
	sel := storage.Selection{
		Filter: parseExpr(t, `{"op":"eq","field":"severity","value":"error"}`),
		Native: &storage.NativeQuery{Dialect: "logsql", Text: `app_name:sshd | stats count()`},
	}
	got, pipes, err := b.Describe(sel)
	if err != nil || !pipes || got != `"severity":="error" AND (app_name:sshd) | stats count()` {
		t.Errorf("Describe = %q, %v, %v", got, pipes, err)
	}
	got, pipes, _ = b.Describe(storage.Selection{Native: &storage.NativeQuery{Dialect: "logsql", Text: "  "}})
	if got != "*" || pipes {
		t.Errorf("empty native = %q", got)
	}
	if _, _, err := b.Describe(storage.Selection{Native: &storage.NativeQuery{Dialect: "sql", Text: "x"}}); !errors.Is(err, storage.ErrUnsupportedDialect) {
		t.Errorf("dialect: %v", err)
	}
}

func FuzzCompileFilterValue(f *testing.F) {
	f.Add("fw01", "hostname")
	f.Add(`") OR x:* OR ("`, `we"ird`)
	f.Fuzz(func(t *testing.T, value, field string) {
		e := &filter.Expr{Op: filter.Eq, Field: field, Value: value}
		out, err := CompileFilter(e)
		if err != nil {
			return
		}
		// The compiled filter must be exactly: quoted field, ":=", quoted value.
		fp, vp, ok := strings.Cut(out, ":=")
		for ok && !isQuoted(fp) {
			var rest string
			rest, vp, ok = strings.Cut(vp, ":=")
			fp = fp + ":=" + rest
		}
		if !ok || !isQuoted(fp) || !isQuoted(vp) {
			t.Fatalf("compiled filter is not two quoted literals: %s", out)
		}
		if _, pipes, err := SplitNative(out); err != nil || pipes != "" {
			t.Fatalf("compiled filter leaks a pipe or breaks quoting: %s (%v)", out, err)
		}
	})
}

func isQuoted(s string) bool {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return false
	}
	escaped := false
	for i := 1; i < len(s)-1; i++ {
		switch {
		case escaped:
			escaped = false
		case s[i] == '\\':
			escaped = true
		case s[i] == '"':
			return false
		}
	}
	return !escaped
}
