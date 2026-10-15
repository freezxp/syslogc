// Package hostinfo reports CPU, memory and disk utilisation for the machine or
// container Syslogc runs in.
//
// The distinction is the whole point of this package. Inside a container
// /proc/meminfo and /proc/stat still describe the host: a process capped at
// 2 GiB on a 64 GiB machine reads /proc/meminfo as 3% used while it is one
// allocation away from being OOM-killed. A monitoring tool that reports the
// host figure there is lying at exactly the moment the number matters. So
// every reading prefers the cgroup's own limit, falls back to /proc only when
// the cgroup says there is no limit, and carries a Source saying which of the
// two it measured.
//
// Every /proc and /sys path is joined onto a configurable root so tests can
// point at a fixture tree instead of the running kernel.
package hostinfo

import (
	"context"
	"sync"
	"time"
)

// Source says what a figure describes: the container's own limit, or the whole
// machine.
type Source string

const (
	// SourceCgroup means the figure came from a cgroup that sets a real
	// limit, so it describes this container.
	SourceCgroup Source = "cgroup"
	// SourceHost means no cgroup limit applied - either there is no cgroup,
	// or it says "max" - so the host figure is the one that constrains us.
	SourceHost Source = "host"
)

// CPU is processor utilisation over the last sampling interval.
type CPU struct {
	// Fraction is the share of the available cores used, 0..1. Measured is
	// false until two samples have been taken: utilisation is a rate, and no
	// single reading can give it.
	Fraction float64
	Measured bool
	// Cores is how many cores Fraction is measured against - the cgroup quota
	// when there is one, otherwise the host's CPU count. A fraction without
	// it cannot be turned back into CPU-seconds.
	Cores  float64
	Source Source
}

// Memory is memory usage against whatever actually limits it.
type Memory struct {
	UsedBytes  int64
	LimitBytes int64
	Source     Source
}

// Disk is one filesystem's usage.
type Disk struct {
	Path       string
	TotalBytes int64
	UsedBytes  int64
	// AvailBytes is what a non-root process may still write. It is smaller
	// than TotalBytes-UsedBytes by the reserve the filesystem keeps for root
	// (5% on ext4 by default), and it is the number that decides whether a
	// write succeeds, so it is the one reported rather than the free-to-root
	// figure.
	AvailBytes int64
}

// UsedFraction is how full the filesystem is, counting only the space a
// non-root writer could ever have had. This is what df's "Use%" column
// reports, and it reaches 100% while root can still write into the reserve.
func (d Disk) UsedFraction() float64 {
	capacity := d.UsedBytes + d.AvailBytes
	if capacity <= 0 {
		return 0
	}
	return float64(d.UsedBytes) / float64(capacity)
}

// Snapshot is one round of readings.
type Snapshot struct {
	CPU    CPU
	Memory Memory
	Disks  []Disk
	At     time.Time
}

// Reader samples utilisation. It is not safe for concurrent use: CPU
// utilisation is a delta against the previous call, so Sample must be called
// from one goroutine. Use Sampler to share readings with HTTP handlers.
type Reader struct {
	// root prefixes every /proc and /sys read. Production passes "/"; tests
	// pass a fixture directory.
	root string
	// diskPaths are passed to statfs as-is. They are real paths even in
	// tests, because statfs asks the kernel and no fixture tree can answer
	// for it - tests use t.TempDir instead.
	diskPaths []string

	prev    cpuSample
	hasPrev bool
}

// NewReader returns a Reader rooted at root, reporting disk usage for each of
// paths.
func NewReader(root string, paths []string) *Reader {
	if root == "" {
		root = "/"
	}
	return &Reader{root: root, diskPaths: paths}
}

// Sample reads CPU, memory and disk. The returned CPU has Measured false on
// the first call, because there is no earlier sample to difference against.
func (r *Reader) Sample() Snapshot {
	now := time.Now()
	s := Snapshot{At: now, Memory: r.memory(), Disks: r.disks()}

	cur, ok := r.cpuSample(now)
	if ok {
		s.CPU = CPU{Cores: cur.cores, Source: cur.source}
		if r.hasPrev && r.prev.source == cur.source {
			s.CPU.Fraction, s.CPU.Measured = cpuFraction(r.prev, cur)
		}
		r.prev, r.hasPrev = cur, true
	}
	return s
}

// disks reads every configured path, skipping any that cannot be read. A
// missing or unmounted path is not worth failing the whole sample over.
func (r *Reader) disks() []Disk {
	var out []Disk
	for _, p := range r.diskPaths {
		d, err := diskUsage(p)
		if err != nil {
			continue
		}
		out = append(out, d)
	}
	return out
}

// Sampler keeps the most recent Snapshot so a request handler can read it
// without waiting. CPU utilisation needs an interval to elapse, and no HTTP
// request should be held open for one.
type Sampler struct {
	r *Reader

	mu   sync.RWMutex
	last Snapshot
	ok   bool
}

// NewSampler wraps r.
func NewSampler(r *Reader) *Sampler { return &Sampler{r: r} }

// Sample takes a reading and stores it. Call it on a ticker.
func (s *Sampler) Sample() {
	snap := s.r.Sample()
	s.mu.Lock()
	s.last, s.ok = snap, true
	s.mu.Unlock()
}

// Snapshot returns the last reading, and false before the first Sample.
func (s *Sampler) Snapshot() (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last, s.ok
}

// Run samples every interval until ctx is cancelled. The first sample is taken
// immediately so memory and disk are available straight away; CPU needs the
// second one.
func (s *Sampler) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	s.Sample()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sample()
		}
	}
}
