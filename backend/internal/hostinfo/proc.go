package hostinfo

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cpuSample is one cumulative CPU reading. Utilisation is the difference
// between two of them, which is why Sample has to be called twice before it
// can report anything.
type cpuSample struct {
	at time.Time
	// usage is cumulative CPU time consumed.
	usage time.Duration
	// total is cumulative time across all CPUs, busy and idle together. Only
	// the host source fills it; it already accounts for the core count, so
	// host utilisation is usage/total and needs no division by cores.
	total  time.Duration
	cores  float64
	source Source
}

// memory returns usage against whatever actually limits it, preferring the
// cgroup's limit over the host's total.
func (r *Reader) memory() Memory {
	if m, ok := r.cgroupMemory(); ok {
		return m
	}
	return r.procMemory()
}

// procMemory reads the host's memory from /proc/meminfo.
//
// Used is MemTotal-MemAvailable rather than MemTotal-MemFree. MemAvailable is
// the kernel's own estimate of what can still be allocated without swapping,
// so the difference is the memory genuinely committed; MemFree would count
// reclaimable page cache as used and report a busy machine as nearly full.
// This also matches the "used" column of free(1).
func (r *Reader) procMemory() Memory {
	fields := r.meminfo()
	total := fields["MemTotal"]
	avail, ok := fields["MemAvailable"]
	if !ok {
		// MemAvailable predates every kernel we support, but fall back
		// rather than report a used figure equal to the whole machine.
		avail = fields["MemFree"] + fields["Cached"] + fields["Buffers"]
	}
	used := total - avail
	if used < 0 {
		used = 0
	}
	return Memory{UsedBytes: used, LimitBytes: total, Source: SourceHost}
}

// meminfo parses /proc/meminfo into bytes keyed by field name. The file
// reports kB, which is really KiB.
func (r *Reader) meminfo() map[string]int64 {
	out := map[string]int64{}
	b, err := os.ReadFile(filepath.Join(r.root, "proc/meminfo"))
	if err != nil {
		return out
	}
	for line := range strings.Lines(string(b)) {
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		v := parseInt(f[0])
		if len(f) > 1 && f[1] == "kB" {
			v *= 1024
		}
		out[name] = v
	}
	return out
}

// physicalMemory is the machine's MemTotal, used to recognise a cgroup limit
// that is not really a limit.
func (r *Reader) physicalMemory() int64 {
	if t := r.meminfo()["MemTotal"]; t > 0 {
		return t
	}
	// With no readable meminfo there is nothing to compare against, so take
	// any cgroup limit at face value.
	return 1 << 62
}

// cpuSample takes a CPU reading, preferring the cgroup's quota over the host.
func (r *Reader) cpuSample(now time.Time) (cpuSample, bool) {
	if s, ok := r.cgroupCPU(now); ok {
		return s, true
	}
	return r.procCPU(now)
}

// procCPU reads the host's aggregate CPU counters from /proc/stat.
func (r *Reader) procCPU(now time.Time) (cpuSample, bool) {
	b, err := os.ReadFile(filepath.Join(r.root, "proc/stat"))
	if err != nil {
		return cpuSample{}, false
	}
	var s cpuSample
	var found bool
	cores := 0
	for line := range strings.Lines(string(b)) {
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		if f[0] != "cpu" {
			// Per-core lines are counted only to report how many cores the
			// fraction covers; the aggregate line already sums them.
			cores++
			continue
		}
		// user nice system idle iowait irq softirq steal ...
		var total, idle time.Duration
		for i, v := range f[1:] {
			d := jiffies(parseInt(v))
			total += d
			// Idle and iowait are both time the CPU had nothing to run, so
			// neither counts as busy. steal does count: time the hypervisor
			// took is time our work did not get.
			if i == 3 || i == 4 {
				idle += d
			}
		}
		s.usage, s.total, found = total-idle, total, true
	}
	if !found {
		return cpuSample{}, false
	}
	s.at, s.source, s.cores = now, SourceHost, float64(cores)
	return s, true
}

// userHZ is the clock tick /proc/stat counts in. It is settable per kernel
// build but has been 100 on every Linux architecture Go supports for long
// enough that reading it through cgo is not worth it, and it cancels out of
// the busy/total ratio anyway.
const userHZ = 100

func jiffies(n int64) time.Duration { return time.Duration(n) * time.Second / userHZ }

// cpuFraction differences two samples. It reports false when the samples
// cannot be compared - no time elapsed, or a counter went backwards because
// the machine or container restarted underneath us.
func cpuFraction(prev, cur cpuSample) (float64, bool) {
	du := cur.usage - prev.usage
	if du < 0 {
		return 0, false
	}
	var frac float64
	switch cur.source {
	case SourceHost:
		dt := cur.total - prev.total
		if dt <= 0 {
			return 0, false
		}
		frac = float64(du) / float64(dt)
	default:
		// A cgroup reports CPU time consumed but no denominator, so the
		// denominator is wall time multiplied by the cores the quota allows.
		dw := cur.at.Sub(prev.at)
		if dw <= 0 || cur.cores <= 0 {
			return 0, false
		}
		frac = float64(du) / (float64(dw) * cur.cores)
	}
	// Sampling skew and jiffy rounding can put the ratio just outside 0..1.
	// Clamping is honest here: the real value is at the bound.
	return min(max(frac, 0), 1), true
}
