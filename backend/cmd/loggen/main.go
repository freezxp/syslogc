// Command loggen sends synthetic syslog traffic for testing and benchmarking.
//
//	loggen --target 127.0.0.1 --port 514 --protocol udp --rate 10000 --format rfc5424
//
// Every message ends with "run=<id> seq=<n>" so delivery can be verified by
// querying storage for the run ID.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/freezxp/syslogc/backend/internal/loggen"
)

type options struct {
	target          string
	port            int
	protocol        string
	format          string
	rate            int
	duration        time.Duration
	count           uint64
	connections     int
	framing         string
	hosts           int
	apps            int
	severityWeights string
	customFields    int
	fieldCard       int
	messageSize     int
	seed            uint64
	runID           string
	statsInterval   time.Duration
	jsonReport      string
	tlsInsecure     bool
}

type report struct {
	RunID          string  `json:"run_id"`
	Protocol       string  `json:"protocol"`
	Target         string  `json:"target"`
	TargetRate     int     `json:"target_rate"`
	Sent           uint64  `json:"sent"`
	Errors         uint64  `json:"errors"`
	Bytes          uint64  `json:"bytes"`
	DurationSec    float64 `json:"duration_seconds"`
	AchievedRate   float64 `json:"achieved_rate"`
	GeneratorLimit bool    `json:"generator_limited"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "loggen:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	var o options
	fs := pflag.NewFlagSet("loggen", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.target, "target", "127.0.0.1", "target host")
	fs.IntVar(&o.port, "port", 514, "target port")
	fs.StringVar(&o.protocol, "protocol", "udp", "udp | tcp | tls")
	fs.StringVar(&o.format, "format", "rfc5424", "rfc5424 | rfc3164 | mixed:rfc5424=60,rfc3164=40")
	fs.IntVar(&o.rate, "rate", 1000, "messages per second (0 = as fast as possible)")
	fs.DurationVar(&o.duration, "duration", 0, "how long to run (0 = until --count or interrupted)")
	fs.Uint64Var(&o.count, "count", 0, "total messages to send (0 = unlimited)")
	fs.IntVar(&o.connections, "connections", 1, "parallel senders (TCP/TLS connections or UDP sockets)")
	fs.StringVar(&o.framing, "framing", "octet_counting", "tcp framing: octet_counting | lf")
	fs.IntVar(&o.hosts, "hosts", 50, "number of distinct hostnames")
	fs.IntVar(&o.apps, "apps", 0, "number of distinct applications (0 = all templates)")
	fs.StringVar(&o.severityWeights, "severity-weights", "info=70,notice=10,warning=12,error=6,critical=2", "severity mix")
	fs.IntVar(&o.customFields, "custom-fields", 0, "extra key/value fields per message")
	fs.IntVar(&o.fieldCard, "custom-field-cardinality", 100, "distinct values per custom field")
	fs.IntVar(&o.messageSize, "message-size", 0, "minimum message body size in bytes (padding)")
	fs.Uint64Var(&o.seed, "seed", 0, "random seed (0 = random)")
	fs.StringVar(&o.runID, "run-id", "", "run identifier embedded in messages (default: random)")
	fs.DurationVar(&o.statsInterval, "stats-interval", 5*time.Second, "progress report interval (0 = off)")
	fs.StringVar(&o.jsonReport, "json-report", "", "write a JSON summary to this file")
	fs.BoolVar(&o.tlsInsecure, "tls-insecure", false, "skip TLS certificate verification")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.connections < 1 || o.rate < 0 {
		return errors.New("--connections must be >= 1 and --rate >= 0")
	}
	if o.protocol != "udp" && o.protocol != "tcp" && o.protocol != "tls" {
		return fmt.Errorf("unsupported protocol %q", o.protocol)
	}
	if o.seed == 0 {
		o.seed = rand.Uint64()
	}
	if o.runID == "" {
		o.runID = fmt.Sprintf("lg%08x", rand.Uint32())
	}

	formats := map[loggen.Format]int{}
	switch {
	case o.format == "rfc5424" || o.format == "rfc3164":
		formats[loggen.Format(o.format)] = 1
	case len(o.format) > 6 && o.format[:6] == "mixed:":
		w, err := loggen.ParseWeights(o.format[6:])
		if err != nil {
			return err
		}
		for k, v := range w {
			formats[loggen.Format(k)] = v
		}
	default:
		return fmt.Errorf("unsupported format %q", o.format)
	}
	sev, err := loggen.ParseWeights(o.severityWeights)
	if err != nil {
		return err
	}
	genOpts := loggen.Options{
		Formats: formats, Hosts: o.hosts, Apps: o.apps, SeverityWeights: sev,
		CustomFields: o.customFields, CustomFieldCardinality: o.fieldCard,
		MinMessageBytes: o.messageSize, RunID: o.runID, Seed: o.seed,
	}
	if _, err := loggen.New(genOpts, 0); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if o.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.duration)
		defer cancel()
	}

	addr := net.JoinHostPort(o.target, strconv.Itoa(o.port))
	fmt.Fprintf(stderr, "loggen: run_id=%s target=%s protocol=%s rate=%d connections=%d seed=%d\n",
		o.runID, addr, o.protocol, o.rate, o.connections, o.seed)

	var sent, errs, bytes atomic.Uint64
	var seq atomic.Uint64
	start := time.Now()
	var wg sync.WaitGroup
	firstErr := make(chan error, o.connections)
	for w := range o.connections {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sender(ctx, o, genOpts, uint64(w+1), addr, &seq, &sent, &errs, &bytes); err != nil {
				firstErr <- err
				stop()
			}
		}()
	}

	statsDone := make(chan struct{})
	if o.statsInterval > 0 {
		go func() {
			t := time.NewTicker(o.statsInterval)
			defer t.Stop()
			last, lastT := uint64(0), start
			for {
				select {
				case <-statsDone:
					return
				case now := <-t.C:
					s := sent.Load()
					fmt.Fprintf(stderr, "loggen: sent=%d rate=%.0f/s errors=%d\n", s, float64(s-last)/now.Sub(lastT).Seconds(), errs.Load())
					last, lastT = s, now
				}
			}
		}()
	}
	wg.Wait()
	close(statsDone)

	elapsed := time.Since(start).Seconds()
	r := report{
		RunID: o.runID, Protocol: o.protocol, Target: addr, TargetRate: o.rate,
		Sent: sent.Load(), Errors: errs.Load(), Bytes: bytes.Load(), DurationSec: elapsed,
		AchievedRate: float64(sent.Load()) / elapsed,
	}
	r.GeneratorLimit = o.rate > 0 && r.AchievedRate < float64(o.rate)*0.95 && o.count == 0
	out, _ := json.MarshalIndent(r, "", "  ")
	fmt.Fprintln(stdout, string(out))
	if r.GeneratorLimit {
		fmt.Fprintln(stderr, "loggen: WARNING achieved rate is below 95% of target; the generator (not the server) may be the bottleneck")
	}
	if o.jsonReport != "" {
		if err := os.WriteFile(o.jsonReport, append(out, '\n'), 0o644); err != nil {
			return err
		}
	}
	select {
	case err := <-firstErr:
		return err
	default:
		return nil
	}
}

func sender(ctx context.Context, o options, genOpts loggen.Options, worker uint64, addr string,
	seq, sent, errs, bytes *atomic.Uint64) error {
	gen, err := loggen.New(genOpts, worker)
	if err != nil {
		return err
	}
	conn, err := dial(o, addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	var w *bufio.Writer
	if o.protocol != "udp" {
		w = bufio.NewWriterSize(conn, 256<<10)
		defer w.Flush()
	}

	// Pace in 10 ms slots: send whatever is due, then flush.
	perWorker := float64(o.rate) / float64(o.connections)
	const slot = 10 * time.Millisecond
	ticker := time.NewTicker(slot)
	defer ticker.Stop()
	start := time.Now()
	var mine uint64
	buf := make([]byte, 0, 4096)
	frame := make([]byte, 0, 4096)

	for {
		due := uint64(1 << 62)
		if o.rate > 0 {
			due = uint64(time.Since(start).Seconds()*perWorker) + 1
		}
		for mine < due {
			n := seq.Add(1)
			if o.count > 0 && n > o.count {
				return nil
			}
			buf = gen.Append(buf[:0], n, time.Now())
			var werr error
			if w == nil {
				_, werr = conn.Write(buf)
			} else {
				frame = frame[:0]
				if o.framing == "lf" {
					frame = append(append(frame, buf...), '\n')
				} else {
					frame = strconv.AppendInt(frame, int64(len(buf)), 10)
					frame = append(append(frame, ' '), buf...)
				}
				_, werr = w.Write(frame)
			}
			if werr != nil {
				errs.Add(1)
				if w != nil {
					return fmt.Errorf("write: %w", werr)
				}
			} else {
				sent.Add(1)
				bytes.Add(uint64(len(buf)))
			}
			mine++
			if o.rate == 0 && mine%1024 == 0 && ctx.Err() != nil {
				return nil
			}
		}
		if w != nil {
			if err := w.Flush(); err != nil {
				return fmt.Errorf("flush: %w", err)
			}
		}
		if o.rate == 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func dial(o options, addr string) (net.Conn, error) {
	switch o.protocol {
	case "udp":
		return net.Dial("udp", addr)
	case "tls":
		return tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: o.tlsInsecure, MinVersion: tls.VersionTLS12})
	default:
		return net.Dial("tcp", addr)
	}
}
