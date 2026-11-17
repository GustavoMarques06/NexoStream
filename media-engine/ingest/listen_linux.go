//go:build linux

package ingest

import (
	"context"
	"fmt"
	"net"
	"syscall"
)

// listenSocket binds the source address. For multicast on Linux the socket
// is bound to the group address itself, so two channels using different
// groups on the same port do not receive each other's traffic.
func listenSocket(a SourceAddr) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		})
		if err != nil {
			return err
		}
		return serr
	}}
	host := a.Host
	return lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf("%s:%d", host, a.Port))
}
