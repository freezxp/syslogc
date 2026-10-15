package hostinfo

import (
	"fmt"
	"math"
	"syscall"
)

// diskUsage reads one filesystem's usage with statfs.
//
// Unlike the /proc readings this asks the kernel about a real path, so it
// takes no account of the Reader's root: no fixture tree can answer a statfs.
// Tests point it at a temporary directory instead.
func diskUsage(path string) (Disk, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Disk{}, err
	}
	// f_frsize is the fragment size, the unit the block counts are in.
	// f_bsize is the preferred I/O size and is not always the same thing.
	unit := st.Frsize
	if unit <= 0 {
		unit = st.Bsize
	}
	total, ok := blockBytes(st.Blocks, unit)
	if !ok {
		return Disk{}, fmt.Errorf("%s reports %d blocks of %d bytes, which is not a size", path, st.Blocks, unit)
	}
	freeToRoot, _ := blockBytes(st.Bfree, unit)
	avail, _ := blockBytes(st.Bavail, unit)
	return Disk{
		Path:       path,
		TotalBytes: total,
		// Bfree is free to root; Bavail is free to everybody else. Used is
		// total minus Bfree, so it counts the root reserve as occupied, which
		// is what df does.
		UsedBytes:  total - freeToRoot,
		AvailBytes: avail,
	}, nil
}

// blockBytes multiplies a block count by the block size, reporting whether the
// result is a size at all.
//
// The counts are unsigned and the sizes are reported signed. Nothing is within
// several orders of magnitude of overflowing, but an unchecked multiplication
// would turn a nonsense reading into a negative disk size, which is worse than
// refusing to report one: a negative number still draws a bar.
func blockBytes(blocks uint64, unit int64) (int64, bool) {
	if unit <= 0 || blocks > uint64(math.MaxInt64)/uint64(unit) {
		return 0, false
	}
	return int64(blocks) * unit, true //nolint:gosec // bounded on the line above
}
