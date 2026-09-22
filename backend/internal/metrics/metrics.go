// Package metrics defines Syslogc's Prometheus metrics. Label values are
// bounded: source names come from configuration, everything else is an enum.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/freezxp/syslogc/backend/internal/logentry"
)

const namespace = "syslogc"

// Drop reasons for syslogc_ingest_messages_dropped_total.
const (
	DropQueueFull = "queue_full"
	DropDenied    = "denied"
	DropRejected  = "rejected"
	DropShutdown  = "shutdown"
)

// Connection rejection reasons.
const (
	RejectLimit        = "limit"
	RejectTLSHandshake = "tls_handshake"
	RejectDenied       = "denied"
)

// Batch flush reasons.
const (
	FlushRows     = "rows"
	FlushBytes    = "bytes"
	FlushWait     = "wait"
	FlushShutdown = "shutdown"
)

// Metrics holds all application metrics.
type Metrics struct {
	Registry *prometheus.Registry

	received            *prometheus.CounterVec
	bytesReceived       *prometheus.CounterVec
	parsed              *prometheus.CounterVec
	parseErrors         *prometheus.CounterVec
	stored              *prometheus.CounterVec
	dropped             *prometheus.CounterVec
	activeConnections   *prometheus.GaugeVec
	connectionsRejected *prometheus.CounterVec
	udpKernelDrops      *prometheus.CounterVec

	BytesStored         *prometheus.CounterVec
	BatchRows           prometheus.Histogram
	BatchBytes          prometheus.Histogram
	BatchFlushReason    *prometheus.CounterVec
	E2ELatency          prometheus.Histogram
	NormalizationLimits *prometheus.CounterVec
	ParsePanics         *prometheus.CounterVec

	StorageWriteDuration *prometheus.HistogramVec
	StorageWriteErrors   *prometheus.CounterVec
	StorageWriteRetries  *prometheus.CounterVec
	extracted            *prometheus.CounterVec
	ForwardSent          *prometheus.CounterVec
	ForwardDropped       *prometheus.CounterVec
	ForwardErrors        *prometheus.CounterVec
	StorageHealthy       *prometheus.GaugeVec
	StorageReachable     *prometheus.GaugeVec

	HTTPRequests *prometheus.CounterVec
	HTTPDuration *prometheus.HistogramVec
}

// New creates and registers all metrics on a fresh registry.
func New(version, commit string) *Metrics {
	reg := prometheus.NewRegistry()
	f := factory{reg: reg}
	m := &Metrics{
		Registry: reg,

		received: f.counterVec("ingest_messages_received_total",
			"Messages framed by listeners (after CIDR checks).", "source", "protocol"),
		bytesReceived: f.counterVec("ingest_bytes_received_total",
			"Bytes of framed messages.", "source", "protocol"),
		parsed: f.counterVec("ingest_messages_parsed_total",
			"Messages parsed successfully (including partial parses).", "source", "format"),
		parseErrors: f.counterVec("ingest_parse_errors_total",
			"Failed or partial parses, labelled with the attempted format.", "source", "format"),
		stored: f.counterVec("ingest_messages_stored_total",
			"Rows acknowledged by storage.", "source"),
		dropped: f.counterVec("ingest_messages_dropped_total",
			"Messages lost, by reason. Every loss path increments this counter.", "source", "reason"),
		activeConnections: f.gaugeVec("ingest_active_connections",
			"Open TCP/TLS connections.", "source"),
		connectionsRejected: f.counterVec("ingest_connections_rejected_total",
			"Connections closed without being served.", "source", "reason"),
		udpKernelDrops: f.counterVec("ingest_udp_kernel_drops_total",
			"Datagrams dropped by the kernel because the socket receive buffer was full.", "source"),

		BytesStored: f.counterVec("ingest_bytes_stored_total",
			"Estimated uncompressed bytes of rows acknowledged by storage.", "stage"),
		BatchRows: f.histogram("ingest_batch_rows", "Rows per flushed batch.",
			prometheus.ExponentialBuckets(1, 4, 9)),
		BatchBytes: f.histogram("ingest_batch_bytes", "Estimated bytes per flushed batch.",
			prometheus.ExponentialBuckets(1024, 4, 9)),
		BatchFlushReason: f.counterVec("ingest_batch_flush_reason_total",
			"Why batches were flushed.", "reason"),
		E2ELatency: f.histogram("ingest_e2e_latency_seconds",
			"Time from receipt of the oldest row in a batch to storage acknowledgement.",
			[]float64{.01, .05, .1, .25, .5, 1, 2, 5, 10, 30, 60}),
		NormalizationLimits: f.counterVec("ingest_normalization_limits_total",
			"Entries affected by normalization limits.", "limit"),
		ParsePanics: f.counterVec("ingest_parse_panics_total",
			"Recovered parser panics (bugs; must stay zero).", "format"),

		StorageWriteDuration: f.histogramVec("storage_write_duration_seconds",
			"Duration of storage write requests.", prometheus.DefBuckets, "backend"),
		StorageWriteErrors: f.counterVec("storage_write_errors_total",
			"Failed storage writes by error class.", "backend", "class"),
		StorageWriteRetries: f.counterVec("storage_write_retries_total",
			"Storage write retries.", "backend"),
		ForwardSent: f.counterVec("forward_messages_total",
			"Messages written to a forward target.", "target"),
		ForwardDropped: f.counterVec("forward_messages_dropped_total",
			"Messages not forwarded, by reason.", "target", "reason"),
		ForwardErrors: f.counterVec("forward_write_errors_total",
			"Failed writes to a forward target, by error class.", "target", "class"),
		extracted: f.counterVec("ingest_extract_total",
			"Messages by extract rule; rule=\"no_match\" means no rule applied.", "source", "rule"),
		StorageHealthy: f.gaugeVec("storage_healthy",
			"1 if the most recent storage write succeeded. Stays 1 while a write is still in flight.", "backend"),
		StorageReachable: f.gaugeVec("storage_reachable",
			"1 if the storage backend answered its health endpoint on the last periodic check.", "backend"),

		HTTPRequests: f.counterVec("http_requests_total",
			"HTTP requests by route pattern, method and status code.", "route", "method", "code"),
		HTTPDuration: f.histogramVec("http_request_duration_seconds",
			"HTTP request duration by route pattern and method.", prometheus.DefBuckets, "route", "method"),
	}

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace:   namespace,
			Name:        "build_info",
			Help:        "Build information.",
			ConstLabels: prometheus.Labels{"version": version, "commit": commit},
		}, func() float64 { return 1 }),
	)
	return m
}

// Source returns pre-resolved metric handles for one source, so the hot path
// never performs label lookups.
func (m *Metrics) Source(name string, protocol logentry.Protocol) *SourceMetrics {
	proto := protocol.String()
	sm := &SourceMetrics{
		Received:          m.received.WithLabelValues(name, proto),
		BytesReceived:     m.bytesReceived.WithLabelValues(name, proto),
		Stored:            m.stored.WithLabelValues(name),
		DroppedQueueFull:  m.dropped.WithLabelValues(name, DropQueueFull),
		DroppedDenied:     m.dropped.WithLabelValues(name, DropDenied),
		DroppedRejected:   m.dropped.WithLabelValues(name, DropRejected),
		DroppedShutdown:   m.dropped.WithLabelValues(name, DropShutdown),
		ActiveConnections: m.activeConnections.WithLabelValues(name),
		RejectedLimit:     m.connectionsRejected.WithLabelValues(name, RejectLimit),
		RejectedTLS:       m.connectionsRejected.WithLabelValues(name, RejectTLSHandshake),
		RejectedDenied:    m.connectionsRejected.WithLabelValues(name, RejectDenied),
		UDPKernelDrops:    m.udpKernelDrops.WithLabelValues(name),
		ExtractMisses:     m.extracted.WithLabelValues(name, "no_match"),
		extracted:         func(rule string) prometheus.Counter { return m.extracted.WithLabelValues(name, rule) },
	}
	for f := logentry.FormatUnknown; f <= logentry.FormatJSON; f++ {
		sm.parsed[f] = m.parsed.WithLabelValues(name, f.String())
		sm.parseErrors[f] = m.parseErrors.WithLabelValues(name, f.String())
	}
	return sm
}

// SourceMetrics are the per-source metric handles.
type SourceMetrics struct {
	// ExtractMisses counts messages no extract rule matched.
	ExtractMisses prometheus.Counter
	// extracted returns the counter for one matched rule.
	extracted         func(rule string) prometheus.Counter
	Received          prometheus.Counter
	BytesReceived     prometheus.Counter
	Stored            prometheus.Counter
	DroppedQueueFull  prometheus.Counter
	DroppedDenied     prometheus.Counter
	DroppedRejected   prometheus.Counter
	DroppedShutdown   prometheus.Counter
	ActiveConnections prometheus.Gauge
	RejectedLimit     prometheus.Counter
	RejectedTLS       prometheus.Counter
	RejectedDenied    prometheus.Counter
	UDPKernelDrops    prometheus.Counter

	parsed      [logentry.FormatJSON + 1]prometheus.Counter
	parseErrors [logentry.FormatJSON + 1]prometheus.Counter
}

// Parsed returns the parsed counter for format f.
func (s *SourceMetrics) Parsed(f logentry.Format) prometheus.Counter {
	if int(f) >= len(s.parsed) {
		f = logentry.FormatUnknown
	}
	return s.parsed[f]
}

// ParseErrors returns the parse error counter for format f.
func (s *SourceMetrics) ParseErrors(f logentry.Format) prometheus.Counter {
	if int(f) >= len(s.parseErrors) {
		f = logentry.FormatUnknown
	}
	return s.parseErrors[f]
}

type factory struct{ reg *prometheus.Registry }

func (f factory) counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	v := prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: name, Help: help}, labels)
	f.reg.MustRegister(v)
	return v
}

func (f factory) gaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	v := prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: namespace, Name: name, Help: help}, labels)
	f.reg.MustRegister(v)
	return v
}

func (f factory) histogram(name, help string, buckets []float64) prometheus.Histogram {
	v := prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: namespace, Name: name, Help: help, Buckets: buckets})
	f.reg.MustRegister(v)
	return v
}

func (f factory) histogramVec(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	v := prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: namespace, Name: name, Help: help, Buckets: buckets}, labels)
	f.reg.MustRegister(v)
	return v
}

// ForwardMetrics are one forward target's counters and gauges. Gauges are
// registered as functions so the forwarder owns their values.
type ForwardMetrics struct {
	m      *Metrics
	target string
	Sent   prometheus.Counter
}

// Forward returns the metrics for one forward target.
func (m *Metrics) Forward(target string) *ForwardMetrics {
	return &ForwardMetrics{m: m, target: target, Sent: m.ForwardSent.WithLabelValues(target)}
}

func (f *ForwardMetrics) Dropped(reason string) prometheus.Counter {
	return f.m.ForwardDropped.WithLabelValues(f.target, reason)
}

func (f *ForwardMetrics) Errors(class string) prometheus.Counter {
	return f.m.ForwardErrors.WithLabelValues(f.target, class)
}

// QueueMessages registers a gauge reporting queued messages.
func (f *ForwardMetrics) QueueMessages(value func() float64) {
	f.register("forward_queue_messages", "Messages waiting to be forwarded.", value)
}

// Healthy registers a gauge that is 1 while the last write succeeded.
func (f *ForwardMetrics) Healthy(value func() float64) {
	f.register("forward_healthy", "1 if the most recent write to the forward target succeeded.", value)
}

// LastSuccess registers a gauge holding the last successful write time.
func (f *ForwardMetrics) LastSuccess(value func() float64) {
	f.register("forward_last_success_timestamp_seconds", "Unix time of the last successful forward write.", value)
}

func (f *ForwardMetrics) register(name, help string, value func() float64) {
	f.m.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace, Name: name, Help: help, ConstLabels: prometheus.Labels{"target": f.target},
	}, value))
}

// Extracted returns the counter for messages matched by one extract rule.
func (s *SourceMetrics) Extracted(rule string) prometheus.Counter { return s.extracted(rule) }
