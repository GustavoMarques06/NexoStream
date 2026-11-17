package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nexostream/internal/bus"
	"nexostream/internal/logging"
	"nexostream/media-engine/pipeline/pipelinetest"
)

// selfSigned writes a certificate for 127.0.0.1 and returns its files and the
// pool a client needs to trust it.
func selfSigned(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "worker-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certFile, keyFile, pool
}

func TestMediaListenerServesTLS(t *testing.T) {
	certFile, keyFile, pool := selfSigned(t)
	b := bus.NewMemory()
	defer b.Close()
	a, err := New(Config{WorkerID: "w1", HLSDir: t.TempDir(), StateDir: t.TempDir(), DVRDir: t.TempDir(), FFprobePath: "-", Encoders: []string{"cpu"},
		MediaAddr: "127.0.0.1:0", MetricsAddr: "127.0.0.1:0", TLSCertFile: certFile, TLSKeyFile: keyFile}, b, &pipelinetest.Engine{}, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	servers := a.startHTTP()
	defer func() {
		for _, s := range servers {
			_ = s.Close()
		}
	}()
	if len(servers) == 0 {
		t.Fatal("no server started")
	}
	addr := servers[0].Addr

	client := func(min, max uint16) *http.Client {
		return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: min, MaxVersion: max}}}
	}
	// Over TLS the node answers (an unknown channel is a 404, which proves the handshake and the routing).
	resp, err := client(tls.VersionTLS12, tls.VersionTLS13).Get("https://" + addr + "/hls/none/index.m3u8")
	if err != nil {
		t.Fatalf("https request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || resp.TLS == nil || resp.TLS.Version < tls.VersionTLS12 {
		t.Fatalf("status %d, tls %+v", resp.StatusCode, resp.TLS)
	}
	// Plain HTTP on the same port does not get an answer in clear.
	if r, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + addr + "/hls/none/index.m3u8"); err == nil {
		defer r.Body.Close()
		if r.StatusCode == http.StatusNotFound {
			t.Fatal("the TLS port answered a plain HTTP request normally")
		}
	}
	// Old protocol versions are refused.
	if r, err := client(tls.VersionTLS10, tls.VersionTLS11).Get("https://" + addr + "/hls/none/index.m3u8"); err == nil {
		r.Body.Close()
		t.Fatal("TLS 1.1 must be refused")
	}
}

func TestMediaListenerWithoutTLSStaysPlain(t *testing.T) {
	b := bus.NewMemory()
	defer b.Close()
	a, _ := New(Config{WorkerID: "w1", HLSDir: t.TempDir(), StateDir: t.TempDir(), DVRDir: t.TempDir(), FFprobePath: "-", Encoders: []string{"cpu"},
		MediaAddr: "127.0.0.1:0", MetricsAddr: "127.0.0.1:0"}, b, &pipelinetest.Engine{}, logging.Discard())
	servers := a.startHTTP()
	defer func() {
		for _, s := range servers {
			_ = s.Close()
		}
	}()
	resp, err := http.Get("http://" + servers[0].Addr + "/hls/none/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("plain HTTP: %d", resp.StatusCode)
	}
}
