//go:build windows

package light

import (
	"net"
	"syscall"
)

// listenSocketBufferSize is the receive/send buffer requested for the transfer
// listener. Fast Wi-Fi has a large bandwidth-delay product; the OS default
// (~64 KiB) throttles throughput. Accepted TCP sockets inherit the listener's
// buffer sizes.
const listenSocketBufferSize = 8 << 20 // 8 MiB

// socketListenConfig returns a net.ListenConfig that requests a large kernel
// socket buffer on created sockets. Best-effort: if the OS clamps the value,
// transfers still work, just at the smaller buffer.
func socketListenConfig() net.ListenConfig {
	return net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			_ = c.Control(func(fd uintptr) {
				_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, listenSocketBufferSize)
				_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, listenSocketBufferSize)
			})
			return nil
		},
	}
}
