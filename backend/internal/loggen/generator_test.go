package loggen

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/parser"
	"github.com/freezxp/syslogc/backend/internal/parser/rfc3164"
	"github.com/freezxp/syslogc/backend/internal/parser/rfc5424"
)

func TestGeneratedMessagesParse(t *testing.T) {
	now := time.Now()
	for _, format := range []Format{RFC5424, RFC3164} {
		t.Run(string(format), func(t *testing.T) {
			g, err := New(Options{
				Formats: map[Format]int{format: 1}, Hosts: 20, CustomFields: 3,
				MinMessageBytes: 200, RunID: "run42", Seed: 7,
			}, 1)
			if err != nil {
				t.Fatal(err)
			}
			var p parser.Parser = rfc5424.New()
			if format == RFC3164 {
				p = rfc3164.New()
			}
			hosts := map[string]bool{}
			for seq := range uint64(500) {
				msg := string(g.Append(nil, seq, now))
				if got := parser.Detect(msg); got.String() != string(format) {
					t.Fatalf("Detect(%q) = %s", msg, got)
				}
				var e logentry.Entry
				e.Reset()
				if err := p.Parse(&parser.Input{Data: msg, ReceivedAt: now, Location: time.UTC}, &e); err != nil || e.ParseError != "" {
					t.Fatalf("generated message does not parse: %q: %v %s", msg, err, e.ParseError)
				}
				if e.Hostname == "" || e.AppName == "" || e.ProcessID == "" {
					t.Fatalf("missing header fields in %q: %+v", msg, e)
				}
				if !strings.HasSuffix(e.Message, " run=run42 seq="+itoa(seq)) {
					t.Fatalf("run/seq marker missing: %q", e.Message)
				}
				if len(e.Message) < 200 {
					t.Fatalf("message shorter than MinMessageBytes: %d", len(e.Message))
				}
				if format == RFC5424 && len(e.Fields) != 3 {
					t.Fatalf("custom fields = %d", len(e.Fields))
				}
				hosts[e.Hostname] = true
			}
			if len(hosts) < 15 {
				t.Errorf("host cardinality too low: %d", len(hosts))
			}
		})
	}
}

func TestGeneratorDeterministic(t *testing.T) {
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	a, _ := New(Options{Seed: 1, Formats: map[Format]int{RFC5424: 1, RFC3164: 1}}, 3)
	b, _ := New(Options{Seed: 1, Formats: map[Format]int{RFC5424: 1, RFC3164: 1}}, 3)
	for i := range uint64(100) {
		if x, y := string(a.Append(nil, i, now)), string(b.Append(nil, i, now)); x != y {
			t.Fatalf("same seed produced different output:\n%s\n%s", x, y)
		}
	}
}

func TestSeverityWeights(t *testing.T) {
	g, err := New(Options{Seed: 2, SeverityWeights: map[string]int{"error": 1}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for range 200 {
		msg := string(g.Append(nil, 0, time.Now()))
		pri, _, ok := parser.ParsePRI(msg)
		if !ok || pri&7 != 3 {
			t.Fatalf("severity not honoured: %q", msg)
		}
	}
	if _, err := New(Options{SeverityWeights: map[string]int{"loud": 1}}, 0); err == nil {
		t.Error("unknown severity accepted")
	}
}

func TestParseWeights(t *testing.T) {
	w, err := ParseWeights("rfc5424=60, rfc3164=40")
	if err != nil || w["rfc5424"] != 60 || w["rfc3164"] != 40 {
		t.Errorf("ParseWeights = %v, %v", w, err)
	}
	if _, err := ParseWeights("a=x"); err == nil {
		t.Error("invalid weight accepted")
	}
}

func itoa(n uint64) string { return strconv.FormatUint(n, 10) }

func BenchmarkAppend(b *testing.B) {
	g, _ := New(Options{Seed: 1, Formats: map[Format]int{RFC5424: 1}, CustomFields: 3, RunID: "bench"}, 0)
	buf := make([]byte, 0, 1024)
	now := time.Now()
	b.ReportAllocs()
	for i := uint64(0); b.Loop(); i++ {
		buf = g.Append(buf[:0], i, now)
	}
}
