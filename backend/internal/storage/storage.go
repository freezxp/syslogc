// Package storage defines the backend-neutral storage contract.
//
// Only the application composition root imports concrete adapters; every
// other package depends on these interfaces. See ADR-0002.
package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

// Backend is implemented once per storage engine.
type Backend interface {
	Name() string
	Capabilities() Capabilities
	Writer() LogWriter
	Querier() LogQuerier
	Admin() Admin
	// Ping checks that the backend is reachable.
	Ping(ctx context.Context) error
	Close() error
}

// Capabilities declares optional backend features so callers can adapt
// instead of adapters emulating behaviour.
type Capabilities struct {
	NativeDialects     []string
	NativeTail         bool
	NativeFacets       bool
	PerTenantRetention bool
}

// SupportsDialect reports whether native queries in dialect are accepted.
func (c Capabilities) SupportsDialect(dialect string) bool {
	for _, d := range c.NativeDialects {
		if d == dialect {
			return true
		}
	}
	return false
}

// LogWriter stores batches. Implementations must be safe for concurrent use.
type LogWriter interface {
	// WriteBatch stores all entries of batch for batch.Tenant. Errors should
	// be *WriteError values so callers can decide whether to retry.
	WriteBatch(ctx context.Context, batch *logentry.Batch) error
}

// LogQuerier reads logs. Aggregations are computed by the backend, never by
// scanning rows in the application.
type LogQuerier interface {
	// Describe compiles the selection to native query text (for display) and
	// reports whether the native part contains pipes (tabular results).
	Describe(sel Selection) (native string, hasPipes bool, err error)
	// Search returns up to q.Limit matching rows, newest first. The
	// selection must not contain pipes.
	Search(ctx context.Context, q SearchQuery) (Rows, error)
	// Table runs a selection whose native part has pipes and returns up to
	// q.Limit result rows.
	Table(ctx context.Context, q SearchQuery) (Rows, error)
	// Hits returns bucketed counts, optionally split by a field.
	Hits(ctx context.Context, q HitsQuery) ([]HitsSeries, error)
	// Top returns the most frequent non-empty values of a field.
	Top(ctx context.Context, q TopQuery) ([]ValueCount, error)
	// Count counts matching rows, or distinct values of DistinctField.
	Count(ctx context.Context, q CountQuery) (int64, error)
	// Aggregate groups matching rows by a field, by time, or both, and
	// returns one metric value per group.
	Aggregate(ctx context.Context, q AggregateQuery) ([]AggRow, error)
	// CategoryCounts counts distinct values per category in one pass.
	CategoryCounts(ctx context.Context, q CategoryQuery) ([]CategoryRow, error)
	// FieldNames lists field names present in the selection. Counts may be approximate.
	FieldNames(ctx context.Context, sel Selection) ([]FieldInfo, error)
	// Tail streams rows ingested after the call until ctx is done.
	Tail(ctx context.Context, q TailQuery) (Rows, error)
}

// Metrics an AggregateQuery can compute.
const (
	MetricCount    = "count"
	MetricDistinct = "count_distinct"
)

// AggregateQuery groups rows and computes one metric per group. Step buckets
// by time; GroupBy groups by a field; either or both may be set. The
// selection must not contain pipes.
type AggregateQuery struct {
	Selection
	// Step buckets results by time when non-zero.
	Step time.Duration
	// GroupBy is the field to group by; empty groups everything together.
	GroupBy string
	// Metric is MetricCount or MetricDistinct.
	Metric string
	// MetricField is the field whose distinct values are counted.
	MetricField string
	// Limit caps the number of groups returned, highest metric first. It
	// applies only when Step is zero; time series are limited by the caller
	// restricting GroupBy values.
	Limit int
}

// AggRow is one aggregated group.
type AggRow struct {
	// Time is the bucket start, zero when the query had no Step.
	Time time.Time
	// Group is the GroupBy value, empty when grouping everything.
	Group string
	Value float64
}

// MaxCategories bounds how many categories one CategoryQuery may count,
// because each adds two aggregations to the same pass.
const MaxCategories = 32

// CategoryQuery counts, per category and time bucket, how many distinct
// values of DistinctField matched the category.
//
// Categories are counted in one pass over the selection. Each is counted
// independently: a client that matches two categories counts once in each,
// and the categories together need not cover the selection. Distinct counts
// are never additive — the count for an hour is not the sum of its minutes —
// so each resolution a caller wants must be queried at that resolution.
type CategoryQuery struct {
	Selection
	// Categories are the buckets to count into; at most MaxCategories.
	Categories []Category
	// DistinctField is the field whose distinct values are counted.
	DistinctField string
	// Step buckets by time when non-zero; otherwise the whole range is one
	// bucket.
	Step time.Duration
}

// Category is one named bucket of a CategoryQuery.
type Category struct {
	// Name identifies the category in the results.
	Name string
	// Filter selects the messages that belong to it. A nil filter matches
	// everything in the selection, unless Phrases is set.
	Filter *filter.Expr
	// Field and Phrases select by phrase instead, which is what a domain
	// name is: "tiktok.com" matches www.tiktok.com and api.tiktok.com but
	// not nottiktok.com, and — unlike a substring or a regular expression —
	// the backend can answer it from its index. A category counting
	// hundreds of domains is otherwise hundreds of expressions evaluated
	// against every row.
	//
	// Phrases take precedence over Filter when both are set.
	Field   string
	Phrases []string
}

// CategoryRow is one category's counts in one time bucket.
type CategoryRow struct {
	// Time is the bucket start, zero when the query had no Step.
	Time time.Time
	// Category is the category name.
	Category string
	// Distinct is how many distinct values of DistinctField matched.
	Distinct float64
	// Messages is how many messages matched.
	Messages float64
}

// Admin exposes operational information about the backend.
type Admin interface {
	Retention(ctx context.Context) (RetentionInfo, error)
	Usage(ctx context.Context) (UsageInfo, error)
}

// TimeRange is the half-open interval [Start, End).
type TimeRange struct {
	Start time.Time
	End   time.Time
}

// Validate checks that the range is non-empty.
func (r TimeRange) Validate() error {
	if r.Start.IsZero() || r.End.IsZero() {
		return errors.New("time range start and end are required")
	}
	if !r.Start.Before(r.End) {
		return errors.New("time range start must be before end")
	}
	return nil
}

// NativeQuery is query text in a backend-specific dialect (e.g. "logsql").
type NativeQuery struct {
	Dialect string
	Text    string
}

// Selection is shared by all read queries. Filter and Native are AND-ed.
type Selection struct {
	Tenant string
	Range  TimeRange
	Filter *filter.Expr
	Native *NativeQuery
}

// HitsQuery requests bucketed counts.
type HitsQuery struct {
	Selection
	Step time.Duration
	// Offset shifts bucket boundaries (e.g. to align days to a timezone).
	Offset time.Duration
	// Field splits counts by its values; empty means no split.
	Field      string
	FieldLimit int
}

// HitsSeries is one series of bucket counts.
type HitsSeries struct {
	// Value is the split field value; Other marks the merged remainder.
	Value      string
	Other      bool
	Timestamps []time.Time
	Counts     []int64
	Total      int64
}

// TopQuery requests the most frequent values of a field.
type TopQuery struct {
	Selection
	Field string
	Limit int
	// Search keeps only values containing this substring (case-insensitive).
	Search string
}

// ValueCount is a value with its number of occurrences.
type ValueCount struct {
	Value string
	Count int64
}

// CountQuery counts rows or distinct values.
type CountQuery struct {
	Selection
	DistinctField string
}

// FieldInfo describes a field present in stored logs.
type FieldInfo struct {
	Name  string
	Count int64
}

// TailQuery selects rows for live tailing. Selection.Range is ignored.
type TailQuery struct {
	Selection
	StartOffset time.Duration
}

// ErrPipesNotAllowed is returned when a query type cannot run pipes.
var ErrPipesNotAllowed = errors.New("native query pipes are not allowed here")

// SearchQuery selects log rows.
type SearchQuery struct {
	Selection
	// Fields limits returned fields; empty means all fields.
	Fields []string
	Limit  int
}

// Row is one log record as ordered storage fields.
type Row []logentry.Field

// Get returns the value of field key.
func (r Row) Get(key string) (string, bool) {
	for _, f := range r {
		if f.Key == key {
			return f.Value, true
		}
	}
	return "", false
}

// Rows is a pull iterator over query results. Close must always be called.
type Rows interface {
	Next() bool
	Row() Row
	Err() error
	Close() error
}

// RetentionInfo describes the effective retention reported by the backend.
type RetentionInfo struct {
	Period time.Duration
	// Explicit is false when the backend is running with its default.
	Explicit bool
}

// UsageInfo describes storage consumption.
type UsageInfo struct {
	CompressedBytes   int64
	UncompressedBytes int64
	FreeDiskBytes     int64
	TotalDiskBytes    int64
	Partitions        int64
}

// Errors shared by adapters.
var (
	ErrUnsupportedDialect = errors.New("unsupported query dialect")
	ErrUnknownTenant      = errors.New("unknown tenant")
)

// ErrorClass tells callers how to react to a write error.
type ErrorClass uint8

const (
	// Retryable errors are transient (network, timeouts, overload).
	Retryable ErrorClass = iota
	// Rejected means the backend refused the data itself; retrying the same
	// rows will not succeed.
	Rejected
	// Fatal means misconfiguration (authentication, wrong URL); retrying may
	// succeed only after operator intervention.
	Fatal
)

func (c ErrorClass) String() string {
	switch c {
	case Retryable:
		return "retryable"
	case Rejected:
		return "rejected"
	case Fatal:
		return "fatal"
	}
	return "unknown"
}

// WriteError is a classified write failure.
type WriteError struct {
	Class      ErrorClass
	StatusCode int
	Err        error
}

func (e *WriteError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("storage write %s (HTTP %d): %v", e.Class, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("storage write %s: %v", e.Class, e.Err)
}

func (e *WriteError) Unwrap() error { return e.Err }

// ClassOf classifies err. Unclassified errors are treated as retryable.
func ClassOf(err error) ErrorClass {
	var we *WriteError
	if errors.As(err, &we) {
		return we.Class
	}
	return Retryable
}

// QueryError is a failed query. Message is safe to show to users when
// UserFacing is set (e.g. syntax errors).
type QueryError struct {
	StatusCode int
	Message    string
	UserFacing bool
}

func (e *QueryError) Error() string {
	return fmt.Sprintf("storage query failed (HTTP %d): %s", e.StatusCode, e.Message)
}
