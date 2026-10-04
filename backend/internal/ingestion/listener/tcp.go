package listener

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/freezxp/syslogc/backend/internal/ingestion/framing"
	"github.com/freezxp/syslogc/backend/internal/ingestion/pipeline"
	"github.com/freezxp/syslogc/backend/internal/ingestion/source"
)

const (
	tlsHandshakeTimeout = 10 * time.Second
	// tlsRecordHeaderLen is how much of a TLS record is needed to recognise
	// one: content type, then the two version bytes.
	tlsRecordHeaderLen = 3
	// drainGrace is how long connections may keep delivering buffered data
	// after Stop begins.
	drainGrace = 2 * time.Second
)

// TCP receives syslog over TCP, or TLS when tlsConfig is non-nil.
type TCP struct {
	src       *source.Settings
	sink      Sink
	log       *slog.Logger
	tlsConfig *tls.Config
	framing   framing.Mode

	ln    net.Listener
	sem   chan struct{}
	ctx   context.Context
	abort context.CancelFunc

	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	wg       sync.WaitGroup
	stopping atomic.Bool
	// drainDeadline (unix nanos) caps read deadlines once stopping.
	drainDeadline atomic.Int64
}

var _ Listener = (*TCP)(nil)

// NewTCP creates a TCP (or TLS) listener for src.
func NewTCP(src *source.Settings, sink Sink, log *slog.Logger, tlsConfig *tls.Config) *TCP {
	mode, _ := framing.ParseMode(src.Config.Framing)
	maxConns := src.Config.MaxConnections
	if maxConns <= 0 {
		maxConns = 1 << 20
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &TCP{
		src:       src,
		sink:      sink,
		log:       log.With("source", src.Name, "protocol", src.Protocol.String()),
		tlsConfig: tlsConfig,
		framing:   mode,
		sem:       make(chan struct{}, maxConns),
		ctx:       ctx,
		abort:     cancel,
		conns:     make(map[net.Conn]struct{}),
	}
}

func (l *TCP) Start() error {
	// No SO_REUSEPORT for TCP: a second process binding the same port must
	// fail loudly instead of silently sharing connections.
	lc := net.ListenConfig{KeepAlive: 30 * time.Second}
	ln, err := lc.Listen(context.Background(), "tcp", l.src.Config.Address)
	if err != nil {
		return err
	}
	l.ln = ln
	l.wg.Add(1)
	go l.acceptLoop()
	l.log.Info("listener started", "address", ln.Addr().String())
	return nil
}

func (l *TCP) Addr() net.Addr {
	if l.ln == nil {
		return nil
	}
	return l.ln.Addr()
}

func (l *TCP) acceptLoop() {
	defer l.wg.Done()
	m := l.src.Metrics
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			if l.stopping.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			l.log.Warn("accept failed", "error", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		peer := addrPort(conn.RemoteAddr())
		if !l.src.Allowed(peer.Addr()) {
			m.RejectedDenied.Inc()
			_ = conn.Close()
			continue
		}
		select {
		case l.sem <- struct{}{}:
		default:
			m.RejectedLimit.Inc()
			_ = conn.Close()
			continue
		}
		l.mu.Lock()
		if l.stopping.Load() {
			l.mu.Unlock()
			<-l.sem
			_ = conn.Close()
			return
		}
		l.conns[conn] = struct{}{}
		l.wg.Add(1)
		l.mu.Unlock()
		go l.serve(conn)
	}
}

func (l *TCP) serve(conn net.Conn) {
	m := l.src.Metrics
	m.ActiveConnections.Inc()
	defer func() {
		_ = conn.Close()
		l.mu.Lock()
		delete(l.conns, conn)
		l.mu.Unlock()
		m.ActiveConnections.Dec()
		<-l.sem
		l.wg.Done()
	}()

	peer := addrPort(conn.RemoteAddr())
	if l.tlsConfig != nil {
		tc := tls.Server(conn, l.tlsConfig)
		ctx, cancel := context.WithTimeout(l.ctx, tlsHandshakeTimeout)
		err := tc.HandshakeContext(ctx)
		cancel()
		if err != nil {
			m.RejectedTLS.Inc()
			l.log.Debug("tls handshake failed", "peer", peer.String(), "error", err)
			return
		}
		conn = tc
	}

	idle := l.src.Config.IdleTimeout.D()

	var r io.Reader = conn
	if l.tlsConfig == nil {
		// A sender configured for TLS against a plaintext listener writes a
		// ClientHello and waits for a ServerHello that is never coming. Without
		// this check the handshake is framed as a syslog message and stored as
		// binary rubbish, the sender hangs, and nothing anywhere says why — so
		// say exactly what happened, and keep the bytes out of the logs.
		br := bufio.NewReader(conn)
		l.setReadDeadline(conn, idle)
		if head, err := br.Peek(tlsRecordHeaderLen); err == nil && looksLikeTLSClientHello(head) {
			m.RejectedTLS.Inc()
			l.log.Warn("a sender is speaking TLS to a plaintext listener; "+
				"give this source a certificate and set its protocol to tls, "+
				"or configure the sender to send without TLS",
				"peer", peer.String(), "address", l.src.Config.Address)
			return
		}
		r = br
	}

	dec := framing.NewDecoder(r, l.framing, l.src.MaxMessageBytes)
	for {
		l.setReadDeadline(conn, idle)
		msg, truncated, err := dec.Next()
		if len(msg) > 0 {
			rm := pipeline.RawMessage{
				Data:       string(msg),
				ReceivedAt: time.Now(),
				Peer:       peer,
				Truncated:  truncated,
				Source:     l.src,
			}
			m.Received.Inc()
			m.BytesReceived.Add(float64(len(msg)))
			if err := l.sink.Enqueue(l.ctx, rm); err != nil {
				m.DroppedShutdown.Inc()
				return
			}
		}
		if err != nil {
			if !isExpectedConnErr(err) && !l.stopping.Load() {
				l.log.Debug("connection closed with error", "peer", peer.String(), "error", err)
			}
			return
		}
	}
}

// looksLikeTLSClientHello reports whether b begins a TLS handshake record.
//
// A handshake record is content type 22, then the protocol version: 0x03
// followed by 0x00 (SSL 3.0) through 0x04 (TLS 1.3). Syslog is text, and 0x16
// is a control character no sender begins a line with, so there is no
// plausible message this rejects.
func looksLikeTLSClientHello(b []byte) bool {
	return len(b) >= tlsRecordHeaderLen && b[0] == 0x16 && b[1] == 0x03 && b[2] <= 0x04
}

func (l *TCP) setReadDeadline(conn net.Conn, idle time.Duration) {
	var deadline time.Time
	if idle > 0 {
		deadline = time.Now().Add(idle)
	}
	if drain := l.drainDeadline.Load(); drain != 0 {
		if d := time.Unix(0, drain); deadline.IsZero() || d.Before(deadline) {
			deadline = d
		}
	}
	_ = conn.SetReadDeadline(deadline)
}

// Stop closes the listening socket, lets open connections deliver data
// already sent for a short grace period, then closes them.
func (l *TCP) Stop(ctx context.Context) error {
	l.mu.Lock()
	if l.stopping.Swap(true) {
		l.mu.Unlock()
		return nil
	}
	deadline := time.Now().Add(drainGrace)
	l.drainDeadline.Store(deadline.UnixNano())
	if l.ln != nil {
		_ = l.ln.Close()
	}
	for c := range l.conns {
		_ = c.SetReadDeadline(deadline)
	}
	l.mu.Unlock()

	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		l.abort() // unblock Enqueue
		l.mu.Lock()
		for c := range l.conns {
			_ = c.Close()
		}
		l.mu.Unlock()
		<-done
	}
	l.abort()
	l.log.Info("listener stopped")
	return nil
}

func isExpectedConnErr(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded)
}
