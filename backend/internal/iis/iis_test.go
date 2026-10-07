package iis

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

func TestClassOfReadsTheCodeIISActuallyWrote(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   StatusClass
		ok     bool
	}{
		{"100", ClassInfo, true},
		{"200", ClassSuccess, true},
		{"204", ClassSuccess, true},
		{"301", ClassRedirect, true},
		{"304", ClassRedirect, true},
		{"404", ClassClientError, true},
		{"401", ClassClientError, true},
		{"499", ClassClientError, true},
		{"500", ClassServerError, true},
		{"503", ClassServerError, true},
		{"599", ClassServerError, true},
		// IIS writes a bare dash where it had nothing to write, and a
		// truncated line can end anywhere. None of these is a success, and
		// none of them is rounded into the nearest class.
		{"", "", false},
		{"-", "", false},
		{"  ", "", false},
		{"20", "", false},
		{"2000", "", false},
		{"2x4", "", false},
		{"600", "", false},
		{"099", "", false},
		{"0", "", false},
		// Not trimmed, because the filter that counts these cannot trim
		// either; see the comment on ClassOf.
		{" 404 ", "", false},
		// A leading zero is still the number the store will compare, so it
		// is named as the number it is.
		{"0404", ClassClientError, true},
	} {
		got, ok := ClassOf(tc.status)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ClassOf(%q) = %q, %v; want %q, %v", tc.status, got, ok, tc.want, tc.ok)
		}
	}
}

// TestClassFilterSelectsExactlyWhatClassOfNames is the one that matters.
//
// The class a request is reported under comes from ClassOf, and the class a
// request is counted under comes from classFilter, and they are different
// pieces of code. If they disagree, every number on the page is wrong by a
// margin nobody can see. So this walks every value either of them could meet
// and insists they agree.
func TestClassFilterSelectsExactlyWhatClassOfNames(t *testing.T) {
	var statuses []string
	for n := -1; n <= 1000; n++ {
		statuses = append(statuses, strconv.Itoa(n))
	}
	statuses = append(statuses, "", "-", " ", "404 ", "abc", "4o4", "0404", "+404")

	for _, class := range []StatusClass{ClassInfo, ClassSuccess, ClassRedirect, ClassClientError, ClassServerError} {
		expr := classFilter(class)
		if expr == nil {
			t.Fatalf("no filter for class %s", class)
		}
		for _, s := range statuses {
			named, _ := ClassOf(s)
			selected := matches(expr, map[string]string{FieldStatus: s})
			if selected != (named == class) {
				t.Errorf("status %q: filter for %s selects=%v but ClassOf names %q",
					s, class, selected, named)
			}
		}
	}
}

func TestClassFiltersNeverCompileToAPatternMatch(t *testing.T) {
	// A prefix or a substring match on iis.status compiles to a regular
	// expression, and a regular expression over every request in the window
	// is the thing that must never reach this store. A range filter is the
	// same question the store can answer from the values it holds.
	for _, class := range []StatusClass{ClassInfo, ClassSuccess, ClassRedirect, ClassClientError, ClassServerError} {
		walk(classFilter(class), func(e *filter.Expr) {
			switch e.Op {
			case filter.Contains, filter.StartsWith, filter.Regex, filter.Text:
				t.Errorf("the filter for %s uses %s, which no index can answer", class, e.Op)
			}
		})
	}
	walk(slowFilter(1000), func(e *filter.Expr) {
		switch e.Op {
		case filter.Contains, filter.StartsWith, filter.Regex, filter.Text:
			t.Errorf("the slow-request filter uses %s, which no index can answer", e.Op)
		}
	})
}

func TestStatusAndSubStatusAreSaidInWords(t *testing.T) {
	for status, want := range map[string]string{
		"404": "not found",
		"503": "service unavailable",
		"500": "the application failed",
		// 401 means the request was not authenticated. The standard's own
		// word for it is "unauthorized", which has sent people looking at
		// file permissions for twenty years.
		"401": "not authenticated",
	} {
		if got := StatusText(status); got != want {
			t.Errorf("StatusText(%s) = %q, want %q", status, got, want)
		}
	}
	// An unfamiliar code is placed by its class rather than left bare.
	if got := StatusText("418"); got != "Client errors" {
		t.Errorf("StatusText(418) = %q, want the class", got)
	}
	if got := StatusText("-"); got != "" {
		t.Errorf("StatusText(-) = %q, want nothing", got)
	}

	// The 401 sub-status is the difference between a wrong password and a
	// permission on a folder, which is the whole question being asked.
	for sub, want := range map[string]string{
		"1": "the credentials were wrong",
		"3": "the account has no permission on the file or folder",
	} {
		if got := AuthSubStatus(sub); got != want {
			t.Errorf("AuthSubStatus(%s) = %q, want %q", sub, got, want)
		}
	}
	if got := AuthSubStatus("9"); got != "sub-status 9" {
		t.Errorf("unknown sub-status = %q", got)
	}
	if got := AuthSubStatus("-"); got != "" {
		t.Errorf("absent sub-status = %q", got)
	}
}

// requestQuerier answers CategoryCounts and Count from a small table of
// pretend requests by evaluating each category's filter itself, so a test can
// prove an overview number counts the requests it claims to.
type requestQuerier struct {
	requests []map[string]string
}

func (q *requestQuerier) CategoryCounts(_ context.Context, c storage.CategoryQuery) ([]storage.CategoryRow, error) {
	out := make([]storage.CategoryRow, 0, len(c.Categories))
	for _, cat := range c.Categories {
		var n int
		for _, r := range q.requests {
			if matches(cat.Filter, r) {
				n++
			}
		}
		out = append(out, storage.CategoryRow{Category: cat.Name, Messages: float64(n)})
	}
	return out, nil
}

func (q *requestQuerier) Count(_ context.Context, c storage.CountQuery) (int64, error) {
	if c.DistinctField != "" {
		seen := map[string]bool{}
		for _, r := range q.requests {
			if v := r[c.DistinctField]; v != "" && matches(c.Filter, r) {
				seen[v] = true
			}
		}
		return int64(len(seen)), nil
	}
	var n int64
	for _, r := range q.requests {
		if matches(c.Filter, r) {
			n++
		}
	}
	return n, nil
}

func (q *requestQuerier) Aggregate(context.Context, storage.AggregateQuery) ([]storage.AggRow, error) {
	return nil, nil
}

func (q *requestQuerier) Search(context.Context, storage.SearchQuery) (storage.Rows, error) {
	return nil, nil
}

func TestOverviewCountsWhatEachNumberSaysItCounts(t *testing.T) {
	// Twelve requests, two of which have a status no class can own. The class
	// counts must come to ten, not twelve: the difference is left visible on
	// purpose, because a line whose status could not be read is not a success
	// and pretending otherwise is how a page comes to claim perfect
	// availability during an outage.
	req := func(status, taken, client string) map[string]string {
		return map[string]string{FieldStatus: status, FieldTimeTaken: taken, FieldClientIP: client}
	}
	q := &requestQuerier{requests: []map[string]string{
		req("200", "12", "10.0.0.1"),
		req("200", "4000", "10.0.0.1"),
		req("204", "3", "10.0.0.2"),
		req("301", "7", "10.0.0.2"),
		req("404", "9", "10.0.0.3"),
		req("401", "2", "10.0.0.3"),
		req("401", "1500", "10.0.0.4"),
		req("500", "30", "10.0.0.4"),
		req("503", "60000", "10.0.0.4"),
		req("100", "1", "10.0.0.5"),
		req("-", "-", "10.0.0.6"),
		req("", "", "10.0.0.6"),
	}}
	sel := storage.Selection{Range: storage.TimeRange{
		Start: time.Unix(0, 0).UTC(), End: time.Unix(3600, 0).UTC()}}

	out, err := New(q).Overview(context.Background(), sel, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		got, want int64
		because   string
	}{
		// Eleven, not twelve: the last line has no status field at all, so
		// it is not an IIS request and the total must not claim it is. The
		// one with a "-" is a request whose status could not be read, and is
		// counted.
		{"requests", out.Requests, 11, "every IIS line, readable status or not"},
		{"informational", out.Informational, 1, ""},
		{"succeeded", out.Succeeded, 3, ""},
		{"redirected", out.Redirected, 1, ""},
		// Both 401s are client errors as well as authentication failures.
		{"client errors", out.ClientErrors, 3, ""},
		{"server errors", out.ServerErrors, 2, ""},
		{"auth failures", out.AuthFailures, 2, "the 401s, counted again on their own"},
		// 4000 ms, 1500 ms and 60000 ms. The 60 ms is not slow and the "-"
		// is not a number, so neither is counted.
		{"slow requests", out.SlowRequests, 3, "at or above 1000 ms"},
		{"clients", out.Clients, 6, ""},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d %s", tc.name, tc.got, tc.want, tc.because)
		}
	}
	if out.SlowThresholdMillis != 1000 {
		t.Errorf("slow threshold = %d; the count means nothing without it", out.SlowThresholdMillis)
	}
	classes := out.Informational + out.Succeeded + out.Redirected + out.ClientErrors + out.ServerErrors
	if classes != 10 {
		t.Errorf("the classes come to %d, want 10", classes)
	}
	if classes >= out.Requests {
		t.Errorf("the classes come to %d of %d requests; the two unreadable "+
			"statuses should be missing from them", classes, out.Requests)
	}
}

// walk calls fn for every node of an expression.
func walk(e *filter.Expr, fn func(*filter.Expr)) {
	if e == nil {
		return
	}
	fn(e)
	for _, a := range e.Args {
		walk(a, fn)
	}
	walk(e.Arg, fn)
}

// matches evaluates an expression against one pretend record, the way the
// store would.
//
// Range comparisons match only values that are numbers, which is what
// VictoriaLogs does: its range filter reads the stored value as a number and
// a value that is not one matches nothing. That is exactly the behaviour the
// class filters depend on to keep "-" out of the 2xx column, so a test that
// assumed otherwise would prove nothing.
func matches(e *filter.Expr, row map[string]string) bool {
	if e == nil {
		return true
	}
	switch e.Op {
	case filter.And:
		for _, a := range e.Args {
			if !matches(a, row) {
				return false
			}
		}
		return true
	case filter.Or:
		for _, a := range e.Args {
			if matches(a, row) {
				return true
			}
		}
		return false
	case filter.Not:
		return !matches(e.Arg, row)
	case filter.Eq:
		return row[e.Field] == e.Value
	case filter.Exists:
		// A field exists when it has a value, which "-" is: a request whose
		// status could not be read is still a request.
		return row[e.Field] != ""
	case filter.Gte, filter.Gt, filter.Lte, filter.Lt:
		got, err := strconv.ParseFloat(row[e.Field], 64)
		if err != nil {
			return false
		}
		want, err := strconv.ParseFloat(e.Value, 64)
		if err != nil {
			return false
		}
		switch e.Op {
		case filter.Gte:
			return got >= want
		case filter.Gt:
			return got > want
		case filter.Lte:
			return got <= want
		default:
			return got < want
		}
	}
	panic("the test evaluator does not know op " + string(e.Op))
}
