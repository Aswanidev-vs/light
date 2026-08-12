//go:build windows

package light

import (
	"net"
	"syscall"
)

// listenSocketBufferSize is the receive/send buffer requested for transfer
// sockets. Fast Wi-Fi has a large bandwidth-delay product; the OS default
// (~64 KiB) can throttle throughput. Accepted TCP sockets inherit the listener
// buffer sizes, while the TCP dialer applies the same setting to its own
// sockets.
const listenSocketBufferSize = 8 << 20 // 8 MiB

func transferSocketControl(network, address string, c syscall.RawConn) error {
	return c.Control(func(fd uintptr) {
		// Best effort: Windows may clamp these values to the system limit.
		_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, listenSocketBufferSize)
		_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, listenSocketBufferSize)
	})
}

// socketListenConfig returns a net.ListenConfig that requests a large kernel
// socket buffer on created sockets. Best-effort: if the OS clamps the value,
// transfers still work, just at the smaller buffer.
func socketListenConfig() net.ListenConfig {
	return net.ListenConfig{
		Control: transferSocketControl,
	}
}
