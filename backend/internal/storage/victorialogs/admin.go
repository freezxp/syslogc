package victorialogs

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/syslogc/backend/internal/storage"
)

// defaultRetention is VictoriaLogs' -retentionPeriod default.
const defaultRetention = 7 * 24 * time.Hour

// Retention reads -retentionPeriod from the /flags endpoint, which lists
// only flags set explicitly; absent means the VictoriaLogs default.
func (b *Backend) Retention(ctx context.Context) (storage.RetentionInfo, error) {
	body, err := b.get(ctx, b.selectURL+"/flags")
	if err != nil {
		return storage.RetentionInfo{}, err
	}
	defer drain(body)
	return parseRetention(body)
}

func parseRetention(r io.Reader) (storage.RetentionInfo, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		v, ok := strings.CutPrefix(line, "-retentionPeriod=")
		if !ok {
			continue
		}
		d, err := parseVMDuration(strings.Trim(v, `"`))
		if err != nil {
			return storage.RetentionInfo{}, fmt.Errorf("victorialogs: parse retentionPeriod %q: %w", v, err)
		}
		return storage.RetentionInfo{Period: d, Explicit: true}, nil
	}
	if err := sc.Err(); err != nil {
		return storage.RetentionInfo{}, err
	}
	return storage.RetentionInfo{Period: defaultRetention}, nil
}

// parseVMDuration parses VictoriaMetrics-style durations: a number with an
// optional unit h, d, w, y (default unit: months per VictoriaMetrics, which
// we treat as 31 days).
func parseVMDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	unit := s[len(s)-1]
	num := s[:len(s)-1]
	mult := time.Duration(0)
	switch unit {
	case 'h':
		mult = time.Hour
	case 'd':
		mult = 24 * time.Hour
	case 'w':
		mult = 7 * 24 * time.Hour
	case 'y':
		mult = 365 * 24 * time.Hour
	default:
		num, mult = s, 31*24*time.Hour
	}
	n, err := strconv.ParseFloat(num, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return time.Duration(n * float64(mult)), nil
}

// Usage sums storage size metrics from VictoriaLogs' /metrics endpoint.
func (b *Backend) Usage(ctx context.Context) (storage.UsageInfo, error) {
	body, err := b.get(ctx, b.selectURL+"/metrics")
	if err != nil {
		return storage.UsageInfo{}, err
	}
	defer drain(body)
	return parseUsage(body)
}

func parseUsage(r io.Reader) (storage.UsageInfo, error) {
	var u storage.UsageInfo
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		name, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		metric, _, _ := strings.Cut(name, "{")
		var dst *int64
		switch metric {
		case "vl_compressed_data_size_bytes":
			dst = &u.CompressedBytes
		case "vl_uncompressed_data_size_bytes":
			dst = &u.UncompressedBytes
		case "vl_free_disk_space_bytes":
			dst = &u.FreeDiskBytes
		case "vl_total_disk_space_bytes":
			dst = &u.TotalDiskBytes
		case "vl_partitions":
			dst = &u.Partitions
		default:
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil {
			continue
		}
		*dst += int64(v)
	}
	return u, sc.Err()
}

func (b *Backend) get(ctx context.Context, u string) (io.ReadCloser, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	b.auth(req)
	resp, err := b.client.Do(req) //nolint:bodyclose // returned to the caller, which closes it
	if err != nil {
		cancel()
		return nil, fmt.Errorf("victorialogs: GET %s: %w", u, err)
	}
	if resp.StatusCode != http.StatusOK {
		drain(resp.Body)
		cancel()
		return nil, fmt.Errorf("victorialogs: GET %s: HTTP %d", u, resp.StatusCode)
	}
	return &cancelBody{ReadCloser: resp.Body, cancel: cancel}, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelBody) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
