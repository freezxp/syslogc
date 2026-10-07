// Package iis answers the questions people keep a web server's logs for:
// what is failing, what is slow, who is asking for it, and which server
// answered.
//
// Everything here is computed by aggregating in the log store rather than
// reading rows: one busy site writes more requests in an hour than anybody
// would page through, and a request log is the one place where that is
// normal rather than a sign of trouble.
//
// IIS records a response code and nothing that interprets it, so the
// interpretation is here: the class of a code (2xx, 4xx, 5xx) is the part
// almost every question is about, and it is derived from iis.status rather
// than taken from a field, because there is no such field.
//
// There is no average or worst response time anywhere in this package, and
// that is a limit rather than a choice. iis.time_taken arrives as text, and
// the aggregation available here counts rows and distinct values — it cannot
// sum or average a field. So "slow" is answered by counting the requests
// that crossed a stated threshold, which is a number that means exactly what
// it says, instead of by an average computed from however many rows happened
// to fit in a page of results, which would not.
package iis

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

// Fields the template produces, named once so a rename is one edit.
const (
	FieldStatus    = "iis.status"
	FieldSubStatus = "iis.substatus"
	FieldURI       = "iis.uri"
	FieldQuery     = "iis.query"
	FieldMethod    = "iis.method"
	FieldClientIP  = "iis.client_ip"
	FieldUsername  = "iis.username"
	FieldUserAgent = "iis.user_agent"
	FieldReferer   = "iis.referer"
	FieldServerIP  = "iis.server_ip"
	FieldPort      = "iis.port"
	FieldTimeTaken = "iis.time_taken"
)

// StatusAuthRequired is the code IIS writes when a request needed
// credentials and did not have usable ones.
const StatusAuthRequired = "401"

// SlowMillis is the point above which this calls a request slow. A second is
// roughly where somebody waiting stops assuming the page is loading and
// starts assuming it is broken.
const SlowMillis = 1000

// StatusClass is the first digit of a response code, which is the part most
// questions about a web server are actually about.
type StatusClass string

const (
	ClassInfo        StatusClass = "1xx"
	ClassSuccess     StatusClass = "2xx"
	ClassRedirect    StatusClass = "3xx"
	ClassClientError StatusClass = "4xx"
	ClassServerError StatusClass = "5xx"
)

// classLabels name a class in the words somebody reads.
var classLabels = map[StatusClass]string{
	ClassInfo:        "Informational",
	ClassSuccess:     "Succeeded",
	ClassRedirect:    "Redirected",
	ClassClientError: "Client errors",
	ClassServerError: "Server errors",
}

// ClassLabel names a class, or returns the class itself when it is not one
// of the five.
func ClassLabel(c StatusClass) string {
	if l, ok := classLabels[c]; ok {
		return l
	}
	return string(c)
}

// ClassOf derives the class of a response code as IIS wrote it.
//
// IIS writes the status as text, and writes a bare "-" wherever it had
// nothing to write, so a value here is not necessarily a code at all.
// Anything outside 100–599 belongs to no class, and that is reported rather
// than rounded into the nearest one: a request whose status cannot be read is
// not a success.
//
// It reads the value as a number rather than as three digits, and it does not
// trim, because that is what the store does to the same value when it counts
// these classes — classFilter is a numeric range. A label that disagrees with
// the count printed beside it is worse than no label, so the two are made to
// agree by construction and a test walks every value either could meet.
func ClassOf(status string) (StatusClass, bool) {
	n, err := strconv.ParseFloat(status, 64)
	// Written as a positive test so that NaN, which compares false against
	// everything, is rejected rather than passing both bounds.
	if err != nil || !(n >= 100 && n < 600) {
		return "", false
	}
	switch int(n / 100) {
	case 1:
		return ClassInfo, true
	case 2:
		return ClassSuccess, true
	case 3:
		return ClassRedirect, true
	case 4:
		return ClassClientError, true
	case 5:
		return ClassServerError, true
	}
	return "", false
}

// ErrorClasses are the two classes somebody opens this page to look at.
func ErrorClasses() []StatusClass { return []StatusClass{ClassClientError, ClassServerError} }

// chartClasses are the lines the activity chart carries, in the order it
// should stack them. 1xx is left out: IIS logs almost none, and a line that
// is always zero costs a colour and earns nothing. The lines therefore need
// not add up to the total number of requests, which is why Overview counts
// that separately.
var chartClasses = []StatusClass{ClassSuccess, ClassRedirect, ClassClientError, ClassServerError}

// classRanges are the half-open numeric ranges the classes cover.
var classRanges = map[StatusClass][2]int{
	ClassInfo:        {100, 200},
	ClassSuccess:     {200, 300},
	ClassRedirect:    {300, 400},
	ClassClientError: {400, 500},
	ClassServerError: {500, 600},
}

// classFilter selects one status class.
//
// The obvious filter is "the code starts with 5", and that is the filter this
// must not use: starts-with and contains both compile to a regular
// expression, which no index can answer, so it would be evaluated against
// every request in the window — and a request log is exactly where that is
// ruinous. A numeric range is the same question asked in a form the store
// evaluates against the values it has stored rather than against their text.
func classFilter(c StatusClass) *filter.Expr {
	r, ok := classRanges[c]
	if !ok {
		return nil
	}
	return &filter.Expr{Op: filter.And, Args: []*filter.Expr{
		{Op: filter.Gte, Field: FieldStatus, Value: strconv.Itoa(r[0])},
		{Op: filter.Lt, Field: FieldStatus, Value: strconv.Itoa(r[1])},
	}}
}

// slowFilter selects requests that took at least ms milliseconds.
//
// iis.time_taken is text holding a whole number of milliseconds, so this is
// a numeric range for the same reason classFilter is.
func slowFilter(ms int) *filter.Expr {
	return &filter.Expr{Op: filter.Gte, Field: FieldTimeTaken, Value: strconv.Itoa(ms)}
}

// statusTexts name the codes people meet. 401 is deliberately not called
// "unauthorized": the code means the request was not authenticated, and the
// standard's own word for it has sent people looking at permissions for
// twenty years.
var statusTexts = map[string]string{
	"200": "ok",
	"201": "created",
	"204": "no content",
	"206": "partial content",
	"301": "moved permanently",
	"302": "found",
	"304": "not modified",
	"307": "temporary redirect",
	"308": "permanent redirect",
	"400": "bad request",
	"401": "not authenticated",
	"403": "forbidden",
	"404": "not found",
	"405": "method not allowed",
	"408": "the client took too long to send the request",
	"409": "conflict",
	"413": "the request was too large",
	"429": "too many requests",
	"500": "the application failed",
	"501": "not implemented",
	"502": "bad gateway",
	"503": "service unavailable",
	"504": "gateway timeout",
}

// StatusText explains a response code, falling back to its class so an
// unfamiliar code is still placed rather than left bare.
func StatusText(status string) string {
	if t, ok := statusTexts[status]; ok {
		return t
	}
	if c, ok := ClassOf(status); ok {
		return ClassLabel(c)
	}
	return ""
}

// authSubStatuses explain IIS's own breakdown of a 401. The number after the
// dot is IIS's rather than HTTP's, and it is the difference between a wrong
// password and a permission on a folder — which is the whole question when
// somebody says they cannot sign in.
var authSubStatuses = map[string]string{
	"0": "no reason recorded",
	"1": "the credentials were wrong",
	"2": "the site is configured so that this authentication cannot succeed",
	"3": "the account has no permission on the file or folder",
	"4": "rejected by a filter",
	"5": "rejected by an ISAPI or CGI application",
}

// AuthSubStatus explains a 401 sub-status, or names the number when it is not
// one of IIS's documented ones.
func AuthSubStatus(sub string) string {
	s := strings.TrimSpace(sub)
	if t, ok := authSubStatuses[s]; ok {
		return t
	}
	if s == "" || s == "-" {
		return ""
	}
	return "sub-status " + s
}

// Querier is the part of the log store these analyses need.
type Querier interface {
	Aggregate(ctx context.Context, q storage.AggregateQuery) ([]storage.AggRow, error)
	CategoryCounts(ctx context.Context, q storage.CategoryQuery) ([]storage.CategoryRow, error)
	Count(ctx context.Context, q storage.CountQuery) (int64, error)
	Search(ctx context.Context, q storage.SearchQuery) (storage.Rows, error)
}

// anyRequest selects records that are IIS requests at all.
//
// Every other count in this package carries a filter on a field only an IIS
// record has, so it is restricted to IIS by construction. A plain total is
// not: without this it would report every log line in the window — the
// Security channel, the SQL Server errors, the lot — as a web request.
func anyRequest() *filter.Expr {
	return &filter.Expr{Op: filter.Exists, Field: FieldStatus}
}

// narrow adds an expression to a selection, keeping whatever the caller
// already asked for.
func narrow(sel storage.Selection, e *filter.Expr) storage.Selection {
	out := sel
	if e == nil {
		return out
	}
	if sel.Filter == nil {
		out.Filter = e
		return out
	}
	out.Filter = &filter.Expr{Op: filter.And, Args: []*filter.Expr{sel.Filter, e}}
	return out
}

// Overview is the state of the sites in the terms somebody asks about them.
//
// The class counts need not add up to Requests. 1xx is not counted, and
// neither is a line whose status field could not be read — a truncated log
// line, or a field IIS was not configured to write. The difference is
// visible on purpose rather than hidden by making one of the classes absorb
// it.
//
// Every distinct count is counted over the whole window it is reported for,
// because distinct counts do not add up: the clients seen in an hour are not
// the sum of the clients seen in each of its minutes.
type Overview struct {
	// Requests is every request in the window.
	Requests int64 `json:"requests"`
	// Counts per class.
	Informational int64 `json:"informational"`
	Succeeded     int64 `json:"succeeded"`
	Redirected    int64 `json:"redirected"`
	ClientErrors  int64 `json:"client_errors"`
	ServerErrors  int64 `json:"server_errors"`
	// AuthFailures counts 401s. They are also counted in ClientErrors; a 401
	// is a client error, and it is the client error worth its own number.
	AuthFailures int64 `json:"auth_failures"`
	// SlowRequests is how many requests took at least SlowThresholdMillis.
	// The threshold is reported beside it because the count means nothing
	// without it.
	SlowRequests        int64 `json:"slow_requests"`
	SlowThresholdMillis int   `json:"slow_threshold_millis"`
	// Clients, URLs and Servers are distinct values over the window.
	Clients int64 `json:"clients"`
	URLs    int64 `json:"urls"`
	Servers int64 `json:"servers"`
}

// Service answers the IIS questions.
type Service struct {
	q Querier
}

// New returns a service reading from q.
func New(q Querier) *Service { return &Service{q: q} }

// Overview counts the headline numbers over a window.
//
// The class split, the 401s and the slow requests are one query rather than
// seven: they are all conditional counts over the same scan of the same
// window, and asking for them separately would read a request log seven
// times to answer one page.
func (s *Service) Overview(ctx context.Context, sel storage.Selection, slowMillis int) (Overview, error) {
	if slowMillis <= 0 {
		slowMillis = SlowMillis
	}
	out := Overview{SlowThresholdMillis: slowMillis}
	// Narrowed once here so every number below is about IIS requests, and so
	// the store can skip whatever else the window holds.
	sel = narrow(sel, anyRequest())

	buckets := []struct {
		name string
		into *int64
		expr *filter.Expr
	}{
		{"informational", &out.Informational, classFilter(ClassInfo)},
		{"succeeded", &out.Succeeded, classFilter(ClassSuccess)},
		{"redirected", &out.Redirected, classFilter(ClassRedirect)},
		{"client_errors", &out.ClientErrors, classFilter(ClassClientError)},
		{"server_errors", &out.ServerErrors, classFilter(ClassServerError)},
		{"auth_failures", &out.AuthFailures, &filter.Expr{Op: filter.Eq, Field: FieldStatus, Value: StatusAuthRequired}},
		{"slow", &out.SlowRequests, slowFilter(slowMillis)},
	}
	categories := make([]storage.Category, 0, len(buckets))
	for _, b := range buckets {
		categories = append(categories, storage.Category{Name: b.name, Filter: b.expr})
	}
	// No step: one bucket covering the whole window, which is the window
	// every number below is reported for.
	rows, err := s.q.CategoryCounts(ctx, storage.CategoryQuery{
		Selection: sel, Categories: categories, DistinctField: FieldClientIP,
	})
	if err != nil {
		return out, err
	}
	into := map[string]*int64{}
	for _, b := range buckets {
		into[b.name] = b.into
	}
	for _, r := range rows {
		if p, ok := into[r.Category]; ok {
			*p = int64(r.Messages)
		}
	}

	n, err := s.q.Count(ctx, storage.CountQuery{Selection: sel})
	if err != nil {
		return out, err
	}
	out.Requests = n

	distinct := []struct {
		into  *int64
		field string
	}{
		{&out.Clients, FieldClientIP},
		{&out.URLs, FieldURI},
		{&out.Servers, FieldServerIP},
	}
	for _, d := range distinct {
		n, err := s.q.Count(ctx, storage.CountQuery{Selection: sel, DistinctField: d.field})
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

// TopBy counts the commonest values of a field across the whole selection.
func (s *Service) TopBy(ctx context.Context, sel storage.Selection, field string, limit int) ([]Count, error) {
	return s.topBy(ctx, sel, nil, field, limit)
}

// TopByClass counts the commonest values of a field among one status class —
// the URLs behind the 5xxs, for instance.
func (s *Service) TopByClass(ctx context.Context, sel storage.Selection, c StatusClass, field string, limit int) ([]Count, error) {
	return s.topBy(ctx, sel, classFilter(c), field, limit)
}

// TopByStatus counts the commonest values of a field among one exact status
// code, which is how the 401s are broken down.
func (s *Service) TopByStatus(ctx context.Context, sel storage.Selection, status, field string, limit int) ([]Count, error) {
	return s.topBy(ctx, sel, &filter.Expr{Op: filter.Eq, Field: FieldStatus, Value: status}, field, limit)
}

// SlowURLs ranks URLs by how many of their requests took at least ms
// milliseconds.
//
// This is not "the slowest URLs" and does not claim to be: it is the URLs
// with the most slow requests. A page called once that took a minute will
// not appear above a page called ten thousand times of which fifty were
// slow. The store cannot sum or average iis.time_taken, so the honest
// alternative to this count is no answer at all.
func (s *Service) SlowURLs(ctx context.Context, sel storage.Selection, ms, limit int) ([]Count, error) {
	if ms <= 0 {
		ms = SlowMillis
	}
	return s.topBy(ctx, sel, slowFilter(ms), FieldURI, limit)
}

func (s *Service) topBy(ctx context.Context, sel storage.Selection, extra *filter.Expr, field string, limit int) ([]Count, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := s.q.Aggregate(ctx, storage.AggregateQuery{
		Selection: narrow(sel, extra), GroupBy: field, Metric: storage.MetricCount, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Count, 0, len(rows))
	for _, r := range rows {
		// "-" is what IIS writes where it had nothing to write — no
		// authenticated user, no query string — and it is not a value
		// anybody wants ranked.
		if r.Group == "" || r.Group == "-" {
			continue
		}
		c := Count{Value: r.Group, Count: int64(r.Value)}
		switch field {
		case FieldStatus:
			c.Label = StatusText(r.Group)
		case FieldSubStatus:
			c.Label = AuthSubStatus(r.Group)
		}
		out = append(out, c)
	}
	return out, nil
}

// Series is activity over time, one line per class.
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

// chartLine is one line of the activity chart before it is counted.
type chartLine struct {
	name, label string
	expr        *filter.Expr
}

// Activity counts requests over time by status class, with the slow requests
// as a line of their own, in one pass.
//
// They are counted together because they are read together: a rise in 5xx
// means one thing on its own and another when the successes fell by the same
// amount, and a site that went slow before it started failing is a different
// incident from one that did not.
func (s *Service) Activity(ctx context.Context, sel storage.Selection, step time.Duration, slowMillis int) (Series, error) {
	if slowMillis <= 0 {
		slowMillis = SlowMillis
	}
	kinds := make([]chartLine, 0, len(chartClasses)+1)
	for _, c := range chartClasses {
		kinds = append(kinds, chartLine{string(c), ClassLabel(c), classFilter(c)})
	}
	kinds = append(kinds, chartLine{"slow", "Slower than " + strconv.Itoa(slowMillis) + " ms", slowFilter(slowMillis)})

	categories := make([]storage.Category, 0, len(kinds))
	for _, k := range kinds {
		categories = append(categories, storage.Category{Name: k.name, Filter: k.expr})
	}
	rows, err := s.q.CategoryCounts(ctx, storage.CategoryQuery{
		Selection:  narrow(sel, anyRequest()),
		Categories: categories, DistinctField: FieldClientIP, Step: step,
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
		// Requests, not the distinct clients the same pass also counted:
		// "how many requests" is the question. The distinct counts are left
		// alone because they cannot be added across buckets; the ones worth
		// reporting are in Overview, counted over the window they describe.
		line.Points[i] = int64(r.Messages)
		line.Total += int64(r.Messages)
	}
	for _, k := range kinds {
		out.Lines = append(out.Lines, *lines[k.name])
	}
	return out, nil
}

// Request is one request worth seeing, with enough of it to act on.
type Request struct {
	At time.Time `json:"at"`
	// Status is the response code and Class its class, so a caller need not
	// derive the class a second time.
	Status string      `json:"status"`
	Class  StatusClass `json:"class,omitempty"`
	// SubStatus is IIS's own further reason, where it recorded one.
	SubStatus string `json:"substatus,omitempty"`
	Method    string `json:"method,omitempty"`
	URI       string `json:"uri,omitempty"`
	Query     string `json:"query,omitempty"`
	ClientIP  string `json:"client_ip,omitempty"`
	Username  string `json:"username,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	ServerIP  string `json:"server_ip,omitempty"`
	// TimeTakenMillis is how long IIS says it took. It is parsed here rather
	// than passed through so a caller does not have to decide what a "-"
	// means; a value that is not a number is reported as absent.
	TimeTakenMillis *int64 `json:"time_taken_millis,omitempty"`
}

// Failures lists the most recent requests in one class, newest first. It is
// the detail behind the counts: the URL on its own rarely says why a 500
// happened, and the query string and the client often do.
func (s *Service) Failures(ctx context.Context, sel storage.Selection, c StatusClass, limit int) ([]Request, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.q.Search(ctx, storage.SearchQuery{
		Selection: narrow(sel, classFilter(c)),
		Fields: []string{"_time", FieldStatus, FieldSubStatus, FieldMethod, FieldURI, FieldQuery,
			FieldClientIP, FieldUsername, FieldUserAgent, FieldServerIP, FieldTimeTaken},
		Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Request
	for rows.Next() {
		r := rows.Row()
		req := Request{}
		if ts, ok := r.Get("_time"); ok {
			req.At, _ = time.Parse(time.RFC3339Nano, ts)
		}
		req.Status, _ = r.Get(FieldStatus)
		req.SubStatus, _ = r.Get(FieldSubStatus)
		req.Method, _ = r.Get(FieldMethod)
		req.URI, _ = r.Get(FieldURI)
		req.Query, _ = r.Get(FieldQuery)
		req.ClientIP, _ = r.Get(FieldClientIP)
		req.Username, _ = r.Get(FieldUsername)
		req.UserAgent, _ = r.Get(FieldUserAgent)
		req.ServerIP, _ = r.Get(FieldServerIP)
		if class, ok := ClassOf(req.Status); ok {
			req.Class = class
		}
		if v, ok := r.Get(FieldTimeTaken); ok {
			if ms, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				req.TimeTakenMillis = &ms
			}
		}
		out = append(out, req)
	}
	return out, rows.Err()
}
