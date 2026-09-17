// Package logentry defines the normalized in-memory log model shared by
// parsers, normalization, the ingestion pipeline and storage adapters.
// It has no dependencies on other Syslogc packages.
package logentry

import (
	"net/netip"
	"time"
)

// Field is a flattened dynamic key/value pair.
//
// Values are strings: syslog carries no types, and storage (VictoriaLogs)
// stores strings and infers numeric/IP types per column itself. Typed values
// will be introduced together with a typed storage backend.
type Field struct {
	Key   string
	Value string
}

// Entry is one normalized log record. See docs/log-data-model.md.
type Entry struct {
	// Time is the event time (storage _time). Zero until normalization
	// resolves it from the parsed timestamp or ReceivedAt.
	Time       time.Time
	ReceivedAt time.Time
	Message    string

	Hostname  string
	AppName   string
	ProcessID string
	MessageID string

	SourceIP   netip.Addr
	SourcePort uint16
	PeerIP     netip.Addr

	Facility Facility
	Severity Severity

	Protocol   Protocol
	Format     Format
	Source     string
	SourceType SourceType
	Tenant     string

	// Raw is the original message as received, if retained by policy.
	Raw string
	// ParseError describes a failed or partial parse; empty on success.
	ParseError string
	// TimestampRaw holds the original timestamp text when it was unusable.
	TimestampRaw   string
	TimeSource     TimeSource
	SeveritySource SeveritySource
	// Truncated is set when the message or a field value was truncated.
	Truncated bool
	// FieldsDropped counts dynamic fields dropped by limits.
	FieldsDropped int

	// Labels are static operator-assigned labels (shared, read-only slice).
	Labels []Field
	// Fields are dynamic fields in encounter order.
	Fields []Field
}

// Reset clears e for reuse, keeping the Fields backing array. A reset entry
// has no facility and a defaulted "info" severity until a parser sets them.
func (e *Entry) Reset() {
	fields := e.Fields[:0]
	*e = Entry{
		Fields:         fields,
		Facility:       NoFacility,
		Severity:       SeverityInfo,
		SeveritySource: SeverityDefault,
	}
}

// AddField appends a dynamic field.
func (e *Entry) AddField(key, value string) {
	e.Fields = append(e.Fields, Field{Key: key, Value: value})
}

// Priority returns the syslog PRI value and whether it is defined
// (it requires a facility).
func (e *Entry) Priority() (int, bool) {
	if !e.Facility.Valid() {
		return 0, false
	}
	return int(e.Facility)*8 + int(e.Severity), true
}

// Batch is a group of entries for one tenant written to storage together.
type Batch struct {
	Tenant  string
	Entries []Entry
	// Bytes is the approximate encoded size of the entries.
	Bytes int
}

// Reset clears b for reuse, keeping allocated capacity.
func (b *Batch) Reset() {
	for i := range b.Entries {
		b.Entries[i].Reset()
	}
	b.Entries = b.Entries[:0]
	b.Bytes = 0
	b.Tenant = ""
}

// Next returns a pointer to a reset entry appended to the batch, reusing
// previously allocated entries where possible.
func (b *Batch) Next() *Entry {
	n := len(b.Entries)
	if n < cap(b.Entries) {
		b.Entries = b.Entries[:n+1]
	} else {
		b.Entries = append(b.Entries, Entry{})
	}
	e := &b.Entries[n]
	e.Reset()
	return e
}

// EstimateSize approximates the encoded JSON size of e in bytes. It is used
// for byte budgets, so it errs on the high side.
func (e *Entry) EstimateSize() int {
	n := 400 + len(e.Message)*11/10 + len(e.Raw)*11/10 + len(e.Hostname) + len(e.AppName) +
		len(e.ProcessID) + len(e.MessageID) + len(e.Source) + len(e.ParseError) + len(e.TimestampRaw)
	for _, f := range e.Labels {
		n += len(f.Key) + len(f.Value) + 16
	}
	for _, f := range e.Fields {
		n += len(f.Key) + len(f.Value)*11/10 + 6
	}
	return n
}
