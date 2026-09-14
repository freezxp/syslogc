package query

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/freezxp/syslogc/backend/internal/storage"
)

// LogRow is the API representation of a log (docs/log-data-model.md §9).
type LogRow map[string]any

// intFields are core fields rendered as JSON numbers.
var intFields = map[string]bool{
	"source_port": true, "facility_code": true, "severity_code": true, "priority": true, "fields_dropped": true,
}

// stringFields are core fields rendered as top-level strings.
var stringFields = map[string]bool{
	"received_at": true, "hostname": true, "source_ip": true, "peer_ip": true, "facility": true, "severity": true,
	"protocol": true, "format": true, "app_name": true, "process_id": true, "message_id": true, "source": true,
	"source_type": true, "raw_message": true, "parse_error": true, "time_source": true, "severity_source": true,
	"timestamp_raw": true,
}

// fieldKind classifies an API field name.
func fieldKind(name string) string {
	switch {
	case name == "message" || name == "timestamp" || intFields[name] || stringFields[name] || name == "truncated":
		return "core"
	case strings.HasPrefix(name, "labels."):
		return "label"
	}
	return "dynamic"
}

// shapeRow converts a storage row to the API representation.
func shapeRow(row storage.Row, canViewRaw bool) LogRow {
	out := LogRow{}
	ref := map[string]string{"stream_id": "", "time_ns": ""}
	var labels, fields map[string]string
	for _, f := range row {
		switch {
		case f.Key == "_time":
			out["timestamp"] = f.Value
			if t, err := time.Parse(time.RFC3339Nano, f.Value); err == nil {
				ref["time_ns"] = strconv.FormatInt(t.UnixNano(), 10)
			}
		case f.Key == "_msg":
			out["message"] = f.Value
		case f.Key == "_stream_id":
			ref["stream_id"] = f.Value
		case f.Key == "_stream":
		case f.Key == "raw_message":
			if canViewRaw {
				out["raw_message"] = f.Value
			}
		case intFields[f.Key]:
			if n, err := strconv.ParseInt(f.Value, 10, 64); err == nil {
				out[f.Key] = n
			} else {
				out[f.Key] = f.Value
			}
		case f.Key == "truncated":
			out["truncated"] = f.Value == "true"
		case stringFields[f.Key]:
			out[f.Key] = f.Value
		case strings.HasPrefix(f.Key, "labels."):
			if labels == nil {
				labels = map[string]string{}
			}
			labels[strings.TrimPrefix(f.Key, "labels.")] = f.Value
		default:
			if fields == nil {
				fields = map[string]string{}
			}
			fields[f.Key] = f.Value
		}
	}
	if labels != nil {
		out["labels"] = labels
	}
	if fields != nil {
		out["fields"] = fields
	}
	out["_ref"] = ref
	return out
}

// RowValue returns an API field's value from a storage row, for exports.
func RowValue(row storage.Row, apiField string) string {
	switch apiField {
	case "timestamp":
		apiField = "_time"
	case "message":
		apiField = "_msg"
	}
	v, _ := row.Get(apiField)
	return v
}

// ShapeRow is exported for streaming handlers (tail, export).
func ShapeRow(row storage.Row, canViewRaw bool) LogRow { return shapeRow(row, canViewRaw) }

// ttlCache is a small bounded cache for dashboard aggregates.
type ttlCache struct {
	mu    sync.Mutex
	max   int
	items map[string]cacheItem
}

type cacheItem struct {
	value   any
	expires time.Time
}

func newTTLCache(maxItems int) *ttlCache {
	return &ttlCache{max: maxItems, items: map[string]cacheItem{}}
}

func (c *ttlCache) get(key string, now time.Time) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok || now.After(it.expires) {
		return nil, false
	}
	return it.value, true
}

func (c *ttlCache) set(key string, value any, ttl time.Duration, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= c.max {
		for k, it := range c.items {
			if now.After(it.expires) || len(c.items) >= c.max {
				delete(c.items, k)
			}
		}
	}
	c.items[key] = cacheItem{value: value, expires: now.Add(ttl)}
}
