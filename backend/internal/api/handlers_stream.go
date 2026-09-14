package api

import (
	"bufio"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/query"
)

const (
	tailFlushInterval = 250 * time.Millisecond
	tailMaxBatch      = 500
	tailHeartbeat     = 15 * time.Second
	tailStatsInterval = 10 * time.Second
	tailMaxSession    = time.Hour
	tailClientBuffer  = 5000
	exportFlushBytes  = 64 << 10
)

// handleTail streams matching logs as Server-Sent Events (ADR-0008).
func (s *Server) handleTail(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req query.TailRequest
	if q := r.URL.Query().Get("q"); q != "" {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(q, "="))
		if err != nil || len(raw) > maxJSONBody {
			return badRequest("bad_request", "q", "q must be base64url-encoded JSON")
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return badRequest("bad_request", "q", "q is not valid JSON: %s", jsonErrorMessage(err))
		}
	}
	offset := 5 * time.Second
	if v := r.URL.Query().Get("start_offset"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return badRequest("validation_failed", "start_offset", "start_offset must be a duration like 5s")
		}
		offset = d
	}
	rc := http.NewResponseController(w)
	ctx := r.Context()
	tail, err := s.opts.API.Query.OpenTail(ctx, p, req, offset)
	if err != nil {
		return err
	}
	readerStopped := make(chan struct{})
	defer func() {
		tail.Stop()
		<-readerStopped
		tail.Close()
	}()
	if req.Native != nil && strings.TrimSpace(req.Native.Text) != "" {
		s.audit(r, p, "logs.query_native", "success", map[string]any{"native": truncate(req.Native.Text, 2048), "tail": true})
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()
	_ = rc.SetWriteDeadline(time.Time{})

	rows := make(chan query.LogRow, tailClientBuffer)
	readerDone := make(chan error, 1)
	var dropped atomic.Int64
	go func() {
		defer close(readerStopped)
		for tail.Rows.Next() {
			row := query.ShapeRow(tail.Rows.Row(), tail.CanViewRaw)
			select {
			case rows <- row:
			default:
				dropped.Add(1) // client too slow: drop rather than buffer without bound
			}
		}
		readerDone <- tail.Rows.Err()
	}()

	bw := bufio.NewWriter(w)
	send := func(event string, v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if event != "" {
			fmt.Fprintf(bw, "event: %s\n", event)
		}
		fmt.Fprintf(bw, "data: %s\n\n", data)
		if err := bw.Flush(); err != nil {
			return err
		}
		return rc.Flush()
	}

	flush := time.NewTicker(tailFlushInterval)
	defer flush.Stop()
	heartbeat := time.NewTicker(tailHeartbeat)
	defer heartbeat.Stop()
	stats := time.NewTicker(tailStatsInterval)
	defer stats.Stop()
	deadline := time.NewTimer(tailMaxSession)
	defer deadline.Stop()

	var batch []query.LogRow
	var sent int64
	emit := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := send("logs", map[string]any{"rows": batch})
		sent += int64(len(batch))
		batch = batch[:0]
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			_ = emit()
			return nil
		case err := <-readerDone:
			_ = emit()
			if err != nil && ctx.Err() == nil {
				_ = send("error", toProblem(fmt.Errorf("%w: %w", query.ErrStorageUnavailable, err)))
			}
			return nil
		case row := <-rows:
			batch = append(batch, row)
			if len(batch) >= tailMaxBatch {
				if err := emit(); err != nil {
					return nil
				}
			}
		case <-flush.C:
			if err := emit(); err != nil {
				return nil
			}
		case <-heartbeat.C:
			if _, err := bw.WriteString(": heartbeat\n\n"); err != nil || bw.Flush() != nil || rc.Flush() != nil {
				return nil
			}
		case <-stats.C:
			if err := send("stats", map[string]int64{"rows_sent": sent, "dropped_client_slow": dropped.Load()}); err != nil {
				return nil
			}
		}
	}
}

// handleExport streams query results as CSV, NDJSON or JSON with constant memory.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	format := r.URL.Query().Get("format")
	var contentType, ext string
	switch format {
	case "csv":
		contentType, ext = "text/csv; charset=utf-8", "csv"
	case "ndjson":
		contentType, ext = "application/x-ndjson", "ndjson"
	case "json":
		contentType, ext = "application/json", "json"
	default:
		return badRequest("validation_failed", "format", "format must be csv, ndjson or json")
	}

	var req query.ExportRequest
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "application/x-www-form-urlencoded" {
		if err := r.ParseForm(); err != nil {
			return badRequest("bad_request", "", "invalid form body")
		}
		if err := json.Unmarshal([]byte(r.PostForm.Get("request")), &req); err != nil {
			return badRequest("bad_request", "request", "request must be the JSON export request")
		}
	} else if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}

	exp, err := s.opts.API.Query.OpenExport(r.Context(), p, req)
	if err != nil {
		return err
	}
	defer exp.Close()
	start := time.Now()
	filename := fmt.Sprintf("syslogc-export-%s.%s", start.UTC().Format("20060102T150405Z"), ext)
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	h.Set("X-Export-Limit", fmt.Sprint(exp.Limit))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	cw := &countingWriter{w: w}
	bw := bufio.NewWriterSize(cw, exportFlushBytes)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	var rows int64
	var writeErr error
	truncated := false

	switch format {
	case "csv":
		cwr := csv.NewWriter(bw)
		_ = cwr.Write(exp.Fields)
		record := make([]string, len(exp.Fields))
		for exp.Rows.Next() {
			if int(rows) == exp.Limit {
				truncated = true
				break
			}
			for i, f := range exp.Fields {
				record[i] = neutralizeCSV(query.RowValue(exp.Rows.Row(), f))
			}
			if writeErr = cwr.Write(record); writeErr != nil {
				break
			}
			rows++
			if bw.Buffered() >= exportFlushBytes/2 {
				cwr.Flush()
				_ = rc.Flush()
			}
		}
		cwr.Flush()
	default:
		enc := json.NewEncoder(bw)
		enc.SetEscapeHTML(false)
		if format == "json" {
			_, _ = bw.WriteString("[\n")
		}
		for exp.Rows.Next() {
			if int(rows) == exp.Limit {
				truncated = true
				break
			}
			if format == "json" && rows > 0 {
				_, _ = bw.WriteString(",\n")
			}
			row := query.ShapeRow(exp.Rows.Row(), exp.CanViewRaw)
			delete(row, "_ref")
			if format == "json" {
				b, _ := json.Marshal(row)
				_, writeErr = bw.Write(b)
			} else {
				writeErr = enc.Encode(row)
			}
			if writeErr != nil {
				break
			}
			rows++
			if bw.Buffered() >= exportFlushBytes/2 {
				_ = bw.Flush()
				_ = rc.Flush()
			}
		}
		if truncated {
			marker := map[string]any{"_export": map[string]any{"truncated": true, "rows": rows, "limit": exp.Limit}}
			b, _ := json.Marshal(marker)
			if format == "json" {
				_, _ = bw.WriteString(",\n")
				_, _ = bw.Write(b)
			} else {
				_, _ = bw.Write(append(b, '\n'))
			}
		}
		if format == "json" {
			_, _ = bw.WriteString("\n]\n")
		}
	}
	_ = bw.Flush()
	streamErr := exp.Rows.Err()
	outcome := "success"
	if writeErr != nil || streamErr != nil {
		outcome = "failure"
	}
	s.audit(r, p, "logs.export", outcome, map[string]any{
		"format": format, "rows": rows, "bytes": cw.n, "limit": exp.Limit, "truncated": truncated,
		"from": exp.ResolvedRange.Start, "to": exp.ResolvedRange.End,
		"duration_ms": time.Since(start).Milliseconds(), "native": req.Native != nil,
	})
	if streamErr != nil && r.Context().Err() == nil {
		s.log.Warn("export stream failed", "error", streamErr)
	}
	return nil
}

// neutralizeCSV prevents spreadsheet formula injection (docs/security.md).
func neutralizeCSV(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	}
	return v
}

type countingWriter struct {
	w http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}
