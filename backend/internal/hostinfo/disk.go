package hostinfo

import "syscall"

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
	unit := int64(st.Frsize)
	if unit <= 0 {
		unit = int64(st.Bsize)
	}
	total := int64(st.Blocks) * unit
	// Bfree is free to root; Bavail is free to everybody else. Used is
	// total-Bfree so it counts the root reserve as occupied, matching df.
	used := (int64(st.Blocks) - int64(st.Bfree)) * unit
	return Disk{
		Path:       path,
		TotalBytes: total,
		UsedBytes:  used,
		AvailBytes: int64(st.Bavail) * unit,
	}, nil
}
