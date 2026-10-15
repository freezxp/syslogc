package hostinfo

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// unlimited is how cgroup v2 spells "no limit". cgroup v1 instead writes a
// sentinel so large it cannot be real memory, which readLimit handles by
// comparing against the host's physical memory.
const unlimited = "max"

// cgroupDirs returns the directories that may hold this process's control
// files for one cgroup v1 controller ("memory", "cpu", "cpuacct") or, with an
// empty controller, for unified cgroup v2. Most specific first.
//
// Docker's default on cgroup v2 is a private cgroup namespace, which mounts
// the container's own cgroup at the root of /sys/fs/cgroup - the control files
// are simply there. With --cgroupns=host the whole host tree is visible
// instead and the process's own path from /proc/self/cgroup has to be
// appended. Trying the specific path first and the mount root second covers
// both without having to detect which one we are in.
func (r *Reader) cgroupDirs(controller string) []string {
	base := filepath.Join(r.root, "sys/fs/cgroup", controller)
	var dirs []string
	if rel := r.selfCgroup(controller); rel != "" && rel != "/" {
		dirs = append(dirs, filepath.Join(base, rel))
	}
	return append(dirs, base)
}

// selfCgroup returns this process's cgroup path for one controller, read from
// /proc/self/cgroup. The unified v2 line has an empty controller field
// ("0::/the/path"); v1 lines list the controllers they cover
// ("4:cpu,cpuacct:/the/path").
func (r *Reader) selfCgroup(controller string) string {
	b, err := os.ReadFile(filepath.Join(r.root, "proc/self/cgroup"))
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(b)) {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		if controller == "" {
			if parts[1] == "" {
				return parts[2]
			}
			continue
		}
		if slices.Contains(strings.Split(parts[1], ","), controller) {
			return parts[2]
		}
	}
	return ""
}

// readCgroupFile returns the contents of name from the first candidate
// directory that has it.
func (r *Reader) readCgroupFile(controller, name string) (string, bool) {
	for _, dir := range r.cgroupDirs(controller) {
		// The directory comes from this reader's own root and the name is a
		// fixed cgroup file name; neither is ever built from a request.
		b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // see above
		if err != nil {
			continue
		}
		return strings.TrimSpace(string(b)), true
	}
	return "", false
}

// cgroupMemory reads the container's memory limit and usage.
//
// It reports false when there is no cgroup, or when the cgroup sets no real
// limit. An unlimited cgroup tells us nothing the host figure does not, and
// the host is then what actually runs out, so the caller falls back to
// /proc/meminfo rather than reporting a limit of "all of it".
func (r *Reader) cgroupMemory() (Memory, bool) {
	limit, usage, stat, ok := r.cgroupMemoryFiles()
	if !ok {
		return Memory{}, false
	}
	if limit <= 0 || limit >= r.physicalMemory() {
		// Either "max", v1's enormous sentinel, or a limit above the
		// machine's own memory. In all three the host is the real ceiling.
		return Memory{}, false
	}
	return Memory{UsedBytes: usage - inactiveFile(stat), LimitBytes: limit, Source: SourceCgroup}, true
}

// cgroupMemoryFiles returns the limit, the raw usage and the memory.stat
// contents, from v2 if present and v1 otherwise.
func (r *Reader) cgroupMemoryFiles() (limit, usage int64, stat string, ok bool) {
	if raw, found := r.readCgroupFile("", "memory.max"); found {
		cur, _ := r.readCgroupFile("", "memory.current")
		stat, _ := r.readCgroupFile("", "memory.stat")
		return parseLimit(raw), parseInt(cur), stat, true
	}
	if raw, found := r.readCgroupFile("memory", "memory.limit_in_bytes"); found {
		cur, _ := r.readCgroupFile("memory", "memory.usage_in_bytes")
		stat, _ := r.readCgroupFile("memory", "memory.stat")
		return parseLimit(raw), parseInt(cur), stat, true
	}
	return 0, 0, "", false
}

// inactiveFile returns the reclaimable page cache counted in the cgroup's
// usage figure. It is subtracted from usage because that cache is evicted
// under pressure rather than causing an OOM kill, so counting it would show a
// container as near its limit when it is not. This is also what `docker stats`
// subtracts, which keeps the two agreeing.
func inactiveFile(stat string) int64 {
	for line := range strings.Lines(stat) {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		// v2 calls it inactive_file; v1 calls it total_inactive_file.
		if f[0] == "inactive_file" || f[0] == "total_inactive_file" {
			return parseInt(f[1])
		}
	}
	return 0
}

// cgroupCPU returns a sample measured against the cgroup's CPU quota, and
// false when there is no cgroup or no quota. Without a quota the container may
// use every core, so the host's own utilisation is the meaningful figure.
func (r *Reader) cgroupCPU(now time.Time) (cpuSample, bool) {
	if raw, ok := r.readCgroupFile("", "cpu.max"); ok {
		cores, limited := parseCPUMax(raw)
		if !limited {
			return cpuSample{}, false
		}
		stat, _ := r.readCgroupFile("", "cpu.stat")
		usec, found := statField(stat, "usage_usec")
		if !found {
			return cpuSample{}, false
		}
		return cpuSample{at: now, usage: time.Duration(usec) * time.Microsecond, cores: cores, source: SourceCgroup}, true
	}

	quota, qok := r.readCgroupFile("cpu", "cpu.cfs_quota_us")
	period, pok := r.readCgroupFile("cpu", "cpu.cfs_period_us")
	if !qok || !pok {
		return cpuSample{}, false
	}
	q, p := parseInt(quota), parseInt(period)
	// v1 writes -1 for "no quota".
	if q <= 0 || p <= 0 {
		return cpuSample{}, false
	}
	// cpuacct is a separate v1 controller, and it holds the usage counter.
	usage, uok := r.readCgroupFile("cpuacct", "cpuacct.usage")
	if !uok {
		return cpuSample{}, false
	}
	return cpuSample{
		at:     now,
		usage:  time.Duration(parseInt(usage)), // nanoseconds
		cores:  float64(q) / float64(p),
		source: SourceCgroup,
	}, true
}

// parseCPUMax reads a cgroup v2 cpu.max value, "quota period" in
// microseconds, where a quota of "max" means unlimited.
func parseCPUMax(raw string) (cores float64, limited bool) {
	f := strings.Fields(raw)
	if len(f) != 2 || f[0] == unlimited {
		return 0, false
	}
	q, p := parseInt(f[0]), parseInt(f[1])
	if q <= 0 || p <= 0 {
		return 0, false
	}
	return float64(q) / float64(p), true
}

// statField reads one "key value" line out of a cgroup stat file.
func statField(stat, key string) (int64, bool) {
	for line := range strings.Lines(stat) {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == key {
			return parseInt(f[1]), true
		}
	}
	return 0, false
}

// parseLimit reads a byte limit, returning 0 for "max".
func parseLimit(raw string) int64 {
	if raw == unlimited {
		return 0
	}
	return parseInt(raw)
}

func parseInt(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
