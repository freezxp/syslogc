//go:build !linux

package listener

import (
	"net"
	"syscall"
)

func controlReusePort(_, _ string, _ syscall.RawConn) error { return nil }

func enableDropCounter(*net.UDPConn) {}

func parseDropCounter([]byte) (uint32, bool) { return 0, false }

func readBufferSize(*net.UDPConn) int { return 0 }
