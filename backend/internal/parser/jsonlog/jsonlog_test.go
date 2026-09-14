package jsonlog

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/parser"
)

func parse(t *testing.T, data string) (*logentry.Entry, error) {
	t.Helper()
	var e logentry.Entry
	e.Reset()
	err := New().Parse(&parser.Input{Data: data}, &e)
	return &e, err
}

func fieldMap(e *logentry.Entry) map[string]string {
	m := map[string]string{}
	for _, f := range e.Fields {
		m[f.Key] = f.Value
	}
	return m
}

func TestParseBriefExample(t *testing.T) {
	e, err := parse(t, `{"timestamp":"2026-09-14T14:30:00Z","host":"server01","level":"error","service":"nginx","message":"connection refused","source_ip":"10.10.10.20"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Time.Equal(time.Date(2026, 9, 14, 14, 30, 0, 0, time.UTC)) || e.Hostname != "server01" || e.Severity != logentry.SeverityError ||
		e.SeveritySource != logentry.SeverityFromLog || e.AppName != "nginx" || e.Message != "connection refused" || e.SourceIP.String() != "10.10.10.20" {
		t.Errorf("core fields: %+v", e)
	}
	if len(e.Fields) != 0 {
		t.Errorf("unexpected dynamic fields: %v", e.Fields)
	}
}

func TestParseFlatteningAndTypes(t *testing.T) {
	e, err := parse(t, `{"msg":"hi","http":{"status":503,"path":"/api"},"tags":["a","b"],"ok":true,"n":null,
		"level":"WARN","user":{"geo":{"country":"GB"}},"big":12345678901234567890,"@timestamp":1789396200.5}`)
	if err != nil {
		t.Fatal(err)
	}
	got := fieldMap(e)
	want := map[string]string{"http.status": "503", "http.path": "/api", "tags": `["a","b"]`, "ok": "true",
		"level": "WARN", "user.geo.country": "GB", "big": "12345678901234567890"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("field %s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["n"]; ok {
		t.Error("null value stored")
	}
	if e.Severity != logentry.SeverityWarning {
		t.Errorf("severity = %s", e.Severity)
	}
	if e.Time.UnixMilli() != 1789396200500 {
		t.Errorf("epoch seconds with fraction: %s", e.Time)
	}
}

func TestAliasPriority(t *testing.T) {
	e, _ := parse(t, `{"msg":"second","message":"first","host":"h2","hostname":"h1","ts":1789396200000}`)
	if e.Message != "first" || e.Hostname != "h1" {
		t.Errorf("alias priority: message=%q hostname=%q", e.Message, e.Hostname)
	}
	got := fieldMap(e)
	if got["msg"] != "second" || got["host"] != "h2" {
		t.Errorf("lower-priority aliases not preserved: %v", got)
	}
	if e.Time.UnixMilli() != 1789396200000 {
		t.Errorf("epoch millis: %s", e.Time)
	}
}

func TestUnparseableValuesPreserved(t *testing.T) {
	e, _ := parse(t, `{"timestamp":"yesterday","level":"loud","source_ip":"not-an-ip"}`)
	if e.TimestampRaw != "yesterday" || !e.Time.IsZero() {
		t.Errorf("timestamp: raw=%q time=%s", e.TimestampRaw, e.Time)
	}
	if e.SeveritySource != logentry.SeverityDefault {
		t.Error("unknown level treated as parsed")
	}
	got := fieldMap(e)
	if got["level"] != "loud" || got["source_ip"] != "not-an-ip" {
		t.Errorf("unparseable aliases not kept: %v", got)
	}
}

func TestRejectsNonObjects(t *testing.T) {
	for _, in := range []string{"", "[1,2]", `"str"`, "{broken", `{"a":1}{"b":2}`, "<14>syslog"} {
		if _, err := parse(t, in); !errors.Is(err, parser.ErrNotThisFormat) {
			t.Errorf("Parse(%q) = %v", in, err)
		}
	}
}

func TestDeepNestingStoredAsJSON(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 20) + `1` + strings.Repeat(`}`, 20)
	e, err := parse(t, `{"deep":`+deep+`}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range e.Fields {
		if strings.Count(f.Key, ".") > maxDepth {
			t.Errorf("key deeper than limit: %s", f.Key)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add(`{"message":"x","level":"error","a":{"b":[1,2]}}`)
	f.Add(`{"ts":1e20,"host":""}`)
	f.Fuzz(func(t *testing.T, data string) {
		var e logentry.Entry
		e.Reset()
		err := New().Parse(&parser.Input{Data: data}, &e)
		if err != nil && !errors.Is(err, parser.ErrNotThisFormat) {
			t.Fatalf("unexpected error type: %v", err)
		}
	})
}
