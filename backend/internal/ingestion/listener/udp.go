package listener

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/freezxp/syslogc/backend/internal/ingestion/pipeline"
	"github.com/freezxp/syslogc/backend/internal/ingestion/source"
)

const maxDatagram = 65535

// UDP receives syslog datagrams on one or more SO_REUSEPORT sockets.
type UDP struct {
	src  *source.Settings
	sink Sink
	log  *slog.Logger

	conns    []*net.UDPConn
	wg       sync.WaitGroup
	stopping atomic.Bool
}

var _ Listener = (*UDP)(nil)

// NewUDP creates a UDP listener for src.
func NewUDP(src *source.Settings, sink Sink, log *slog.Logger) *UDP {
	return &UDP{src: src, sink: sink, log: log.With("source", src.Name, "protocol", "udp")}
}

func (l *UDP) Start() error {
	n := l.src.Config.UDP.Sockets
	if n <= 0 {
		n = runtime.GOMAXPROCS(0)
	}
	lc := net.ListenConfig{Control: controlReusePort}
	addr := l.src.Config.Address
	for i := range n {
		pc, err := lc.ListenPacket(context.Background(), "udp", addr)
		if err != nil {
			for _, c := range l.conns {
				_ = c.Close()
			}
			l.conns = nil
			return err
		}
		conn := pc.(*net.UDPConn)
		if i == 0 {
			// With port 0 every socket would get its own port; bind the rest
			// to the port the first socket received.
			addr = conn.LocalAddr().String()
		}
		want := l.src.Config.UDP.ReadBufferBytes.Int()
		if err := conn.SetReadBuffer(want); err != nil {
			l.log.Warn("cannot set UDP receive buffer", "error", err)
		}
		if got := readBufferSize(conn); i == 0 && got > 0 && got < want {
			l.log.Warn("kernel capped UDP receive buffer; raise net.core.rmem_max to reduce drops under bursts",
				"requested_bytes", want, "effective_bytes", got)
		}
		enableDropCounter(conn)
		l.conns = append(l.conns, conn)
	}
	for _, c := range l.conns {
		l.wg.Add(1)
		go l.readLoop(c)
	}
	l.log.Info("listener started", "address", l.conns[0].LocalAddr().String(), "sockets", n)
	return nil
}

func (l *UDP) Addr() net.Addr {
	if len(l.conns) == 0 {
		return nil
	}
	return l.conns[0].LocalAddr()
}

func (l *UDP) readLoop(conn *net.UDPConn) {
	defer l.wg.Done()
	m := l.src.Metrics
	buf := make([]byte, maxDatagram)
	oob := make([]byte, 128)
	var lastDrops uint32
	max := l.src.MaxMessageBytes

	for {
		n, oobn, _, addr, err := conn.ReadMsgUDPAddrPort(buf, oob)
		if err != nil {
			if l.stopping.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			l.log.Warn("UDP read failed", "error", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if drops, ok := parseDropCounter(oob[:oobn]); ok && drops != lastDrops {
			m.UDPKernelDrops.Add(float64(drops - lastDrops))
			lastDrops = drops
		}

		peer := netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
		if !l.src.Allowed(peer.Addr()) {
			m.DroppedDenied.Inc()
			continue
		}
		data := trimTrailing(buf[:n])
		if len(data) == 0 {
			continue
		}
		truncated := false
		if len(data) > max {
			data, truncated = data[:max], true
		}
		m.Received.Inc()
		m.BytesReceived.Add(float64(len(data)))
		l.sink.TryEnqueue(pipeline.RawMessage{
			Data:       string(data),
			ReceivedAt: time.Now(),
			Peer:       peer,
			Truncated:  truncated,
			Source:     l.src,
		})
	}
}

// Stop closes the sockets. Datagrams already read are handed to the sink.
func (l *UDP) Stop(ctx context.Context) error {
	if l.stopping.Swap(true) {
		return nil
	}
	for _, c := range l.conns {
		_ = c.Close()
	}
	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	l.log.Info("listener stopped")
	return nil
}

func trimTrailing(b []byte) []byte {
	for len(b) > 0 {
		switch b[len(b)-1] {
		case '\n', '\r', 0:
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b
}
