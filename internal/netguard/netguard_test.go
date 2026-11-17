package netguard_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nexostream/internal/netguard"
)

func TestBlocked(t *testing.T) {
	for ip, want := range map[string]bool{
		"169.254.169.254": true, "0.0.0.0": true, "fe80::1": true, "224.0.0.1": true, "100.100.100.200": true,
		"10.0.0.5": false, "192.168.1.10": false, "127.0.0.1": false, "8.8.8.8": false,
	} {
		if got := netguard.Blocked(net.ParseIP(ip)); got != want {
			t.Errorf("Blocked(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestClientAllowsLoopbackAndRefusesMetadata(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer ts.Close()
	c := netguard.Client(2 * time.Second)
	resp, err := c.Get(ts.URL)
	if err != nil {
		t.Fatalf("loopback should be allowed: %v", err)
	}
	resp.Body.Close()
	if _, err := c.Get("http://169.254.169.254/latest/meta-data/"); err == nil {
		t.Fatal("metadata address must be refused")
	}
}
