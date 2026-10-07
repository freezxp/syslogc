// Package mssql answers the questions people keep SQL Server's log for: who
// could not sign in and from where, what deadlocked, whether the backups ran,
// and which errors mean the database itself is in trouble.
//
// Everything here is computed by aggregating in the log store rather than
// reading rows: an instance under a brute-force attempt writes tens of
// thousands of failed sign-ins an hour, and counting those in the application
// is not an option.
//
// SQL Server says what happened through an error number, which arrives as the
// Windows event id. The number is the stable part — the sentence beside it is
// written in the server's own language and its details differ between builds
// — so the number is what this reads.
package mssql

import (
	"context"
	"sort"
	"time"

	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

// The SQL Server error numbers these analyses are built from.
const (
	// EventLoginFailed is the one people open this page for.
	EventLoginFailed = "18456" // login failed for an account
	// EventLoginSucceededTrusted and EventLoginSucceededSQL are both
	// successes. SQL Server records them only when login auditing is set to
	// both failures and successes, which is not the default on many builds.
	//
	// One of the pair is the Windows-authenticated ("trusted") connection
	// and the other the SQL-authenticated one, but which number carries
	// which is not consistent across builds, so they are counted together
	// and labelled the same rather than split on a guess. Anybody who needs
	// the distinction has the message text.
	EventLoginSucceededTrusted = "18453"
	EventLoginSucceededSQL     = "18454"
	// EventSSPIFailed is a Windows-authentication handshake that failed
	// before SQL Server had an account to name.
	EventSSPIFailed = "17806"

	// EventDeadlock is the deadlock victim notice.
	EventDeadlock = "1205"

	// EventIOError is an I/O the operating system refused: the disk, the
	// path or the controller, not the data.
	EventIOError = "823"
	// EventIOLogicalError is an I/O that completed and returned something
	// that cannot be a valid page — a torn page or a failed checksum. This
	// one means the data on disk is damaged.
	EventIOLogicalError = "824"
	// EventIORetrySucceeded is a read that failed and then succeeded on a
	// retry. Nothing has been lost and no query failed, so it is a warning
	// and not an error — but it is the warning that precedes 823 and 824,
	// which is why it is read at all.
	EventIORetrySucceeded = "825"

	// EventLogFull is a transaction log that has no room left. Writes to the
	// database stop until it has room again.
	EventLogFull = "9002"
	// EventOutOfMemory is a query refused for want of memory.
	EventOutOfMemory = "701"

	// EventSchedulerNonYielding and EventSchedulerStuck are the scheduler
	// stalls. The exact wording of the 1788x family differs between builds,
	// so they are described in general terms rather than quoted.
	EventSchedulerNonYielding = "17883"
	EventSchedulerStuck       = "17884"

	// EventBackupDone is a database backup that finished. EventLogBackupDone
	// is a transaction-log backup, which is a different thing people
	// routinely mistake for it.
	EventBackupDone    = "18264"
	EventLogBackupDone = "18265"
)

// Fields the template produces, named once so a rename is one edit.
//
// FieldLoginUser and FieldClientIP are read out of the message text by the
// part's extract rules, so they exist only on events whose message carries
// them: a failed sign-in has both, a deadlock has neither. Treat them as
// optional everywhere.
const (
	FieldEventID   = "mssql.event_id"
	FieldProvider  = "mssql.provider"
	FieldSeverity  = "mssql.severity"
	FieldLoginUser = "mssql.login_user"
	FieldClientIP  = "mssql.client_ip"
	FieldHost      = "mssql.host"
	FieldMessage   = "mssql.message"
)

// Kind groups an event by what somebody reading it would do about it.
type Kind string

const (
	KindLoginFailure Kind = "login_failure"
	KindLoginSuccess Kind = "login_success"
	KindDeadlock     Kind = "deadlock"
	// KindCorruption covers the I/O and page-consistency events: the ones
	// that say the data or the disk underneath it is not well.
	KindCorruption Kind = "corruption"
	// KindResource covers running out of something: log space, memory.
	KindResource Kind = "resource"
	// KindScheduler covers a server that has stopped doing work without
	// having failed at anything in particular.
	KindScheduler Kind = "scheduler"
	KindBackup    Kind = "backup"
	KindUnknown   Kind = "unknown"
)

// Event describes one error number: what it is, and whether it records
// something that failed or something that was merely noticed.
type Event struct {
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	// What describes the event in the words somebody reviewing it would use.
	What string `json:"what"`
	// Failure says whether anything actually went wrong. It is false for the
	// backups, for successful sign-ins, and for 825 — a read that succeeded
	// on retry, which is a warning about the disk and not a failed query.
	Failure bool `json:"failure"`
}

var events = map[string]Event{
	EventLoginFailed:           {EventLoginFailed, KindLoginFailure, "sign-in failed", true},
	EventSSPIFailed:            {EventSSPIFailed, KindLoginFailure, "Windows authentication handshake failed", true},
	EventLoginSucceededTrusted: {EventLoginSucceededTrusted, KindLoginSuccess, "sign-in succeeded", false},
	EventLoginSucceededSQL:     {EventLoginSucceededSQL, KindLoginSuccess, "sign-in succeeded", false},
	EventDeadlock:              {EventDeadlock, KindDeadlock, "a transaction was chosen as a deadlock victim", true},
	EventIOError:               {EventIOError, KindCorruption, "the operating system refused an I/O request", true},
	EventIOLogicalError:        {EventIOLogicalError, KindCorruption, "a page read back damaged", true},
	EventIORetrySucceeded:      {EventIORetrySucceeded, KindCorruption, "a read succeeded only after retrying", false},
	EventLogFull:               {EventLogFull, KindResource, "the transaction log is full", true},
	EventOutOfMemory:           {EventOutOfMemory, KindResource, "not enough memory to run a query", true},
	EventSchedulerNonYielding:  {EventSchedulerNonYielding, KindScheduler, "a worker stopped yielding its scheduler", true},
	EventSchedulerStuck:        {EventSchedulerStuck, KindScheduler, "new queries were not picked up by any worker", true},
	EventBackupDone:            {EventBackupDone, KindBackup, "database backed up", false},
	EventLogBackupDone:         {EventLogBackupDone, KindBackup, "transaction log backed up", false},
}

// Lookup returns what is known about an error number.
func Lookup(id string) (Event, bool) {
	e, ok := events[id]
	return e, ok
}

// Describe explains an error number in words, or names the number when it is
// not one this package knows — an unfamiliar event is still worth showing.
func Describe(id string) string {
	if e, ok := events[id]; ok {
		return e.What
	}
	if id == "" {
		return ""
	}
	return "event " + id
}

// Classify says what kind of event an error number is.
func Classify(id string) Kind {
	if e, ok := events[id]; ok {
		return e.Kind
	}
	return KindUnknown
}

// SevereEvents are the errors worth being woken up for: the I/O and
// consistency errors, the log filling, memory running out and the scheduler
// stalling.
//
// 825 is deliberately not among them. It means a read failed and then
// succeeded, so nothing was lost; counting it as a severe error would turn a
// disk that is beginning to fail into a page full of red that nobody can act
// on. It is counted on its own, as a warning, by Overview.
func SevereEvents() []string {
	return []string{
		EventIOError, EventIOLogicalError,
		EventLogFull, EventOutOfMemory,
		EventSchedulerNonYielding, EventSchedulerStuck,
	}
}

// ProblemEvents are every event this package treats as something having gone
// wrong, which is what the recent-errors list and the per-instance and
// per-host breakdowns are counted over.
func ProblemEvents() []string {
	out := make([]string, 0, len(events))
	for id, e := range events {
		if e.Failure {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Querier is the part of the log store these analyses need.
type Querier interface {
	Aggregate(ctx context.Context, q storage.AggregateQuery) ([]storage.AggRow, error)
	CategoryCounts(ctx context.Context, q storage.CategoryQuery) ([]storage.CategoryRow, error)
	Count(ctx context.Context, q storage.CountQuery) (int64, error)
	Search(ctx context.Context, q storage.SearchQuery) (storage.Rows, error)
}

// eventFilter matches one or more error numbers.
func eventFilter(ids ...string) *filter.Expr {
	if len(ids) == 1 {
		return &filter.Expr{Op: filter.Eq, Field: FieldEventID, Value: ids[0]}
	}
	return &filter.Expr{Op: filter.In, Field: FieldEventID, Values: ids}
}

// withEvents narrows a selection to the given events, keeping whatever the
// caller already asked for.
func withEvents(sel storage.Selection, ids ...string) storage.Selection {
	out := sel
	if len(ids) == 0 {
		return out
	}
	ev := eventFilter(ids...)
	if sel.Filter == nil {
		out.Filter = ev
		return out
	}
	out.Filter = &filter.Expr{Op: filter.And, Args: []*filter.Expr{sel.Filter, ev}}
	return out
}

// Overview is the state of the instances in the terms somebody asks about
// them.
//
// Every distinct count here is counted over the whole window it is reported
// for, because distinct counts do not add up: the accounts that failed in an
// hour are not the sum of the accounts that failed in each of its minutes.
type Overview struct {
	// SignInFailures counts event 18456 over the window.
	SignInFailures int64 `json:"sign_in_failures"`
	// SignIns counts successful sign-ins. It is zero on an instance whose
	// login auditing was left at failures only, which is the default on many
	// builds — zero here means "not recorded", not "nobody signed in".
	SignIns int64 `json:"sign_ins"`
	// FailedAccounts is how many distinct accounts a failed sign-in was for,
	// and FailureSources how many distinct addresses they came from. One
	// account from one address is somebody with an old password; many
	// accounts from one address is somebody guessing.
	FailedAccounts int64 `json:"failed_accounts"`
	FailureSources int64 `json:"failure_sources"`
	// Deadlocks counts event 1205. See Activity for why a zero here does not
	// prove there were none.
	Deadlocks int64 `json:"deadlocks"`
	// SevereErrors counts the errors in SevereEvents.
	SevereErrors int64 `json:"severe_errors"`
	// ReadRetries counts event 825 — reads that succeeded only after
	// failing. Nothing was lost; the disk is worth looking at.
	ReadRetries int64 `json:"read_retries"`
	// Backups counts database backups that finished (18264). Log backups are
	// a different event and are not counted here.
	Backups int64 `json:"backups"`
	// Instances and Hosts are how many distinct instances and servers sent
	// anything at all over the window.
	Instances int64 `json:"instances"`
	Hosts     int64 `json:"hosts"`
}

// Service answers the SQL Server questions.
type Service struct {
	q Querier
}

// New returns a service reading from q.
func New(q Querier) *Service { return &Service{q: q} }

// Overview counts the headline numbers over a window.
func (s *Service) Overview(ctx context.Context, sel storage.Selection) (Overview, error) {
	var out Overview
	counts := []struct {
		into   *int64
		events []string
	}{
		{&out.SignInFailures, []string{EventLoginFailed}},
		{&out.SignIns, []string{EventLoginSucceededTrusted, EventLoginSucceededSQL}},
		{&out.Deadlocks, []string{EventDeadlock}},
		{&out.SevereErrors, SevereEvents()},
		{&out.ReadRetries, []string{EventIORetrySucceeded}},
		{&out.Backups, []string{EventBackupDone}},
	}
	for _, c := range counts {
		n, err := s.q.Count(ctx, storage.CountQuery{Selection: withEvents(sel, c.events...)})
		if err != nil {
			return out, err
		}
		*c.into = n
	}
	distinct := []struct {
		into   *int64
		field  string
		events []string
	}{
		{&out.FailedAccounts, FieldLoginUser, []string{EventLoginFailed}},
		{&out.FailureSources, FieldClientIP, []string{EventLoginFailed}},
		{&out.Instances, FieldProvider, nil},
		{&out.Hosts, FieldHost, nil},
	}
	for _, d := range distinct {
		n, err := s.q.Count(ctx, storage.CountQuery{
			Selection: withEvents(sel, d.events...), DistinctField: d.field})
		if err != nil {
			return out, err
		}
		*d.into = n
	}
	return out, nil
}

// Count is one value and how often it occurred.
type Count struct {
	Value string `json:"value"`
	// Label is Value said in words, where there is a better way to say it.
	Label string `json:"label,omitempty"`
	Count int64  `json:"count"`
}

// TopBy counts the commonest values of a field among the given events. No
// events means every event in the selection.
func (s *Service) TopBy(ctx context.Context, sel storage.Selection, field string, limit int, events ...string) ([]Count, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := s.q.Aggregate(ctx, storage.AggregateQuery{
		Selection: withEvents(sel, events...), GroupBy: field, Metric: storage.MetricCount, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Count, 0, len(rows))
	for _, r := range rows {
		// "-" is what a sender writes where it had nothing to write, and it
		// is not a value anybody wants ranked.
		if r.Group == "" || r.Group == "-" {
			continue
		}
		c := Count{Value: r.Group, Count: int64(r.Value)}
		if field == FieldEventID {
			c.Label = Describe(r.Group)
		}
		out = append(out, c)
	}
	return out, nil
}

// Series is activity over time, one line per kind of event.
type Series struct {
	Step        time.Duration `json:"-"`
	StepSeconds int           `json:"step_seconds"`
	Timestamps  []time.Time   `json:"timestamps"`
	Lines       []Line        `json:"lines"`
}

// Line is one counted thing over time.
type Line struct {
	Name   string  `json:"name"`
	Label  string  `json:"label"`
	Points []int64 `json:"points"`
	Total  int64   `json:"total"`
}

// Activity counts failed sign-ins, deadlocks, severe errors and backups over
// time, in one pass.
//
// They are counted together because they are read together: failed sign-ins
// matter differently beside a log that filled up, and a backup finishing is
// the context for everything that stopped happening while it ran.
//
// A zero deadlock line is not proof that nothing deadlocked. SQL Server
// reports 1205 to the client that lost; it reaches the Windows Application
// channel only where the message is marked as logged or the deadlock trace
// flags (1204, 1222) are on. The same caution applies to anything else below
// severity 19, which SQL Server does not log unconditionally.
func (s *Service) Activity(ctx context.Context, sel storage.Selection, step time.Duration) (Series, error) {
	kinds := []struct {
		name, label string
		events      []string
	}{
		{"sign_in_failures", "Failed sign-ins", []string{EventLoginFailed}},
		{"deadlocks", "Deadlocks", []string{EventDeadlock}},
		{"severe_errors", "Severe errors", SevereEvents()},
		{"read_retries", "Reads that needed a retry", []string{EventIORetrySucceeded}},
		{"backups", "Backups finished", []string{EventBackupDone, EventLogBackupDone}},
	}
	categories := make([]storage.Category, 0, len(kinds))
	for _, k := range kinds {
		categories = append(categories, storage.Category{
			Name: k.name, Field: FieldEventID, Phrases: k.events,
		})
	}
	rows, err := s.q.CategoryCounts(ctx, storage.CategoryQuery{
		Selection: sel, Categories: categories, DistinctField: FieldLoginUser, Step: step,
	})
	if err != nil {
		return Series{}, err
	}

	out := Series{Step: step, StepSeconds: int(step.Seconds())}
	index := map[int64]int{}
	for t := sel.Range.Start.Truncate(step); t.Before(sel.Range.End); t = t.Add(step) {
		index[t.UnixNano()] = len(out.Timestamps)
		out.Timestamps = append(out.Timestamps, t.UTC())
	}
	lines := map[string]*Line{}
	for _, k := range kinds {
		lines[k.name] = &Line{Name: k.name, Label: k.label, Points: make([]int64, len(out.Timestamps))}
	}
	for _, r := range rows {
		line, ok := lines[r.Category]
		if !ok {
			continue
		}
		i, ok := index[r.Time.UTC().UnixNano()]
		if !ok {
			continue
		}
		// Messages, not the distinct accounts the same pass also counted:
		// "how many failures" is the question, and the same account failing
		// twice is two failures. The distinct counts are left alone because
		// they cannot be added across buckets; the ones worth reporting are
		// in Overview, counted over the window they describe.
		line.Points[i] = int64(r.Messages)
		line.Total += int64(r.Messages)
	}
	for _, k := range kinds {
		out.Lines = append(out.Lines, *lines[k.name])
	}
	return out, nil
}

// Problem is one event worth seeing, with enough of it to act on.
type Problem struct {
	At time.Time `json:"at"`
	// Event is the error number.
	Event string `json:"event"`
	// What describes it in words, and Kind groups it.
	What string `json:"what"`
	Kind Kind   `json:"kind"`
	// Instance is the SQL Server instance, Host the server it runs on.
	Instance string `json:"instance,omitempty"`
	Host     string `json:"host,omitempty"`
	// Account and ClientIP are set only where the message carried them,
	// which in practice means a failed sign-in.
	Account  string `json:"account,omitempty"`
	ClientIP string `json:"client_ip,omitempty"`
	// Message is the event text. The fields above are what SQL Server puts
	// in a field; the database name, the file, the page and the offset are
	// only in here.
	Message string `json:"message,omitempty"`
}

// Problems lists what went wrong, newest first.
//
// Failed sign-ins are left out. There are usually thousands of them and they
// are already answered by the accounts and addresses they came from; mixing
// them in would bury the one 824 that matters.
func (s *Service) Problems(ctx context.Context, sel storage.Selection, limit int) ([]Problem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	ids := make([]string, 0, len(events))
	for _, id := range ProblemEvents() {
		if Classify(id) == KindLoginFailure {
			continue
		}
		ids = append(ids, id)
	}
	rows, err := s.q.Search(ctx, storage.SearchQuery{
		Selection: withEvents(sel, ids...),
		Fields: []string{"_time", FieldEventID, FieldProvider, FieldHost,
			FieldLoginUser, FieldClientIP, FieldMessage},
		Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Problem
	for rows.Next() {
		r := rows.Row()
		p := Problem{}
		if ts, ok := r.Get("_time"); ok {
			p.At, _ = time.Parse(time.RFC3339Nano, ts)
		}
		p.Event, _ = r.Get(FieldEventID)
		p.Instance, _ = r.Get(FieldProvider)
		p.Host, _ = r.Get(FieldHost)
		p.Account, _ = r.Get(FieldLoginUser)
		p.ClientIP, _ = r.Get(FieldClientIP)
		p.Message, _ = r.Get(FieldMessage)
		p.What, p.Kind = Describe(p.Event), Classify(p.Event)
		out = append(out, p)
	}
	return out, rows.Err()
}

// TopMessages ranks the commonest problem messages.
//
// This is less useful than it sounds, and is offered beside the ranking by
// error number rather than instead of it: SQL Server writes the database
// name, the file, the page and a byte offset into the sentence, so two 824s
// about the same broken file group as two different messages while a hundred
// identical "the transaction log is full" group as one. Read it as "the text
// that repeated", and read TopBy over the error number for "what went wrong
// most".
func (s *Service) TopMessages(ctx context.Context, sel storage.Selection, limit int) ([]Count, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := s.q.Aggregate(ctx, storage.AggregateQuery{
		Selection: withEvents(sel, ProblemEvents()...),
		GroupBy:   FieldMessage, Metric: storage.MetricCount, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Count, 0, len(rows))
	for _, r := range rows {
		if r.Group == "" || r.Group == "-" {
			continue
		}
		out = append(out, Count{Value: r.Group, Count: int64(r.Value)})
	}
	return out, nil
}
