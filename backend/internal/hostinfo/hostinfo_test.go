package hostinfo

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// gib is a readable unit for the fixtures below.
const gib = 1 << 30

// meminfo64 is a host with 64 GiB total and 8 GiB still allocatable, so
// MemTotal-MemAvailable is 56 GiB used. Every memory fixture shares it, which
// is what makes the container cases legible: anything reporting 64 GiB as the
// limit has fallen through to the host by mistake.
const meminfo64 = `MemTotal:       67108864 kB
MemFree:         2097152 kB
MemAvailable:    8388608 kB
Buffers:          524288 kB
Cached:          4194304 kB
`

// v2MemStat is a cgroup v2 memory.stat holding 512 MiB of reclaimable page
// cache, which must not count towards the limit.
const v2MemStat = `anon 1073741824
file 536870912
inactive_anon 0
inactive_file 536870912
active_file 0
`

// fixture writes a tree of files and returns its root. Keys are paths
// relative to the root, so a fixture reads like the /proc and /sys layout it
// stands in for.
func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestMemory(t *testing.T) {
	for _, tc := range []struct {
		name      string
		files     map[string]string
		wantUsed  int64
		wantLimit int64
		wantSrc   Source
	}{
		{
			// The case this package exists for. The cgroup caps us at 2 GiB
			// on a 64 GiB machine: reporting the host's 56/64 GiB would say
			// 87% when we are at 75% of what will actually kill us, and
			// reporting the host total as the limit would say 2% when we are
			// one allocation from an OOM kill.
			name: "container limit is reported, not the host total",
			files: map[string]string{
				"proc/meminfo":                     meminfo64,
				"sys/fs/cgroup/memory.max":         "2147483648\n",
				"sys/fs/cgroup/memory.current":     "1610612736\n",
				"sys/fs/cgroup/memory.stat":        v2MemStat,
				"sys/fs/cgroup/cgroup.controllers": "cpu memory\n",
			},
			// 1.5 GiB current less 512 MiB inactive_file.
			wantUsed:  1610612736 - 536870912,
			wantLimit: 2 * gib,
			wantSrc:   SourceCgroup,
		},
		{
			// "max" means the cgroup imposes nothing, so the host total is
			// the real ceiling and must be reported as such.
			name: "cgroup reporting max falls back to the host",
			files: map[string]string{
				"proc/meminfo":                 meminfo64,
				"sys/fs/cgroup/memory.max":     "max\n",
				"sys/fs/cgroup/memory.current": "1610612736\n",
				"sys/fs/cgroup/memory.stat":    v2MemStat,
			},
			wantUsed:  56 * gib,
			wantLimit: 64 * gib,
			wantSrc:   SourceHost,
		},
		{
			// A limit above the machine's own memory cannot bind first.
			name: "cgroup limit above physical memory is not a limit",
			files: map[string]string{
				"proc/meminfo":                 meminfo64,
				"sys/fs/cgroup/memory.max":     "137438953472\n", // 128 GiB
				"sys/fs/cgroup/memory.current": "1610612736\n",
			},
			wantUsed:  56 * gib,
			wantLimit: 64 * gib,
			wantSrc:   SourceHost,
		},
		{
			name: "cgroup v1 limit",
			files: map[string]string{
				"proc/meminfo": meminfo64,
				"sys/fs/cgroup/memory/memory.limit_in_bytes": "2147483648\n",
				"sys/fs/cgroup/memory/memory.usage_in_bytes": "1610612736\n",
				"sys/fs/cgroup/memory/memory.stat":           "total_inactive_file 536870912\n",
			},
			wantUsed:  1610612736 - 536870912,
			wantLimit: 2 * gib,
			wantSrc:   SourceCgroup,
		},
		{
			// v1 spells "unlimited" as a number too large to be real memory
			// rather than as "max".
			name: "cgroup v1 unlimited sentinel falls back to the host",
			files: map[string]string{
				"proc/meminfo": meminfo64,
				"sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712\n",
				"sys/fs/cgroup/memory/memory.usage_in_bytes": "1610612736\n",
			},
			wantUsed:  56 * gib,
			wantLimit: 64 * gib,
			wantSrc:   SourceHost,
		},
		{
			name:      "no cgroup at all",
			files:     map[string]string{"proc/meminfo": meminfo64},
			wantUsed:  56 * gib,
			wantLimit: 64 * gib,
			wantSrc:   SourceHost,
		},
		{
			// With --cgroupns=host the whole host tree is visible, so the
			// container's own files sit under the path /proc/self/cgroup
			// names rather than at the mount root.
			name: "host cgroup namespace resolves our own path",
			files: map[string]string{
				"proc/meminfo":     meminfo64,
				"proc/self/cgroup": "0::/system.slice/docker-abc.scope\n",
				"sys/fs/cgroup/system.slice/docker-abc.scope/memory.max":     "2147483648\n",
				"sys/fs/cgroup/system.slice/docker-abc.scope/memory.current": "1073741824\n",
			},
			wantUsed:  1 * gib,
			wantLimit: 2 * gib,
			wantSrc:   SourceCgroup,
		},
		{
			// Without MemAvailable, used must not come out as the whole
			// machine.
			name: "meminfo without MemAvailable",
			files: map[string]string{
				"proc/meminfo": "MemTotal:       67108864 kB\nMemFree:         2097152 kB\n" +
					"Buffers:          524288 kB\nCached:          4194304 kB\n",
			},
			wantUsed:  64*gib - (2*gib + 512*(1<<20) + 4*gib),
			wantLimit: 64 * gib,
			wantSrc:   SourceHost,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewReader(fixture(t, tc.files), nil).memory()
			if got.UsedBytes != tc.wantUsed {
				t.Errorf("used = %d, want %d", got.UsedBytes, tc.wantUsed)
			}
			if got.LimitBytes != tc.wantLimit {
				t.Errorf("limit = %d, want %d", got.LimitBytes, tc.wantLimit)
			}
			if got.Source != tc.wantSrc {
				t.Errorf("source = %q, want %q", got.Source, tc.wantSrc)
			}
		})
	}
}

// procStat builds a /proc/stat with two cores and the given aggregate
// counters, in the field order the kernel uses:
// user nice system idle iowait irq softirq steal.
func procStat(user, idle, iowait int64) string {
	return "cpu  " + itoa(user) + " 0 0 " + itoa(idle) + " " + itoa(iowait) + " 0 0 0\n" +
		"cpu0 0 0 0 0 0 0 0 0\ncpu1 0 0 0 0 0 0 0 0\n" +
		"ctxt 123456\n"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestCPUSampleSource(t *testing.T) {
	for _, tc := range []struct {
		name      string
		files     map[string]string
		wantCores float64
		wantUsage time.Duration
		wantSrc   Source
	}{
		{
			name: "cgroup v2 quota",
			files: map[string]string{
				"proc/stat":              procStat(100, 100, 0),
				"sys/fs/cgroup/cpu.max":  "150000 100000\n",
				"sys/fs/cgroup/cpu.stat": "usage_usec 2500000\nuser_usec 2000000\n",
			},
			wantCores: 1.5,
			wantUsage: 2500 * time.Millisecond,
			wantSrc:   SourceCgroup,
		},
		{
			// An unlimited cgroup gives no denominator, so the host's own
			// utilisation is the only meaningful figure.
			name: "cgroup v2 cpu.max of max falls back to the host",
			files: map[string]string{
				"proc/stat":              procStat(100, 300, 0),
				"sys/fs/cgroup/cpu.max":  "max 100000\n",
				"sys/fs/cgroup/cpu.stat": "usage_usec 2500000\n",
			},
			wantCores: 2,
			wantUsage: jiffies(100),
			wantSrc:   SourceHost,
		},
		{
			name: "cgroup v1 quota",
			files: map[string]string{
				"proc/stat":                           procStat(100, 100, 0),
				"sys/fs/cgroup/cpu/cpu.cfs_quota_us":  "50000\n",
				"sys/fs/cgroup/cpu/cpu.cfs_period_us": "100000\n",
				"sys/fs/cgroup/cpuacct/cpuacct.usage": "3000000000\n",
			},
			wantCores: 0.5,
			wantUsage: 3 * time.Second,
			wantSrc:   SourceCgroup,
		},
		{
			name: "cgroup v1 without a quota falls back to the host",
			files: map[string]string{
				"proc/stat":                           procStat(100, 300, 0),
				"sys/fs/cgroup/cpu/cpu.cfs_quota_us":  "-1\n",
				"sys/fs/cgroup/cpu/cpu.cfs_period_us": "100000\n",
			},
			wantCores: 2,
			wantUsage: jiffies(100),
			wantSrc:   SourceHost,
		},
		{
			name:      "host only",
			files:     map[string]string{"proc/stat": procStat(50, 150, 0)},
			wantCores: 2,
			wantUsage: jiffies(50),
			wantSrc:   SourceHost,
		},
		{
			// iowait is time with nothing to run, so it must not land in the
			// busy figure.
			name:      "iowait does not count as busy",
			files:     map[string]string{"proc/stat": procStat(50, 100, 50)},
			wantCores: 2,
			wantUsage: jiffies(50),
			wantSrc:   SourceHost,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NewReader(fixture(t, tc.files), nil).cpuSample(time.Now())
			if !ok {
				t.Fatal("cpuSample reported no reading")
			}
			if got.source != tc.wantSrc {
				t.Errorf("source = %q, want %q", got.source, tc.wantSrc)
			}
			if got.cores != tc.wantCores {
				t.Errorf("cores = %v, want %v", got.cores, tc.wantCores)
			}
			if got.usage != tc.wantUsage {
				t.Errorf("usage = %v, want %v", got.usage, tc.wantUsage)
			}
		})
	}
}

func TestCPUSampleWithoutProcStat(t *testing.T) {
	if _, ok := NewReader(fixture(t, nil), nil).cpuSample(time.Now()); ok {
		t.Error("cpuSample reported a reading with nothing to read")
	}
}

func TestCPUFraction(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	for _, tc := range []struct {
		name     string
		prev     cpuSample
		cur      cpuSample
		want     float64
		wantMeas bool
	}{
		{
			// Host utilisation is busy/total, which already divides by the
			// core count.
			name:     "host half busy",
			prev:     cpuSample{at: base, usage: 0, total: 0, source: SourceHost},
			cur:      cpuSample{at: base.Add(time.Second), usage: time.Second, total: 2 * time.Second, source: SourceHost},
			want:     0.5,
			wantMeas: true,
		},
		{
			// One second of CPU time in one second of wall time against a
			// two-core quota is half the quota.
			name:     "cgroup one core of a two core quota",
			prev:     cpuSample{at: base, usage: 0, cores: 2, source: SourceCgroup},
			cur:      cpuSample{at: base.Add(time.Second), usage: time.Second, cores: 2, source: SourceCgroup},
			want:     0.5,
			wantMeas: true,
		},
		{
			name:     "cgroup saturating its quota",
			prev:     cpuSample{at: base, usage: 0, cores: 1.5, source: SourceCgroup},
			cur:      cpuSample{at: base.Add(2 * time.Second), usage: 3 * time.Second, cores: 1.5, source: SourceCgroup},
			want:     1,
			wantMeas: true,
		},
		{
			// Sampling skew can show marginally more CPU time than wall time
			// allows; the true value is the bound, not something above it.
			name:     "overshoot is clamped to one",
			prev:     cpuSample{at: base, usage: 0, cores: 1, source: SourceCgroup},
			cur:      cpuSample{at: base.Add(time.Second), usage: 1100 * time.Millisecond, cores: 1, source: SourceCgroup},
			want:     1,
			wantMeas: true,
		},
		{
			name: "counter going backwards is not a reading",
			prev: cpuSample{at: base, usage: 5 * time.Second, cores: 1, source: SourceCgroup},
			cur:  cpuSample{at: base.Add(time.Second), usage: time.Second, cores: 1, source: SourceCgroup},
		},
		{
			name: "no elapsed wall time is not a reading",
			prev: cpuSample{at: base, usage: 0, cores: 1, source: SourceCgroup},
			cur:  cpuSample{at: base, usage: time.Second, cores: 1, source: SourceCgroup},
		},
		{
			name: "host with no elapsed total is not a reading",
			prev: cpuSample{at: base, total: time.Second, source: SourceHost},
			cur:  cpuSample{at: base.Add(time.Second), total: time.Second, source: SourceHost},
		},
		{
			name: "cgroup without cores is not a reading",
			prev: cpuSample{at: base, source: SourceCgroup},
			cur:  cpuSample{at: base.Add(time.Second), usage: time.Second, source: SourceCgroup},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := cpuFraction(tc.prev, tc.cur)
			if ok != tc.wantMeas {
				t.Fatalf("measured = %v, want %v", ok, tc.wantMeas)
			}
			if ok && got != tc.want {
				t.Errorf("fraction = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSampleNeedsTwoReadings(t *testing.T) {
	root := fixture(t, map[string]string{
		"proc/meminfo": meminfo64,
		"proc/stat":    procStat(100, 100, 0),
	})
	r := NewReader(root, nil)

	first := r.Sample()
	if first.CPU.Measured {
		t.Error("the first sample reported a utilisation it cannot know")
	}
	// Memory and disk are instantaneous, so they are available immediately.
	if first.Memory.LimitBytes != 64*gib {
		t.Errorf("limit = %d, want %d", first.Memory.LimitBytes, 64*gib)
	}

	// Advance the counters by 100 busy and 100 idle jiffies: half busy.
	stat := filepath.Join(root, "proc/stat")
	if err := os.WriteFile(stat, []byte(procStat(200, 200, 0)), 0o644); err != nil {
		t.Fatal(err)
	}
	second := r.Sample()
	if !second.CPU.Measured {
		t.Fatal("the second sample reported no utilisation")
	}
	if second.CPU.Fraction != 0.5 {
		t.Errorf("fraction = %v, want 0.5", second.CPU.Fraction)
	}
	if second.CPU.Source != SourceHost {
		t.Errorf("source = %q, want %q", second.CPU.Source, SourceHost)
	}
}

// TestSampleDiscardsSourceChange covers a cgroup limit appearing or going away
// between samples. The two counters measure different things, so differencing
// across the change would produce a meaningless number.
func TestSampleDiscardsSourceChange(t *testing.T) {
	root := fixture(t, map[string]string{
		"proc/meminfo":           meminfo64,
		"proc/stat":              procStat(100, 100, 0),
		"sys/fs/cgroup/cpu.max":  "100000 100000\n",
		"sys/fs/cgroup/cpu.stat": "usage_usec 1000000\n",
	})
	r := NewReader(root, nil)
	r.Sample()
	if err := os.WriteFile(filepath.Join(root, "sys/fs/cgroup/cpu.max"), []byte("max 100000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := r.Sample(); s.CPU.Measured {
		t.Error("differenced a cgroup sample against a host sample")
	}
}

func TestDiskUsage(t *testing.T) {
	dir := t.TempDir()
	d, err := diskUsage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Path != dir {
		t.Errorf("path = %q, want %q", d.Path, dir)
	}
	if d.TotalBytes <= 0 {
		t.Fatalf("total = %d, want a positive size", d.TotalBytes)
	}
	if d.UsedBytes < 0 || d.UsedBytes > d.TotalBytes {
		t.Errorf("used = %d, outside 0..%d", d.UsedBytes, d.TotalBytes)
	}
	// Free-to-user can never exceed free-to-root, which is what is left after
	// the used figure.
	if d.AvailBytes > d.TotalBytes-d.UsedBytes {
		t.Errorf("avail = %d exceeds free-to-root %d", d.AvailBytes, d.TotalBytes-d.UsedBytes)
	}
	if f := d.UsedFraction(); f < 0 || f > 1 {
		t.Errorf("used fraction = %v, outside 0..1", f)
	}
}

func TestDiskUsageMissingPathIsSkipped(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-mounted")
	if _, err := diskUsage(missing); err == nil {
		t.Fatal("expected an error for a path that does not exist")
	}
	// Sample must still produce memory and CPU for the paths that do work.
	r := NewReader(fixture(t, map[string]string{"proc/meminfo": meminfo64}), []string{missing, t.TempDir()})
	if got := len(r.Sample().Disks); got != 1 {
		t.Errorf("disks = %d, want 1 (the unreadable path skipped)", got)
	}
}

func TestUsedFraction(t *testing.T) {
	for _, tc := range []struct {
		name string
		disk Disk
		want float64
	}{{
		// df's Use% divides by used+avail, not by the filesystem size, so the
		// reserve kept for root does not make a full disk look like 95%.
		name: "excludes the root reserve",
		disk: Disk{TotalBytes: 100, UsedBytes: 95, AvailBytes: 0},
		want: 1,
	}, {
		name: "half full",
		disk: Disk{TotalBytes: 100, UsedBytes: 45, AvailBytes: 45},
		want: 0.5,
	}, {
		name: "empty filesystem does not divide by zero",
		disk: Disk{},
		want: 0,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.disk.UsedFraction(); got != tc.want {
				t.Errorf("fraction = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSamplerSharesTheLastReading(t *testing.T) {
	r := NewReader(fixture(t, map[string]string{"proc/meminfo": meminfo64}), nil)
	s := NewSampler(r)
	if _, ok := s.Snapshot(); ok {
		t.Error("reported a snapshot before the first sample")
	}
	s.Sample()
	snap, ok := s.Snapshot()
	if !ok {
		t.Fatal("no snapshot after sampling")
	}
	if snap.Memory.LimitBytes != 64*gib {
		t.Errorf("limit = %d, want %d", snap.Memory.LimitBytes, 64*gib)
	}
}
