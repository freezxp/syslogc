//go:build linux

package listener

import (
	"encoding/binary"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// controlReusePort sets SO_REUSEPORT so several sockets (and restarted
// listeners) can bind the same address.
func controlReusePort(_, _ string, c syscall.RawConn) error {
	var sockErr error
	err := c.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	})
	if err != nil {
		return err
	}
	return sockErr
}

// enableDropCounter asks the kernel to attach the socket's cumulative
// receive-queue overflow count (SO_RXQ_OVFL) to received datagrams.
func enableDropCounter(conn *net.UDPConn) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RXQ_OVFL, 1)
	})
}

// parseDropCounter extracts the SO_RXQ_OVFL value from control messages.
func parseDropCounter(oob []byte) (uint32, bool) {
	if len(oob) == 0 {
		return 0, false
	}
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return 0, false
	}
	for _, m := range msgs {
		if m.Header.Level == unix.SOL_SOCKET && m.Header.Type == unix.SO_RXQ_OVFL && len(m.Data) >= 4 {
			return binary.NativeEndian.Uint32(m.Data), true
		}
	}
	return 0, false
}

// readBufferSize returns the effective SO_RCVBUF. Linux reports double the
// usable size (bookkeeping overhead), so the value is halved.
func readBufferSize(conn *net.UDPConn) int {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0
	}
	size := 0
	_ = raw.Control(func(fd uintptr) {
		if v, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF); err == nil {
			size = v / 2
		}
	})
	return size
}
