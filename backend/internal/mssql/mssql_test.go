package mssql

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

func TestEventIdsAreTheOnesSQLServerUses(t *testing.T) {
	// Pinned because they are the contract with SQL Server, and a typo here
	// would produce a page that is simply always empty.
	for name, got := range map[string]string{
		"login failed":       EventLoginFailed,
		"deadlock":           EventDeadlock,
		"os i/o error":       EventIOError,
		"page damaged":       EventIOLogicalError,
		"read retried":       EventIORetrySucceeded,
		"transaction log":    EventLogFull,
		"out of memory":      EventOutOfMemory,
		"non-yielding":       EventSchedulerNonYielding,
		"schedulers stalled": EventSchedulerStuck,
		"backup done":        EventBackupDone,
	} {
		want := map[string]string{
			"login failed": "18456", "deadlock": "1205", "os i/o error": "823",
			"page damaged": "824", "read retried": "825", "transaction log": "9002",
			"out of memory": "701", "non-yielding": "17883", "schedulers stalled": "17884",
			"backup done": "18264",
		}[name]
		if got != want {
			t.Errorf("%s event = %s, want %s", name, got, want)
		}
	}
}

func TestEventsAreClassifiedByWhatToDoAboutThem(t *testing.T) {
	for _, tc := range []struct {
		id      string
		kind    Kind
		failure bool
		why     string
	}{
		{EventLoginFailed, KindLoginFailure, true, "somebody could not sign in"},
		{EventSSPIFailed, KindLoginFailure, true, "a Windows-authentication sign-in that failed before it had an account"},
		{EventLoginSucceededTrusted, KindLoginSuccess, false, "a sign-in that worked"},
		{EventLoginSucceededSQL, KindLoginSuccess, false, "a sign-in that worked"},
		{EventDeadlock, KindDeadlock, true, "a transaction was rolled back"},
		{EventIOError, KindCorruption, true, "the disk refused the request"},
		{EventIOLogicalError, KindCorruption, true, "the page read back damaged"},
		// The one that catches people out: 825 is a retry that succeeded.
		// Nothing was lost and no query failed, so calling it a failure would
		// have somebody restoring a database that is perfectly intact.
		{EventIORetrySucceeded, KindCorruption, false, "the read succeeded on retry"},
		{EventLogFull, KindResource, true, "writes have stopped"},
		{EventOutOfMemory, KindResource, true, "a query was refused"},
		{EventSchedulerNonYielding, KindScheduler, true, "a worker stopped yielding"},
		{EventSchedulerStuck, KindScheduler, true, "queries are not being picked up"},
		{EventBackupDone, KindBackup, false, "a backup finished, which is good news"},
		{EventLogBackupDone, KindBackup, false, "a log backup finished"},
	} {
		e, ok := Lookup(tc.id)
		if !ok {
			t.Errorf("event %s is not in the table", tc.id)
			continue
		}
		if e.Kind != tc.kind {
			t.Errorf("event %s kind = %s, want %s (%s)", tc.id, e.Kind, tc.kind, tc.why)
		}
		if e.Failure != tc.failure {
			t.Errorf("event %s failure = %v, want %v (%s)", tc.id, e.Failure, tc.failure, tc.why)
		}
		if Classify(tc.id) != tc.kind {
			t.Errorf("Classify(%s) = %s, want %s", tc.id, Classify(tc.id), tc.kind)
		}
	}
}

func TestEveryEventHasWordsToShowBesideIt(t *testing.T) {
	// The ids are searched for and the descriptions are shown, and both come
	// from one table, so an id that is counted always has something to say.
	for id, e := range events {
		if e.What == "" {
			t.Errorf("event %s is read but has no description", id)
		}
		if e.ID != id {
			t.Errorf("event %s is filed under id %q", e.ID, id)
		}
		if e.Kind == KindUnknown {
			t.Errorf("event %s is in the table but classified as unknown", id)
		}
	}
	// An unfamiliar number is still shown rather than hidden.
	if got := Describe("12345"); got != "event 12345" {
		t.Errorf("unknown event = %q", got)
	}
	if got := Describe(""); got != "" {
		t.Errorf("empty event = %q", got)
	}
	if got := Classify("12345"); got != KindUnknown {
		t.Errorf("unknown event kind = %s", got)
	}
}

func TestSevereEventsLeaveOutTheRetryThatSucceeded(t *testing.T) {
	severe := SevereEvents()
	if slices.Contains(severe, EventIORetrySucceeded) {
		t.Error("825 is among the severe errors; it means a read succeeded on retry, " +
			"so it would fill the page with red nobody can act on")
	}
	// The ones it must contain, because they are the reason the list exists.
	for _, id := range []string{EventIOError, EventIOLogicalError, EventLogFull,
		EventOutOfMemory, EventSchedulerNonYielding, EventSchedulerStuck} {
		if !slices.Contains(severe, id) {
			t.Errorf("event %s is not among the severe errors", id)
		}
	}
	// Backups and successful sign-ins are not problems, so they are not in
	// the set the problem lists are counted over.
	problems := ProblemEvents()
	for _, id := range []string{EventBackupDone, EventLogBackupDone,
		EventLoginSucceededTrusted, EventLoginSucceededSQL, EventIORetrySucceeded} {
		if slices.Contains(problems, id) {
			t.Errorf("event %s is counted as a problem; %s", id, Describe(id))
		}
	}
	if !slices.Contains(problems, EventDeadlock) {
		t.Error("deadlocks are not counted as problems")
	}
}

// countingQuerier answers Count from a small table of pretend events, so a
// test can prove that a number counts what its name says and nothing else.
// Everything else returns nothing: these tests are about which events each
// number is asked for, not about the store.
type countingQuerier struct {
	byEvent map[string]int64
}

func (q *countingQuerier) Count(_ context.Context, c storage.CountQuery) (int64, error) {
	if c.DistinctField != "" {
		return 0, nil
	}
	var total int64
	for _, id := range eventIDs(c.Filter) {
		total += q.byEvent[id]
	}
	return total, nil
}

func (q *countingQuerier) Aggregate(context.Context, storage.AggregateQuery) ([]storage.AggRow, error) {
	return nil, nil
}

func (q *countingQuerier) CategoryCounts(context.Context, storage.CategoryQuery) ([]storage.CategoryRow, error) {
	return nil, nil
}

func (q *countingQuerier) Search(context.Context, storage.SearchQuery) (storage.Rows, error) {
	return nil, nil
}

// eventIDs collects the event ids a filter selects.
func eventIDs(e *filter.Expr) []string {
	if e == nil {
		return nil
	}
	switch e.Op {
	case filter.And, filter.Or:
		var out []string
		for _, a := range e.Args {
			out = append(out, eventIDs(a)...)
		}
		return out
	case filter.Eq:
		if e.Field == FieldEventID {
			return []string{e.Value}
		}
	case filter.In:
		if e.Field == FieldEventID {
			return e.Values
		}
	}
	return nil
}

func TestOverviewCountsWhatEachNumberSaysItCounts(t *testing.T) {
	// 500 retries that succeeded and one page that did not. "Severe errors"
	// must say one, not 501: the whole point of separating them is that the
	// first number is a reason to replace a disk at the weekend and the
	// second is a reason to restore a database tonight.
	q := &countingQuerier{byEvent: map[string]int64{
		EventIORetrySucceeded: 500,
		EventIOLogicalError:   1,
		EventLoginFailed:      1000,
		EventDeadlock:         7,
		EventBackupDone:       2,
		EventLogBackupDone:    48,
		EventSSPIFailed:       3,
	}}
	sel := storage.Selection{Range: storage.TimeRange{
		Start: time.Unix(0, 0).UTC(), End: time.Unix(3600, 0).UTC()}}

	out, err := New(q).Overview(context.Background(), sel)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		got  int64
		want int64
	}{
		{"severe errors", out.SevereErrors, 1},
		{"read retries", out.ReadRetries, 500},
		// 18456 only. 17806 is a failed sign-in too, but no account is read
		// out of it, so counting it here would make this number disagree
		// with the accounts listed beside it.
		{"sign-in failures", out.SignInFailures, 1000},
		{"deadlocks", out.Deadlocks, 7},
		// Database backups. The 48 log backups are a different event and are
		// deliberately not added in.
		{"backups", out.Backups, 2},
		{"sign-ins", out.SignIns, 0},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}
