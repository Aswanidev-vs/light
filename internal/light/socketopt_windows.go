//go:build windows

package light

import (
	"net"
	"syscall"
)

// tcpSocketBufferSize bounds the kernel buffers of transfer TCP sockets. This
// is deliberately small: the sender counts bytes as soon as the OS accepts
// them into the socket buffer, while the receiver only counts them after the
// disk write — every byte buffered in the kernel inflates the reported gap
// between the two devices (an 8 MiB send buffer times several parallel
// streams produced ~30 MB of apparent over-transfer on fast Wi-Fi). Wi-Fi
// RTT is single-digit milliseconds, so 1 MiB still covers the
// bandwidth-delay product at hundreds of MB/s.
const tcpSocketBufferSize = 1 << 20 // 1 MiB

// udpSocketBufferSize is kept large for QUIC only: dropped UDP datagrams
// throttle the whole QUIC connection, so its receive buffer stays generous.
const udpSocketBufferSize = 8 << 20 // 8 MiB

func transferSocketControl(network, address string, c syscall.RawConn) error {
	return c.Control(func(fd uintptr) {
		// Best effort: Windows may clamp these values to the system limit.
		// TCP_NODELAY is a no-op (ignored) on the QUIC/UDP socket.
		_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, tcpSocketBufferSize)
		_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, tcpSocketBufferSize)
		_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1)
	})
}

func quicSocketControl(network, address string, c syscall.RawConn) error {
	return c.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, udpSocketBufferSize)
		_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, udpSocketBufferSize)
	})
}

// socketListenConfig returns a net.ListenConfig that requests the (bounded)
// kernel socket buffer on created sockets. Best-effort: if the OS clamps the
// value, transfers still work, just at the smaller buffer.
func socketListenConfig() net.ListenConfig {
	return net.ListenConfig{
		Control: transferSocketControl,
	}
}
