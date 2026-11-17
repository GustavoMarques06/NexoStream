//go:build !linux

package ingest

import (
	"fmt"
	"net"
)

// listenSocket binds the source address. Outside Linux (development only)
// multicast sockets bind to the wildcard address.
func listenSocket(a SourceAddr) (net.PacketConn, error) {
	host := a.Host
	if a.Multicast {
		host = "0.0.0.0"
	}
	return net.ListenPacket("udp4", fmt.Sprintf("%s:%d", host, a.Port))
}
