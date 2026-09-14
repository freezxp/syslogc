// Package listener implements network receivers for syslog over UDP, TCP
// and TLS. Listeners frame bytes into messages and hand them to a Sink.
package listener

import (
	"context"
	"net"

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
type Listener interface {
	// Start binds the socket and begins serving.
	Start() error
	// Addr returns the bound address (valid after Start).
	Addr() net.Addr
	// Stop stops accepting data and waits for in-flight messages to be
	// handed to the sink, or until ctx is done.
	Stop(ctx context.Context) error
}
