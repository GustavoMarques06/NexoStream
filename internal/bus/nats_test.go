package bus

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// startNATS runs an embedded NATS server with JetStream for tests.
func startNATS(t *testing.T) string {
	t.Helper()
	opts := &server.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
	}
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats not ready")
	}
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
}

func TestNATSConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	url := startNATS(t)
	b, err := NewNATS(context.Background(), NATSConfig{
		URL: url, Name: "test", StreamName: "TEST_EVENTS", StreamSubjects: []string{"x.*.event"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	RunConformance(t, b)
}
