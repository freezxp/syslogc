package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/storage"
)

const (
	devSearchDefaultLimit = 100
	devSearchMaxLimit     = 1000
	devSearchMaxRange     = 31 * 24 * time.Hour
)

// devSearch is an UNAUTHENTICATED development endpoint that runs a native
// LogsQL query. It exists so Phase 1 ingestion can be verified end to end
// and is replaced by POST /api/v1/logs/search in Phase 3.
//
//	GET /api/v1/dev/search?query=<logsql>&from=<rfc3339|duration>&to=<rfc3339>&limit=<n>&fields=a,b
//
// "from" also accepts a duration relative to now (e.g. "15m", "24h").
func (s *Server) devSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := time.Now()

	end := now
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid 'to': want RFC 3339"})
			return
		}
		end = t
	}
	start := end.Add(-time.Hour)
	if v := q.Get("from"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			start = end.Add(-d)
		} else if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			start = t
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid 'from': want RFC 3339 or duration"})
			return
		}
	}
	if end.Sub(start) > devSearchMaxRange {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "time range too large (max 31 days)"})
		return
	}

	limit := devSearchDefaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > devSearchMaxLimit {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and 1000"})
			return
		}
		limit = n
	}
	var fields []string
	if v := q.Get("fields"); v != "" {
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				fields = append(fields, f)
			}
		}
	}

	sq := storage.SearchQuery{
		Selection: storage.Selection{
			Tenant: "default",
			Range:  storage.TimeRange{Start: start, End: end},
			Native: &storage.NativeQuery{Dialect: "logsql", Text: q.Get("query")},
		},
		Fields: fields,
		Limit:  limit,
	}
	rows, err := s.opts.DevQuerier.Search(r.Context(), sq)
	if err != nil {
		var qe *storage.QueryError
		if errors.As(err, &qe) && qe.UserFacing {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": qe.Message})
			return
		}
		s.opts.Log.Warn("dev search failed", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "storage query failed"})
		return
	}
	defer func() { _ = rows.Close() }()

	// Stream rows as a JSON array of flat objects, preserving field order.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(`{"rows":[`))
	n := 0
	for rows.Next() {
		if n > 0 {
			_, _ = w.Write([]byte{','})
		}
		writeRow(w, rows.Row())
		n++
	}
	status := "ok"
	if err := rows.Err(); err != nil {
		s.opts.Log.Warn("dev search stream failed", "error", err)
		status = "incomplete"
	}
	tail, _ := json.Marshal(map[string]any{
		"returned": n,
		"status":   status,
		"range":    map[string]string{"start": start.UTC().Format(time.RFC3339Nano), "end": end.UTC().Format(time.RFC3339Nano)},
	})
	_, _ = w.Write([]byte(`],"meta":`))
	_, _ = w.Write(tail)
	_, _ = w.Write([]byte("}\n"))
}

func writeRow(w http.ResponseWriter, row storage.Row) {
	_, _ = w.Write([]byte{'{'})
	for i, f := range row {
		if i > 0 {
			_, _ = w.Write([]byte{','})
		}
		k, _ := json.Marshal(f.Key)
		v, _ := json.Marshal(f.Value)
		_, _ = w.Write(k)
		_, _ = w.Write([]byte{':'})
		_, _ = w.Write(v)
	}
	_, _ = w.Write([]byte{'}'})
}
