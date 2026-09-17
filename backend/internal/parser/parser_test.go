package parser

import (
	"testing"

	"github.com/freezxp/syslogc/backend/internal/logentry"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		in   string
		want logentry.Format
	}{
		{"", logentry.FormatUnknown},
		{"<34>1 2003-10-11T22:14:15.003Z host su - ID47 - msg", logentry.FormatRFC5424},
		{"<34>1 - - - - - -", logentry.FormatRFC5424},
		{"<34>Oct 11 22:14:15 host su: msg", logentry.FormatRFC3164},
		{"<34>10 is not a version", logentry.FormatRFC3164},
		{"<34>1x", logentry.FormatRFC3164},
		{"<999>1 - - - - - -", logentry.FormatRFC3164},
		{`{"message":"hi"}`, logentry.FormatJSON},
		{`[{"message":"hi"}]`, logentry.FormatJSON},
		{"Oct 11 22:14:15 host su: no pri", logentry.FormatRFC3164},
		{"<", logentry.FormatRFC3164},
	}
	for _, tt := range tests {
		if got := Detect(tt.in); got != tt.want {
			t.Errorf("Detect(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParsePRI(t *testing.T) {
	tests := []struct {
		in      string
		wantPRI int
		wantN   int
		wantOK  bool
	}{
		{"<0>", 0, 3, true},
		{"<13>rest", 13, 4, true},
		{"<191>x", 191, 5, true},
		{"<192>x", 0, 0, false},
		{"<>x", 0, 0, false},
		{"<1234>x", 0, 0, false},
		{"<12", 0, 0, false},
		{"13>", 0, 0, false},
		{"<1a>", 0, 0, false},
	}
	for _, tt := range tests {
		pri, n, ok := ParsePRI(tt.in)
		if pri != tt.wantPRI || n != tt.wantN || ok != tt.wantOK {
			t.Errorf("ParsePRI(%q) = (%d, %d, %v), want (%d, %d, %v)", tt.in, pri, n, ok, tt.wantPRI, tt.wantN, tt.wantOK)
		}
	}
}

func FuzzDetect(f *testing.F) {
	f.Add("<34>1 - - - - - -")
	f.Add("<34>Oct 11 22:14:15 host su: msg")
	f.Fuzz(func(t *testing.T, s string) {
		_ = Detect(s)
		if pri, n, ok := ParsePRI(s); ok && (pri < 0 || pri > 191 || n < 3 || n > 5) {
			t.Fatalf("ParsePRI(%q) = %d, %d", s, pri, n)
		}
	})
}
