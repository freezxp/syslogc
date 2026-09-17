package victorialogs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

const dialectLogsQL = "logsql"

// maxLineBytes bounds a single result row; larger rows fail the query
// instead of growing memory without limit.
const maxLineBytes = 16 << 20

// Search runs a native LogsQL selection, newest first.
func (b *Backend) Search(ctx context.Context, q storage.SearchQuery) (storage.Rows, error) {
	if err := q.Range.Validate(); err != nil {
		return nil, err
	}
	filter := "*"
	if q.Native != nil {
		if q.Native.Dialect != dialectLogsQL {
			return nil, fmt.Errorf("%w: %q", storage.ErrUnsupportedDialect, q.Native.Dialect)
		}
		if t := strings.TrimSpace(q.Native.Text); t != "" {
			filter = t
		}
	}
	if q.Limit <= 0 {
		return nil, fmt.Errorf("search limit must be positive")
	}

	var sb strings.Builder
	sb.WriteString(filter)
	sb.WriteString(" | sort by (_time desc) limit ")
	sb.WriteString(strconv.Itoa(q.Limit))
	if len(q.Fields) > 0 {
		sb.WriteString(" | fields ")
		for i, f := range q.Fields {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(QuoteFieldName(f))
		}
	}
	return b.query(ctx, q.Tenant, sb.String(), q.Range)
}

func (b *Backend) query(ctx context.Context, tenant, logsql string, r storage.TimeRange) (storage.Rows, error) {
	form := url.Values{}
	form.Set("query", logsql)
	form.Set("start", r.Start.UTC().Format(time.RFC3339Nano))
	// VictoriaLogs' start/end args form the half-open interval [start, end)
	// at nanosecond precision (verified against v1.52), matching TimeRange.
	form.Set("end", r.End.UTC().Format(time.RFC3339Nano))
	if b.cfg.QueryTimeout > 0 {
		form.Set("timeout", b.cfg.QueryTimeout.String())
	}

	var cancel context.CancelFunc
	if b.cfg.QueryTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, b.cfg.QueryTimeout+5*time.Second)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.selectURL+"/select/logsql/query", strings.NewReader(form.Encode()))
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := tenantHeaders(req, tenant); err != nil {
		cancel()
		return nil, err
	}
	b.auth(req)

	resp, err := b.client.Do(req) //nolint:bodyclose // closed by drain or rows.Close
	if err != nil {
		cancel()
		return nil, fmt.Errorf("victorialogs query: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		drain(resp.Body)
		cancel()
		return nil, &storage.QueryError{
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(string(msg)),
			UserFacing: resp.StatusCode == http.StatusBadRequest,
		}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	return &rows{body: resp.Body, scanner: sc, cancel: cancel}, nil
}

// rows iterates over a JSON-lines query response.
type rows struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
	cancel  context.CancelFunc
	row     storage.Row
	err     error
	closed  bool
}

func (r *rows) Next() bool {
	if r.err != nil || r.closed {
		return false
	}
	for r.scanner.Scan() {
		line := r.scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		row, err := decodeRow(r.row[:0], line)
		if err != nil {
			r.err = err
			return false
		}
		r.row = row
		return true
	}
	r.err = r.scanner.Err()
	return false
}

func (r *rows) Row() storage.Row { return r.row }
func (r *rows) Err() error       { return r.err }

func (r *rows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	drain(r.body)
	r.cancel()
	return nil
}

// decodeRow decodes one flat JSON object, preserving field order.
func decodeRow(dst storage.Row, line []byte) (storage.Row, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("victorialogs: malformed result row")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("victorialogs: malformed result row: %w", err)
		}
		key, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("victorialogs: malformed result row: %w", err)
		}
		var value string
		if len(raw) > 0 && raw[0] == '"' {
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, fmt.Errorf("victorialogs: malformed result row: %w", err)
			}
		} else {
			value = string(raw)
		}
		dst = append(dst, logentry.Field{Key: key, Value: value})
	}
	return dst, nil
}

// QuoteFieldName quotes a field name for use in LogsQL.
func QuoteFieldName(name string) string {
	return strconv.Quote(name)
}
