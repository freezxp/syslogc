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

// LogQuerier reads logs. The Phase 1 surface is intentionally minimal; the
// full query API (filter AST, histograms, facets, fields, tail, export)
// arrives in Phase 3.
type LogQuerier interface {
	// Search returns up to q.Limit matching rows, newest first.
	Search(ctx context.Context, q SearchQuery) (Rows, error)
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

// Selection is shared by all read queries.
type Selection struct {
	Tenant string
	Range  TimeRange
	Native *NativeQuery
}

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
