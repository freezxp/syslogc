package victorialogs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/klauspost/compress/gzip"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

// maxPooledBuffer bounds buffers returned to pools so one huge batch does
// not pin memory forever.
const maxPooledBuffer = 64 << 20

type byteBuf struct{ b []byte }

var encodePool = sync.Pool{New: func() any { return &byteBuf{b: make([]byte, 0, 1<<20)} }}

var gzipPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
	return w
}}

// WriteBatch encodes batch as JSON lines and posts it to /insert/jsonline.
func (b *Backend) WriteBatch(ctx context.Context, batch *logentry.Batch) error {
	if len(batch.Entries) == 0 {
		return nil
	}
	enc := encodePool.Get().(*byteBuf)
	defer putBuf(&encodePool, enc)
	enc.b = encodeBatch(enc.b[:0], batch)
	payload := enc.b

	if b.gzip {
		zbuf := encodePool.Get().(*byteBuf)
		defer putBuf(&encodePool, zbuf)
		compressed, err := gzipBytes(zbuf.b[:0], payload)
		if err != nil {
			return &storage.WriteError{Class: storage.Retryable, Err: err}
		}
		zbuf.b = compressed
		payload = compressed
	}

	q := url.Values{}
	q.Set("_stream_fields", strings.Join(b.cfg.StreamFields, ","))
	q.Set("_time_field", fieldTime)
	q.Set("_msg_field", fieldMsg)

	ctx, cancel := context.WithTimeout(ctx, b.cfg.WriteTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.insertURL+"/insert/jsonline?"+q.Encode(), bytes.NewReader(payload))
	if err != nil {
		return &storage.WriteError{Class: storage.Fatal, Err: err}
	}
	// VictoriaLogs silently discards jsonline bodies sent with a form content
	// type (it parses them as form values), so this header is mandatory.
	req.Header.Set("Content-Type", "application/stream+json")
	if b.gzip {
		req.Header.Set("Content-Encoding", "gzip")
	}
	if err := tenantHeaders(req, batch.Tenant); err != nil {
		return &storage.WriteError{Class: storage.Rejected, Err: err}
	}
	b.auth(req)

	resp, err := b.client.Do(req) //nolint:bodyclose // closed by drain
	if err != nil {
		return &storage.WriteError{Class: storage.Retryable, Err: err}
	}
	defer drain(resp.Body)
	if resp.StatusCode/100 == 2 {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return &storage.WriteError{
		Class:      classifyStatus(resp.StatusCode),
		StatusCode: resp.StatusCode,
		Err:        errors.New(strings.TrimSpace(string(msg))),
	}
}

// encodeBatch appends every entry of batch as a JSON line.
func encodeBatch(dst []byte, batch *logentry.Batch) []byte {
	for i := range batch.Entries {
		dst = appendEntry(dst, &batch.Entries[i])
	}
	return dst
}

func gzipBytes(dst, src []byte) ([]byte, error) {
	w := bytes.NewBuffer(dst)
	zw := gzipPool.Get().(*gzip.Writer)
	defer gzipPool.Put(zw)
	zw.Reset(w)
	if _, err := zw.Write(src); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

func putBuf(pool *sync.Pool, buf *byteBuf) {
	if cap(buf.b) <= maxPooledBuffer {
		buf.b = buf.b[:0]
		pool.Put(buf)
	}
}

func classifyStatus(code int) storage.ErrorClass {
	switch {
	case code == http.StatusTooManyRequests, code == http.StatusRequestTimeout, code >= 500:
		return storage.Retryable
	case code == http.StatusUnauthorized, code == http.StatusForbidden, code == http.StatusNotFound,
		code == http.StatusMethodNotAllowed:
		return storage.Fatal
	case code >= 400:
		return storage.Rejected
	}
	return storage.Retryable
}
