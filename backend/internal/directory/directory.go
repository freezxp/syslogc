// Package directory answers the questions people keep a domain controller's
// logs for: who signed in, who could not, whose account locked, and what
// changed.
//
// Everything here is computed from fields the Active Directory template
// produces, by aggregating in the log store rather than reading rows: a
// domain of any size writes far too many sign-in events to count in the
// application.
//
// Windows says what happened through an event id. The ids are the stable
// part — the text beside them is written in the server's own language — so
// they are what this reads.
package directory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

// The Security events these analyses are built from.
const (
	EventLogonSuccess          = "4624" // an account signed in
	EventLogonFailure          = "4625" // an account failed to sign in
	EventLogoff                = "4634" // a session ended
	EventLogoffUser            = "4647" // the person signed out themselves
	EventPrivilegedLogon       = "4672" // a sign-in carrying administrative rights
	EventLockout               = "4740" // an account was locked out
	EventUnlocked              = "4767" // and unlocked again
	EventPasswordReset         = "4724" // an administrator reset a password
	EventPasswordChange        = "4723" // the person changed their own
	EventAccountCreated        = "4720"
	EventAccountEnabled        = "4722"
	EventAccountDisabled       = "4725"
	EventAccountDeleted        = "4726"
	EventGroupAddLocal         = "4732"
	EventGroupAddGlobal        = "4728"
	EventGroupAddUniversal     = "4756"
	EventGroupRemoveLocal      = "4733"
	EventGroupRemoveGlobal     = "4729"
	EventGroupRemoveUniversal  = "4757"
	EventKerberosTGT           = "4768" // a ticket was granted
	EventKerberosService       = "4769"
	EventKerberosPreauthFailed = "4771" // usually a wrong password
	EventPolicyChanged         = "4719"
)

// Fields the template produces, named once so a rename is one edit.
const (
	FieldEventID     = "ad.event_id"
	FieldUser        = "ad.user"
	FieldDomain      = "ad.domain"
	FieldLogonType   = "ad.logon_type"
	FieldSourceIP    = "ad.source_ip"
	FieldWorkstation = "ad.workstation"
	FieldCaller      = "ad.caller_computer"
	FieldStatus      = "ad.status"
	FieldDC          = "ad.dc"
	FieldMember      = "ad.member"
	FieldActor       = "ad.actor"
)

// LogonType describes how somebody signed in. The number is what Windows
// records; the name is what a person reads.
var logonTypes = map[string]string{
	"2":  "console",
	"3":  "network",
	"4":  "batch",
	"5":  "service",
	"7":  "unlock",
	"8":  "network (cleartext)",
	"9":  "new credentials",
	"10": "remote desktop",
	"11": "cached console",
}

// LogonTypeName renders a logon type, falling back to the number so an
// unfamiliar one is still shown rather than hidden.
func LogonTypeName(code string) string {
	if name, ok := logonTypes[strings.TrimSpace(code)]; ok {
		return name
	}
	if code == "" {
		return ""
	}
	return "type " + code
}

// failureReasons translate the status codes people actually meet. Windows
// reports the same "wrong credentials" for a bad password and an unknown
// account on purpose, so an attacker cannot tell them apart; this says so
// rather than pretending to know which.
var failureReasons = map[string]string{
	"0xC000006D": "wrong user name or password",
	"0xC000006A": "wrong password",
	"0xC0000064": "no such account",
	"0xC0000234": "account locked out",
	"0xC0000072": "account disabled",
	"0xC0000070": "not allowed to sign in from this machine",
	"0xC000006F": "outside the hours this account may sign in",
	"0xC0000193": "account expired",
	"0xC0000071": "password expired",
	"0xC0000224": "must change password at next sign-in",
	"0xC0000413": "blocked by an authentication policy",
}

// FailureReason explains a status code in a sentence, or returns the code
// when it is not one we know.
//
// Codes arrive in whatever case the sender used, so the prefix and the digits
// are normalised separately: upper-casing the whole string would turn 0x into
// 0X and match nothing, which is a quiet way to show hex to people instead of
// words.
func FailureReason(status string) string {
	if r, ok := failureReasons[normalStatus(status)]; ok {
		return r
	}
	return status
}

func normalStatus(status string) string {
	s := strings.TrimSpace(status)
	if rest, ok := strings.CutPrefix(strings.ToLower(s), "0x"); ok {
		return "0x" + strings.ToUpper(rest)
	}
	return strings.ToUpper(s)
}

// Querier is the part of the log store these analyses need.
type Querier interface {
	Aggregate(ctx context.Context, q storage.AggregateQuery) ([]storage.AggRow, error)
	CategoryCounts(ctx context.Context, q storage.CategoryQuery) ([]storage.CategoryRow, error)
	Count(ctx context.Context, q storage.CountQuery) (int64, error)
	Search(ctx context.Context, q storage.SearchQuery) (storage.Rows, error)
}

// eventFilter matches one or more event ids.
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
	ev := eventFilter(ids...)
	if sel.Filter == nil {
		out.Filter = ev
		return out
	}
	out.Filter = &filter.Expr{Op: filter.And, Args: []*filter.Expr{sel.Filter, ev}}
	return out
}

// Overview is the state of the domain in the terms somebody asks about it.
type Overview struct {
	// SignedIn is how many distinct accounts signed in over the window and
	// have not signed out. It is an estimate and says so: a workstation that
	// loses power never sends the sign-out, so the number drifts upwards
	// until the session is counted as ended.
	SignedIn int64 `json:"signed_in"`
	// Accounts is how many distinct accounts signed in at all.
	Accounts int64 `json:"accounts"`
	// Logons, Failures and Lockouts count events over the window.
	Logons   int64 `json:"logons"`
	Failures int64 `json:"failures"`
	Lockouts int64 `json:"lockouts"`
	// PrivilegedLogons are sign-ins that carried administrative rights.
	PrivilegedLogons int64 `json:"privileged_logons"`
	// AccountChanges counts accounts created, deleted, enabled or disabled.
	AccountChanges int64 `json:"account_changes"`
	// GroupChanges counts membership added or removed.
	GroupChanges int64 `json:"group_changes"`
}

// Service answers the directory questions.
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
		{&out.Logons, []string{EventLogonSuccess}},
		{&out.Failures, []string{EventLogonFailure}},
		{&out.Lockouts, []string{EventLockout}},
		{&out.PrivilegedLogons, []string{EventPrivilegedLogon}},
		{&out.AccountChanges, []string{EventAccountCreated, EventAccountDeleted, EventAccountEnabled, EventAccountDisabled}},
		{&out.GroupChanges, []string{EventGroupAddLocal, EventGroupAddGlobal, EventGroupAddUniversal,
			EventGroupRemoveLocal, EventGroupRemoveGlobal, EventGroupRemoveUniversal}},
	}
	for _, c := range counts {
		n, err := s.q.Count(ctx, storage.CountQuery{Selection: withEvents(sel, c.events...)})
		if err != nil {
			return out, err
		}
		*c.into = n
	}
	// Distinct accounts that signed in, and distinct accounts that signed
	// out. One minus the other is how many are still signed in — an
	// estimate, because a machine that is switched off never says so.
	in, err := s.q.Count(ctx, storage.CountQuery{
		Selection: withEvents(sel, EventLogonSuccess), DistinctField: FieldUser})
	if err != nil {
		return out, err
	}
	gone, err := s.q.Count(ctx, storage.CountQuery{
		Selection: withEvents(sel, EventLogoff, EventLogoffUser), DistinctField: FieldUser})
	if err != nil {
		return out, err
	}
	out.Accounts = in
	out.SignedIn = max(in-gone, 0)
	return out, nil
}

// Lockout is one account locking, with what caused it.
type Lockout struct {
	At time.Time `json:"at"`
	// User is the account that locked.
	User string `json:"user"`
	// Caller is the machine whose attempts locked it, as the domain
	// controller recorded it. This is the answer to "what is doing this" —
	// usually a phone with an old password, or a service running as the
	// account.
	Caller string `json:"caller,omitempty"`
	// SourceIP is where the failures came from, when the failures that
	// caused the lockout name one.
	SourceIP string `json:"source_ip,omitempty"`
	// DC is the domain controller that locked the account.
	DC string `json:"dc,omitempty"`
	// FailuresBefore counts failed sign-ins for this account in the minutes
	// before the lockout, which is what identifies the culprit when the
	// lockout event itself names only a machine.
	FailuresBefore int64 `json:"failures_before"`
}

// Lockouts lists accounts that locked, newest first, with what caused each.
//
// The lockout event names the machine but not always the address, so the
// failures just before it are read as well: that is where the address is, and
// it is the question everybody asks next.
func (s *Service) Lockouts(ctx context.Context, sel storage.Selection, limit int) ([]Lockout, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.q.Search(ctx, storage.SearchQuery{
		Selection: withEvents(sel, EventLockout),
		Fields:    []string{"_time", FieldUser, FieldCaller, FieldDC, FieldSourceIP},
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Lockout
	for rows.Next() {
		r := rows.Row()
		l := Lockout{}
		if ts, ok := r.Get("_time"); ok {
			l.At, _ = time.Parse(time.RFC3339Nano, ts)
		}
		l.User, _ = r.Get(FieldUser)
		l.Caller, _ = r.Get(FieldCaller)
		l.DC, _ = r.Get(FieldDC)
		l.SourceIP, _ = r.Get(FieldSourceIP)
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fill in where the failures came from. One query per lockout, which is
	// why the list is short: the alternative is one enormous query whose
	// results have to be matched up here anyway.
	for i := range out {
		if out[i].User == "" {
			continue
		}
		ip, n, err := s.failuresBefore(ctx, sel, out[i].User, out[i].At)
		if err != nil {
			continue // the lockout itself is worth showing without this
		}
		out[i].FailuresBefore = n
		if out[i].SourceIP == "" {
			out[i].SourceIP = ip
		}
	}
	return out, nil
}

// lockoutWindow is how far back to look for the failures that caused a
// lockout. Windows counts them over the domain's lockout window, which is
// typically well under this.
const lockoutWindow = 30 * time.Minute

// failuresBefore finds where an account's failed sign-ins came from in the
// window before it locked, and how many there were.
func (s *Service) failuresBefore(ctx context.Context, sel storage.Selection, user string, at time.Time) (string, int64, error) {
	if at.IsZero() {
		return "", 0, nil
	}
	window := sel
	window.Range = storage.TimeRange{Start: at.Add(-lockoutWindow), End: at.Add(time.Minute)}
	window.Filter = &filter.Expr{Op: filter.And, Args: []*filter.Expr{
		eventFilter(EventLogonFailure, EventKerberosPreauthFailed),
		{Op: filter.Eq, Field: FieldUser, Value: user},
	}}
	rows, err := s.q.Aggregate(ctx, storage.AggregateQuery{
		Selection: window, GroupBy: FieldSourceIP, Metric: storage.MetricCount, Limit: 5,
	})
	if err != nil {
		return "", 0, err
	}
	var total int64
	best, bestCount := "", 0.0
	for _, r := range rows {
		total += int64(r.Value)
		// The address responsible is the one that tried most: a lockout is
		// repetition, and anything else in the window is incidental.
		if r.Value > bestCount && r.Group != "" && r.Group != "-" {
			best, bestCount = r.Group, r.Value
		}
	}
	return best, total, nil
}

// Count is one value and how often it occurred.
type Count struct {
	Value string `json:"value"`
	// Label is Value said in words, where there is a better way to say it.
	Label string `json:"label,omitempty"`
	Count int64  `json:"count"`
}

// TopBy counts the commonest values of a field among the given events.
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
		if r.Group == "" || r.Group == "-" {
			continue
		}
		c := Count{Value: r.Group, Count: int64(r.Value)}
		switch field {
		case FieldLogonType:
			c.Label = LogonTypeName(r.Group)
		case FieldStatus:
			c.Label = FailureReason(r.Group)
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

// Activity counts sign-ins, failures and lockouts over time, in one pass.
//
// They are counted together because they are read together: a rise in
// failures matters when sign-ins are flat, and a lockout matters more when
// failures rose before it.
func (s *Service) Activity(ctx context.Context, sel storage.Selection, step time.Duration) (Series, error) {
	kinds := []struct {
		name, label string
		events      []string
	}{
		{"logons", "Sign-ins", []string{EventLogonSuccess}},
		{"failures", "Failed sign-ins", []string{EventLogonFailure, EventKerberosPreauthFailed}},
		{"lockouts", "Lockouts", []string{EventLockout}},
		{"privileged", "Privileged sign-ins", []string{EventPrivilegedLogon}},
	}
	categories := make([]storage.Category, 0, len(kinds))
	for _, k := range kinds {
		categories = append(categories, storage.Category{
			Name: k.name, Field: FieldEventID, Phrases: k.events,
		})
	}
	rows, err := s.q.CategoryCounts(ctx, storage.CategoryQuery{
		Selection: sel, Categories: categories, DistinctField: FieldUser, Step: step,
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
		// Messages, not distinct users: "how many sign-ins" is the question,
		// and the same person signing in twice is two sign-ins.
		line.Points[i] = int64(r.Messages)
		line.Total += int64(r.Messages)
	}
	for _, k := range kinds {
		out.Lines = append(out.Lines, *lines[k.name])
	}
	return out, nil
}

// Change is one account or group change worth seeing.
type Change struct {
	At    time.Time `json:"at"`
	Event string    `json:"event"`
	// What describes the event in words.
	What string `json:"what"`
	// Subject is the account the change was made to.
	Subject string `json:"subject,omitempty"`
	// Actor is who made it.
	Actor  string `json:"actor,omitempty"`
	Member string `json:"member,omitempty"`
	DC     string `json:"dc,omitempty"`
}

// changeDescriptions name each change in the words somebody reviewing them
// would use.
var changeDescriptions = map[string]string{
	EventAccountCreated:       "account created",
	EventAccountDeleted:       "account deleted",
	EventAccountEnabled:       "account enabled",
	EventAccountDisabled:      "account disabled",
	EventPasswordReset:        "password reset by an administrator",
	EventPasswordChange:       "password changed by the user",
	EventLockout:              "account locked out",
	EventUnlocked:             "account unlocked",
	EventGroupAddLocal:        "added to a local group",
	EventGroupAddGlobal:       "added to a global group",
	EventGroupAddUniversal:    "added to a universal group",
	EventGroupRemoveLocal:     "removed from a local group",
	EventGroupRemoveGlobal:    "removed from a global group",
	EventGroupRemoveUniversal: "removed from a universal group",
	EventPolicyChanged:        "audit policy changed",
}

// ChangeEvents are the ids Changes reads, in one place so the filter and the
// descriptions cannot drift apart.
func ChangeEvents() []string {
	out := make([]string, 0, len(changeDescriptions))
	for id := range changeDescriptions {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Changes lists what was done to accounts and groups, newest first.
func (s *Service) Changes(ctx context.Context, sel storage.Selection, limit int) ([]Change, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.q.Search(ctx, storage.SearchQuery{
		Selection: withEvents(sel, ChangeEvents()...),
		Fields:    []string{"_time", FieldEventID, FieldUser, FieldActor, FieldMember, FieldDC},
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Change
	for rows.Next() {
		r := rows.Row()
		c := Change{}
		if ts, ok := r.Get("_time"); ok {
			c.At, _ = time.Parse(time.RFC3339Nano, ts)
		}
		c.Event, _ = r.Get(FieldEventID)
		c.Subject, _ = r.Get(FieldUser)
		c.Actor, _ = r.Get(FieldActor)
		c.Member, _ = r.Get(FieldMember)
		c.DC, _ = r.Get(FieldDC)
		c.What = changeDescriptions[c.Event]
		if c.What == "" {
			c.What = fmt.Sprintf("event %s", c.Event)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
