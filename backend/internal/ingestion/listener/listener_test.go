package listener

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/ingestion/pipeline"
	"github.com/freezxp/syslogc/backend/internal/ingestion/source"
	"github.com/freezxp/syslogc/backend/internal/metrics"
)

type fakeSink struct {
	mu   sync.Mutex
	msgs []pipeline.RawMessage
	full bool
}

func (s *fakeSink) TryEnqueue(m pipeline.RawMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.full {
		m.Source.Metrics.DroppedQueueFull.Inc()
		return false
	}
	s.msgs = append(s.msgs, m)
	return true
}

func (s *fakeSink) Enqueue(_ context.Context, m pipeline.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, m)
	return nil
}

func (s *fakeSink) waitFor(t *testing.T, n int) []pipeline.RawMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		got := append([]pipeline.RawMessage(nil), s.msgs...)
		s.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("received %d messages, want %d", len(got), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func newSource(t *testing.T, mutate func(*config.Source)) *source.Settings {
	t.Helper()
	sc := config.Source{
		Name: "t", Type: "syslog", Protocol: "udp", Address: "127.0.0.1:0", Format: "auto",
		Timezone: "UTC", RawMessage: "always", Tenant: "default", HostnameFallback: "none",
		SDFlatten: "full", Framing: "auto", MaxConnections: 10, IdleTimeout: config.Duration(time.Minute),
		MaxMessageBytes: 1024, UDP: config.UDPConfig{Sockets: 2, ReadBufferBytes: 1 << 20},
	}
	if mutate != nil {
		mutate(&sc)
	}
	src, err := source.New(sc, metrics.New("test", "test"))
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func stop(t *testing.T, l Listener) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

func TestUDPListener(t *testing.T) {
	src := newSource(t, func(s *config.Source) { s.MaxMessageBytes = 256 })
	sink := &fakeSink{}
	l := NewUDP(src, sink, discard)
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	defer stop(t, l)

	conn, err := net.Dial("udp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	msgs := []string{"<14>Sep 14 10:00:00 h a: one\n", "<14>two\x00", strings.Repeat("x", 300), "\n"}
	for _, m := range msgs {
		if _, err := conn.Write([]byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	got := sink.waitFor(t, 3)
	byData := map[string]pipeline.RawMessage{}
	for _, m := range got {
		byData[m.Data] = m
	}
	if _, ok := byData["<14>Sep 14 10:00:00 h a: one"]; !ok {
		t.Errorf("trailing newline not trimmed: %v", got)
	}
	if _, ok := byData["<14>two"]; !ok {
		t.Errorf("trailing NUL not trimmed: %v", got)
	}
	long, ok := byData[strings.Repeat("x", 256)]
	if !ok || !long.Truncated {
		t.Errorf("oversize datagram not truncated to 256 bytes")
	}
	local := conn.LocalAddr().(*net.UDPAddr)
	if m := got[0]; m.Peer.Addr().String() != "127.0.0.1" || int(m.Peer.Port()) != local.Port || m.ReceivedAt.IsZero() {
		t.Errorf("peer/receive metadata wrong: %+v", m)
	}
	if v := testutil.ToFloat64(src.Metrics.Received); v != 3 {
		t.Errorf("received counter = %v, want 3 (empty datagram ignored)", v)
	}
}

func TestUDPListenerDeniesByCIDR(t *testing.T) {
	src := newSource(t, func(s *config.Source) { s.AllowedCIDRs = []string{"10.0.0.0/8"} })
	sink := &fakeSink{}
	l := NewUDP(src, sink, discard)
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	defer stop(t, l)
	conn, _ := net.Dial("udp", l.Addr().String())
	defer conn.Close()
	_, _ = conn.Write([]byte("<14>denied"))
	deadline := time.Now().Add(2 * time.Second)
	for testutil.ToFloat64(src.Metrics.DroppedDenied) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("denied datagram not counted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(sink.msgs) != 0 {
		t.Errorf("denied datagram delivered")
	}
}

func TestTCPListenerFraming(t *testing.T) {
	src := newSource(t, func(s *config.Source) { s.Protocol = "tcp" })
	sink := &fakeSink{}
	l := NewTCP(src, sink, discard, nil)
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	defer stop(t, l)

	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	octet := "<14>1 - - - - - -"
	payload := "<13>Sep 14 10:00:00 host a: lf framed\n" + fmt.Sprintf("%d %s", len(octet), octet) + "<13>final line\n"
	// Write in small pieces to exercise partial reads.
	for i := 0; i < len(payload); i += 7 {
		_, _ = conn.Write([]byte(payload[i:min(i+7, len(payload))]))
	}
	got := sink.waitFor(t, 3)
	want := []string{"<13>Sep 14 10:00:00 host a: lf framed", "<14>1 - - - - - -", "<13>final line"}
	for i, w := range want {
		if got[i].Data != w {
			t.Errorf("message %d = %q, want %q", i, got[i].Data, w)
		}
	}
	if v := testutil.ToFloat64(src.Metrics.ActiveConnections); v != 1 {
		t.Errorf("active connections = %v", v)
	}
	_ = conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for testutil.ToFloat64(src.Metrics.ActiveConnections) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("active connections gauge not decremented")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTCPListenerConnectionLimit(t *testing.T) {
	src := newSource(t, func(s *config.Source) { s.Protocol = "tcp"; s.MaxConnections = 1 })
	sink := &fakeSink{}
	l := NewTCP(src, sink, discard, nil)
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	defer stop(t, l)

	c1, _ := net.Dial("tcp", l.Addr().String())
	defer c1.Close()
	_, _ = c1.Write([]byte("first\n"))
	sink.waitFor(t, 1)

	c2, _ := net.Dial("tcp", l.Addr().String())
	defer c2.Close()
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c2.Read(make([]byte, 1)); err == nil {
		t.Error("second connection was served beyond the limit")
	}
	if v := testutil.ToFloat64(src.Metrics.RejectedLimit); v != 1 {
		t.Errorf("rejected (limit) = %v, want 1", v)
	}
}

func TestTCPListenerStopDrainsAndCloses(t *testing.T) {
	src := newSource(t, func(s *config.Source) { s.Protocol = "tcp" })
	sink := &fakeSink{}
	l := NewTCP(src, sink, discard, nil)
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	conn, _ := net.Dial("tcp", l.Addr().String())
	defer conn.Close()
	_, _ = conn.Write([]byte("before stop\n"))
	sink.waitFor(t, 1)

	start := time.Now()
	stop(t, l) // idle client: Stop must not wait for the full idle timeout
	if d := time.Since(start); d > drainGrace+time.Second {
		t.Errorf("Stop took %s", d)
	}
	if _, err := net.DialTimeout("tcp", l.Addr().String(), time.Second); err == nil {
		t.Error("listener still accepting after Stop")
	}
}

func TestTLSListener(t *testing.T) {
	certFile, keyFile := writeSelfSignedCert(t)
	src := newSource(t, func(s *config.Source) {
		s.Protocol = "tls"
		s.TLS = config.TLSConfig{CertFile: certFile, KeyFile: keyFile, MinVersion: "1.2", ClientAuth: "none"}
	})
	tlsCfg, err := NewTLSConfig(src.Config.TLS, discard)
	if err != nil {
		t.Fatal(err)
	}
	sink := &fakeSink{}
	l := NewTCP(src, sink, discard, tlsCfg)
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	defer stop(t, l)

	conn, err := tls.Dial("tcp", l.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	msg := "<14>1 2026-09-14T10:00:00Z h app - - - over tls"
	fmt.Fprintf(conn, "%d %s", len(msg), msg)
	got := sink.waitFor(t, 1)
	if got[0].Data != msg {
		t.Errorf("got %q", got[0].Data)
	}

	// A plaintext client fails the handshake and is counted.
	plain, _ := net.Dial("tcp", l.Addr().String())
	_, _ = plain.Write([]byte("<14>not tls\n"))
	_ = plain.Close()
	deadline := time.Now().Add(3 * time.Second)
	for testutil.ToFloat64(src.Metrics.RejectedTLS) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("failed handshake not counted")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func writeSelfSignedCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	return certFile, keyFile
}

// A sender configured for TLS against a plaintext listener used to have its
// handshake framed as a syslog message and stored as binary rubbish. It must
// be recognised and dropped instead, with nothing reaching the sink.
func TestTCPListenerRejectsTLSOnPlaintext(t *testing.T) {
	src := newSource(t, func(s *config.Source) { s.Protocol = "tcp" })
	sink := &fakeSink{}
	l := NewTCP(src, sink, discard, nil)
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	defer stop(t, l)

	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// A real client's first flight: handshake record, TLS 1.0 record version
	// (what every client sends for compatibility), then a ClientHello.
	if _, err := conn.Write([]byte{0x16, 0x03, 0x01, 0x00, 0x2c, 0x01, 0x00, 0x00, 0x28, 0x03, 0x03}); err != nil {
		t.Fatal(err)
	}

	// The listener closes rather than answering, which is what tells the
	// sender to stop waiting for a ServerHello.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("connection stayed open; the sender would wait for a ServerHello forever")
	}

	sink.mu.Lock()
	n := len(sink.msgs)
	sink.mu.Unlock()
	if n != 0 {
		t.Errorf("stored %d messages; a TLS handshake must not be stored as a log", n)
	}
	if v := testutil.ToFloat64(src.Metrics.RejectedTLS); v != 1 {
		t.Errorf("rejected_tls = %v, want 1", v)
	}
}

func TestLooksLikeTLSClientHello(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"tls 1.0 record version", []byte{0x16, 0x03, 0x01, 0x00}, true},
		{"tls 1.3 record version", []byte{0x16, 0x03, 0x04}, true},
		{"ssl 3.0", []byte{0x16, 0x03, 0x00}, true},
		{"rfc3164 message", []byte("<13>Sep 14 10:00:00 host a: hi"), false},
		{"rfc5424 message", []byte("<14>1 - - - - - -"), false},
		{"octet counted frame", []byte("17 <14>1 - - - -"), false},
		{"tls alert, not a handshake", []byte{0x15, 0x03, 0x01}, false},
		{"handshake byte but not tls", []byte{0x16, 0x20, 0x01}, false},
		{"too short", []byte{0x16, 0x03}, false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksLikeTLSClientHello(c.in); got != c.want {
				t.Errorf("looksLikeTLSClientHello(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
