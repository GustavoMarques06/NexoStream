// Package netguard builds HTTP clients for URLs chosen by operators (EPG
// guides, authorization backends). They may legitimately point at private
// networks, but never at link-local or unspecified addresses, which is where
// cloud metadata services live (169.254.169.254, fd00:ec2::254).
package netguard

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// Blocked reports whether a destination address must not be contacted.
func Blocked(ip net.IP) bool {
	return ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.Equal(net.ParseIP("100.100.100.200")) || ip.Equal(net.ParseIP("fd00:ec2::254"))
}

// control runs after DNS resolution, so a hostname that resolves (or is
// rebound) to a blocked address is refused too.
func control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip != nil && Blocked(ip) {
		return fmt.Errorf("destination %s is not allowed", host)
	}
	return nil
}

// Client returns an HTTP client that refuses blocked destinations, also
// across redirects, and follows at most 5 redirects.
func Client(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: control}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: d.DialContext, Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 10 * time.Second},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("stopped after 5 redirects")
			}
			return nil
		},
	}
}
