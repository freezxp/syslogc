package rfc5424

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/parser"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func parse(t *testing.T, data string, short bool) (*logentry.Entry, error) {
	t.Helper()
	var e logentry.Entry
	e.Reset()
	err := New().Parse(&parser.Input{Data: data, SDShort: short}, &e)
	return &e, err
}

var entryCmp = cmp.Options{
	cmpopts.EquateEmpty(),
	cmpopts.IgnoreFields(logentry.Entry{}, "ReceivedAt"),
	cmpopts.EquateComparable(netip.Addr{}),
	cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) }),
}

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		short bool
		want  func(t *testing.T) logentry.Entry
	}{
		{
			name: "RFC 5424 example 1 (no SD, BOM-less)",
			in:   `<34>1 2003-10-11T22:14:15.003Z mymachine.example.com su - ID47 - 'su root' failed for lonvick on /dev/pts/8`,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Time: mustTime(t, "2003-10-11T22:14:15.003Z"), Facility: 4, Severity: 2,
					Hostname: "mymachine.example.com", AppName: "su", MessageID: "ID47",
					Message: "'su root' failed for lonvick on /dev/pts/8",
				}
			},
		},
		{
			name: "RFC 5424 example 2 (offset, NILVALUE hostname-less fields)",
			in:   `<165>1 2003-08-24T05:14:15.000003-07:00 192.0.2.1 myproc 8710 - - %% It's time to make the do-nuts.`,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Time: mustTime(t, "2003-08-24T05:14:15.000003-07:00"), Facility: 20, Severity: 5,
					Hostname: "192.0.2.1", AppName: "myproc", ProcessID: "8710",
					Message: "%% It's time to make the do-nuts.",
				}
			},
		},
		{
			name: "RFC 5424 example 3 (SD and BOM message)",
			in:   "<165>1 2003-10-11T22:14:15.003Z mymachine.example.com evntslog - ID47 [exampleSDID@32473 iut=\"3\" eventSource=\"Application\" eventID=\"1011\"] \ufeffAn application event log entry...",
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Time: mustTime(t, "2003-10-11T22:14:15.003Z"), Facility: 20, Severity: 5,
					Hostname: "mymachine.example.com", AppName: "evntslog", MessageID: "ID47",
					Message: "An application event log entry...",
					Fields: []logentry.Field{
						{Key: "sd.exampleSDID@32473.iut", Value: "3"},
						{Key: "sd.exampleSDID@32473.eventSource", Value: "Application"},
						{Key: "sd.exampleSDID@32473.eventID", Value: "1011"},
					},
				}
			},
		},
		{
			name: "RFC 5424 example 4 (multiple SD elements, no MSG)",
			in:   `<165>1 2003-10-11T22:14:15.003Z mymachine.example.com evntslog - ID47 [exampleSDID@32473 iut="3" eventSource="Application" eventID="1011"][examplePriority@32473 class="high"]`,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Time: mustTime(t, "2003-10-11T22:14:15.003Z"), Facility: 20, Severity: 5,
					Hostname: "mymachine.example.com", AppName: "evntslog", MessageID: "ID47",
					Fields: []logentry.Field{
						{Key: "sd.exampleSDID@32473.iut", Value: "3"},
						{Key: "sd.exampleSDID@32473.eventSource", Value: "Application"},
						{Key: "sd.exampleSDID@32473.eventID", Value: "1011"},
						{Key: "sd.examplePriority@32473.class", Value: "high"},
					},
				}
			},
		},
		{
			name: "all NILVALUE",
			in:   `<13>1 - - - - - -`,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{Facility: 1, Severity: 5}
			},
		},
		{
			name: "util-linux logger --rfc5424 output",
			in:   `<13>1 2026-09-14T15:40:01.123456+00:00 host01 ubuntu - - [timeQuality tzKnown="1" isSynced="1" syncAccuracy="500000"] Test syslog message`,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Time: mustTime(t, "2026-09-14T15:40:01.123456Z"), Facility: 1, Severity: 5,
					Hostname: "host01", AppName: "ubuntu", Message: "Test syslog message",
					Fields: []logentry.Field{
						{Key: "sd.timeQuality.tzKnown", Value: "1"},
						{Key: "sd.timeQuality.isSynced", Value: "1"},
						{Key: "sd.timeQuality.syncAccuracy", Value: "500000"},
					},
				}
			},
		},
		{
			name: "escaped SD values",
			in:   `<14>1 2026-09-14T10:00:00Z h a - - [x@1 q="say \"hi\"" b="a\\b" c="x\]y" d="keep\n"] m`,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Time: mustTime(t, "2026-09-14T10:00:00Z"), Facility: 1, Severity: 6,
					Hostname: "h", AppName: "a", Message: "m",
					Fields: []logentry.Field{
						{Key: "sd.x@1.q", Value: `say "hi"`},
						{Key: "sd.x@1.b", Value: `a\b`},
						{Key: "sd.x@1.c", Value: `x]y`},
						{Key: "sd.x@1.d", Value: `keep\n`},
					},
				}
			},
		},
		{
			name: "repeated SD-PARAM becomes JSON array",
			in:   `<14>1 2026-09-14T10:00:00Z h a - - [tags@1 t="a" t="b" t="c\"d"] m`,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Time: mustTime(t, "2026-09-14T10:00:00Z"), Facility: 1, Severity: 6,
					Hostname: "h", AppName: "a", Message: "m",
					Fields: []logentry.Field{{Key: "sd.tags@1.t", Value: `["a","b","c\"d"]`}},
				}
			},
		},
		{
			name:  "short SD names with collision fallback",
			in:    `<14>1 2026-09-14T10:00:00Z h a - - [a@1 user="x" ip="1.2.3.4"][b@1 user="y"] m`,
			short: true,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Time: mustTime(t, "2026-09-14T10:00:00Z"), Facility: 1, Severity: 6,
					Hostname: "h", AppName: "a", Message: "m",
					Fields: []logentry.Field{
						{Key: "user", Value: "x"},
						{Key: "ip", Value: "1.2.3.4"},
						{Key: "sd.b@1.user", Value: "y"},
					},
				}
			},
		},
		{
			name:  "short SD names with repeated param",
			in:    `<14>1 2026-09-14T10:00:00Z h a - - [a@1 t="1" t="2"] m`,
			short: true,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Time: mustTime(t, "2026-09-14T10:00:00Z"), Facility: 1, Severity: 6,
					Hostname: "h", AppName: "a", Message: "m",
					Fields: []logentry.Field{{Key: "t", Value: `["1","2"]`}},
				}
			},
		},
		{
			name: "invalid timestamp is kept raw",
			in:   `<14>1 yesterday h a - - - m`,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Facility: 1, Severity: 6, TimestampRaw: "yesterday",
					Hostname: "h", AppName: "a", Message: "m",
				}
			},
		},
		{
			name: "lowercase t and z and nanoseconds accepted",
			in:   `<14>1 2026-09-14t10:00:00.123456789z h a - - - m`,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Time: mustTime(t, "2026-09-14T10:00:00.123456789Z"), Facility: 1, Severity: 6,
					Hostname: "h", AppName: "a", Message: "m",
				}
			},
		},
		{
			name: "missing STRUCTURED-DATA is tolerated",
			in:   `<14>1 2026-09-14T10:00:00Z h a p id hello world`,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Time: mustTime(t, "2026-09-14T10:00:00Z"), Facility: 1, Severity: 6,
					Hostname: "h", AppName: "a", ProcessID: "p", MessageID: "id", Message: "hello world",
				}
			},
		},
		{
			name: "malformed SD keeps remainder as message",
			in:   `<14>1 2026-09-14T10:00:00Z h a - - [x@1 k="unterminated] m`,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Time: mustTime(t, "2026-09-14T10:00:00Z"), Facility: 1, Severity: 6,
					Hostname: "h", AppName: "a",
					Message:    `[x@1 k="unterminated] m`,
					ParseError: "rfc5424: unterminated SD-PARAM value at offset 43",
				}
			},
		},
		{
			name: "truncated header",
			in:   `<14>1 2026-09-14T10:00:00Z host`,
			want: func(t *testing.T) logentry.Entry {
				return logentry.Entry{
					Time: mustTime(t, "2026-09-14T10:00:00Z"), Facility: 1, Severity: 6,
					ParseError: "rfc5424: header truncated at offset 27",
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parse(t, tt.in, tt.short)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			want := tt.want(t)
			want.SeveritySource = logentry.SeverityFromLog
			if diff := cmp.Diff(want, *got, entryCmp); diff != "" {
				t.Errorf("Parse() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	for _, in := range []string{
		"",
		"no pri at all",
		"<14>Sep 14 10:00:00 host bsd style",
		"<14>2 2026-09-14T10:00:00Z h a - - - version 2",
		"<999>1 - - - - - -",
		"<14>1",
	} {
		_, err := parse(t, in, false)
		if !errors.Is(err, parser.ErrNotThisFormat) {
			t.Errorf("Parse(%q) error = %v, want ErrNotThisFormat", in, err)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`<34>1 2003-10-11T22:14:15.003Z mymachine.example.com su - ID47 - 'su root' failed`,
		`<165>1 2003-10-11T22:14:15.003Z h evntslog - ID47 [exampleSDID@32473 iut="3" eventSource="App"][p@1 class="high"] msg`,
		`<14>1 - - - - - [a@1 b="c\"d" b="e"]`,
		`<14>1 2026-09-14T10:00:00Z h a - - [x`,
	} {
		f.Add(s, false)
	}
	f.Fuzz(func(t *testing.T, data string, short bool) {
		var e logentry.Entry
		e.Reset()
		err := New().Parse(&parser.Input{Data: data, SDShort: short}, &e)
		if err != nil {
			if !errors.Is(err, parser.ErrNotThisFormat) {
				t.Fatalf("unexpected error type: %v", err)
			}
			return
		}
		if !e.Severity.Valid() || !e.Facility.Valid() {
			t.Fatalf("invalid facility/severity %d/%d", e.Facility, e.Severity)
		}
		// Message must be a suffix of the input unless an SD escape rewrote it.
		if e.Message != "" && !strings.Contains(data, e.Message) {
			t.Fatalf("message %q not found in input", e.Message)
		}
		for _, fld := range e.Fields {
			if fld.Key == "" {
				t.Fatal("empty field key")
			}
			if utf8.ValidString(data) && !utf8.ValidString(fld.Value) {
				t.Fatalf("field value became invalid UTF-8: %q", fld.Value)
			}
		}
	})
}

func BenchmarkParse(b *testing.B) {
	in := &parser.Input{Data: `<165>1 2026-09-14T10:00:00.123456Z fw01.example.com vpnd 812 TUNNEL [meta@32473 vpn="HQ-VPN" iface="wan1" policy="1234"] VPN tunnel HQ-VPN disconnected: DPD timeout`}
	p := New()
	var e logentry.Entry
	b.ReportAllocs()
	for b.Loop() {
		e.Reset()
		if err := p.Parse(in, &e); err != nil {
			b.Fatal(err)
		}
	}
}
