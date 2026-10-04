// Package listener implements network receivers for syslog over UDP, TCP
// and TLS. Listeners frame bytes into messages and hand them to a Sink.
package listener

import (
	"context"
	"net"
	"time"

	"github.com/freezxp/syslogc/backend/internal/ingestion/pipeline"
)

// Sink receives framed messages; *pipeline.Pipeline implements it.
type Sink interface {
	// TryEnqueue must not block; it accounts for drops itself.
	TryEnqueue(msg pipeline.RawMessage) bool
	// Enqueue blocks until the message is queued or ctx is done.
	Enqueue(ctx context.Context, msg pipeline.RawMessage) error
}

// Listener is a running network receiver.
// Diagnoser is implemented by listeners that can say why connections are
// being turned away. That is not a failure of the listener — it is bound and
// running — but it is the reason nothing is arriving, which is the question
// somebody is actually asking.
type Diagnoser interface {
	// Problem returns the most recent reason, when it last happened, and how
	// many times. The reason is empty when there is nothing wrong.
	Problem() (string, time.Time, int)
}

type Listener interface {
	// Start binds the socket and begins serving.
	Start() error
	// Addr returns the bound address (valid after Start).
	Addr() net.Addr
	// Stop stops accepting data and waits for in-flight messages to be
	// handed to the sink, or until ctx is done.
	Stop(ctx context.Context) error
}
