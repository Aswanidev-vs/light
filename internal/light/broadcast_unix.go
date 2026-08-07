//go:build !windows

package light

import (
	"net"
	"syscall"
)

// setBroadcast enables SO_BROADCAST on a UDP socket (required to send to
// 255.255.255.255 / directed broadcast addresses). Must be set at SOL_SOCKET.
func setBroadcast(c *net.UDPConn) error {
	sc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	return sc.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	})
}
