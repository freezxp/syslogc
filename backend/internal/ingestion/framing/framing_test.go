package framing

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

type frame struct {
	msg       string
	truncated bool
}

func decodeAll(t *testing.T, r io.Reader, mode Mode, max int) ([]frame, error) {
	t.Helper()
	d := NewDecoder(r, mode, max)
	var out []frame
	for i := 0; i < 10_000; i++ {
		msg, trunc, err := d.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		out = append(out, frame{string(msg), trunc})
	}
	t.Fatal("decoder did not terminate")
	return nil, nil
}

func TestDecoderAuto(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want []frame
	}{
		{
			name: "octet counting",
			in:   "11 <14>1 hello5 world",
			want: []frame{{"<14>1 hello", false}, {"world", false}},
		},
		{
			name: "LF framing (logger --tcp default)",
			in:   "<13>Sep 14 10:00:00 host a: one\n<13>Sep 14 10:00:00 host a: two\n",
			want: []frame{{"<13>Sep 14 10:00:00 host a: one", false}, {"<13>Sep 14 10:00:00 host a: two", false}},
		},
		{
			name: "CRLF and blank lines",
			in:   "first\r\n\r\n\nsecond\r\n",
			want: []frame{{"first", false}, {"second", false}},
		},
		{
			name: "mixed framing on one connection",
			in:   "5 abcde\n<14>line\n3 xyz",
			want: []frame{{"abcde", false}, {"<14>line", false}, {"xyz", false}},
		},
		{
			name: "digits not followed by space are text (Cisco sequence numbers)",
			in:   "000123: *Sep 14 msg\n123: seq msg\n42\n",
			want: []frame{{"000123: *Sep 14 msg", false}, {"123: seq msg", false}, {"42", false}},
		},
		{
			name: "final unterminated line",
			in:   "a\nlast without newline",
			want: []frame{{"a", false}, {"last without newline", false}},
		},
		{
			name: "oversize LF line is truncated and remainder skipped",
			in:   strings.Repeat("x", 100) + "\nnext\n",
			max:  10,
			want: []frame{{strings.Repeat("x", 10), true}, {"next", false}},
		},
		{
			name: "oversize LF line longer than read buffer",
			in:   strings.Repeat("y", 200_000) + "\nnext\n",
			max:  70_000,
			want: []frame{{strings.Repeat("y", 70_000), true}, {"next", false}},
		},
		{
			name: "oversize octet frame is truncated and remainder discarded",
			in:   "20 " + strings.Repeat("z", 20) + "4 next",
			max:  8,
			want: []frame{{strings.Repeat("z", 8), true}, {"next", false}},
		},
		{
			name: "long line within limit spans buffer refills",
			in:   strings.Repeat("q", 150_000) + "\n",
			max:  200_000,
			want: []frame{{strings.Repeat("q", 150_000), false}},
		},
		{
			name: "stream ends inside length prefix",
			in:   "a\n12",
			want: []frame{{"a", false}, {"12", false}},
		},
		{
			name: "truncated octet frame at EOF is not emitted",
			in:   "a\n50 short",
			want: []frame{{"a", false}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			max := tt.max
			if max == 0 {
				max = 64 << 10
			}
			for _, reader := range []struct {
				name string
				r    io.Reader
			}{
				{"full", strings.NewReader(tt.in)},
				{"one-byte", iotest.OneByteReader(strings.NewReader(tt.in))},
				{"half", iotest.HalfReader(strings.NewReader(tt.in))},
			} {
				got, err := decodeAll(t, reader.r, Auto, max)
				if err != nil {
					t.Fatalf("%s reader: %v", reader.name, err)
				}
				if len(got) != len(tt.want) {
					t.Fatalf("%s reader: got %d frames %q, want %d", reader.name, len(got), summarize(got), len(tt.want))
				}
				for i := range got {
					if got[i] != tt.want[i] {
						t.Errorf("%s reader: frame %d = %q (trunc %v), want %q (trunc %v)", reader.name, i,
							short(got[i].msg), got[i].truncated, short(tt.want[i].msg), tt.want[i].truncated)
					}
				}
			}
		})
	}
}

func TestDecoderExplicitModes(t *testing.T) {
	got, err := decodeAll(t, strings.NewReader("one\x00two\x00"), NUL, 1024)
	if err != nil || len(got) != 2 || got[0].msg != "one" || got[1].msg != "two" {
		t.Errorf("NUL: %v %v", got, err)
	}
	got, err = decodeAll(t, strings.NewReader("12 not-a-frame\n"), LF, 1024)
	if err != nil || len(got) != 1 || got[0].msg != "12 not-a-frame" {
		t.Errorf("LF with leading digits: %v %v", got, err)
	}
	_, err = decodeAll(t, strings.NewReader("<14>no length\n"), OctetCounting, 1024)
	if !errors.Is(err, ErrInvalidFrame) {
		t.Errorf("strict octet counting accepted text: %v", err)
	}
}

func FuzzDecoder(f *testing.F) {
	f.Add("11 <14>1 hello5 world", 16)
	f.Add("line\r\n\n12: x\n3 abc", 4)
	f.Fuzz(func(t *testing.T, in string, max int) {
		if max < 1 || max > 1<<16 {
			return
		}
		d := NewDecoder(iotest.HalfReader(strings.NewReader(in)), Auto, max)
		total := 0
		for i := 0; i <= len(in)+1; i++ {
			msg, _, err := d.Next()
			if err != nil {
				return
			}
			if len(msg) > max {
				t.Fatalf("message of %d bytes exceeds max %d", len(msg), max)
			}
			total += len(msg)
			if total > len(in) {
				t.Fatalf("decoded more bytes (%d) than input (%d)", total, len(in))
			}
		}
		t.Fatal("decoder did not reach EOF")
	})
}

func summarize(fs []frame) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = short(f.msg)
	}
	return out
}

func short(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
