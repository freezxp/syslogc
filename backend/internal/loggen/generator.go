// Package loggen generates realistic synthetic syslog messages for tests,
// benchmarks and demos.
package loggen

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
)

// Format of generated messages.
type Format string

const (
	RFC5424 Format = "rfc5424"
	RFC3164 Format = "rfc3164"
)

// Options configures message content.
type Options struct {
	// Formats with relative weights, e.g. {rfc5424: 60, rfc3164: 40}.
	Formats map[Format]int
	Hosts   int
	Apps    int
	// SeverityWeights maps severity names to relative weights.
	SeverityWeights map[string]int
	// CustomFields is the number of extra key/value fields per message and
	// CustomFieldCardinality the number of distinct values per field.
	CustomFields           int
	CustomFieldCardinality int
	// MinMessageBytes pads messages with filler text up to this size.
	MinMessageBytes int
	RunID           string
	Seed            uint64
}

var severityCodes = map[string]int{
	"emergency": 0, "alert": 1, "critical": 2, "error": 3, "warning": 4, "notice": 5, "info": 6, "debug": 7,
}

// DefaultSeverityWeights approximates a typical production mix.
var DefaultSeverityWeights = map[string]int{"info": 70, "notice": 10, "warning": 12, "error": 6, "critical": 2}

type app struct {
	name      string
	facility  int
	templates []string
}

var appCatalog = []app{
	{"sshd", 4, []string{
		"Failed password for {user} from {ip} port {port} ssh2",
		"Accepted publickey for {user} from {ip} port {port} ssh2: ED25519 SHA256:{hex}",
		"Connection closed by authenticating user {user} {ip} port {port} [preauth]",
	}},
	{"nginx", 16, []string{
		`{ip} - - "GET /api/v1/items/{n} HTTP/1.1" {status} {bytes} "-" "curl/8.9.1"`,
		`{ip} - {user} "POST /login HTTP/2.0" {status} {bytes} "-" "Mozilla/5.0"`,
		"upstream timed out (110: Connection timed out) while reading response header from upstream, client: {ip}",
	}},
	{"kernel", 0, []string{
		"eth{small}: Link is {updown}",
		"TCP: request_sock_TCP: Possible SYN flooding on port {port}. Sending cookies.",
		"Out of memory: Killed process {n} (java) total-vm:{bytes}kB",
	}},
	{"vpnd", 20, []string{
		"VPN tunnel {vpn} {connected}: peer {ip}",
		"IKE negotiation failed for peer {ip}: no proposal chosen",
		"DPD timeout for tunnel {vpn}, peer {ip}",
	}},
	{"firewall", 20, []string{
		"action={action} src={ip} dst={ip} proto=tcp sport={port} dport={dport} policy_id={policy} bytes={bytes}",
		"action={action} src={ip} dst={ip} proto=udp dport=53 policy_id={policy}",
	}},
	{"CRON", 9, []string{
		"({user}) CMD (run-parts /etc/cron.hourly)",
		"pam_unix(cron:session): session opened for user {user} by (uid=0)",
	}},
	{"postgres", 16, []string{
		"duration: {n} ms  statement: SELECT * FROM orders WHERE customer_id = {n}",
		"FATAL:  password authentication failed for user \"{user}\"",
		"LOG:  checkpoint complete: wrote {n} buffers",
	}},
	{"dockerd", 3, []string{
		`level=info msg="Container {hex} health status changed to healthy"`,
		`level=error msg="Handler for POST /containers/{hex}/start returned error: port is already allocated"`,
	}},
}

var (
	users   = []string{"root", "admin", "alice", "bob", "deploy", "backup", "postgres", "www-data", "svc-monitor", "carol"}
	vpns    = []string{"HQ-VPN", "DC2-VPN", "BRANCH-LON", "BRANCH-NYC", "AWS-TGW"}
	actions = []string{"accept", "accept", "accept", "deny", "drop"}
	hostPfx = []string{"fw", "web", "db", "app", "sw", "rtr", "k8s-node", "nas", "mail", "vpn"}
)

// Generator produces messages. It is not safe for concurrent use; create
// one per goroutine with distinct seeds.
type Generator struct {
	opts      Options
	rng       *rand.Rand
	hosts     []string
	apps      []app
	formats   weighted[Format]
	severity  weighted[int]
	fieldKeys []string
	filler    string
}

type weighted[T any] struct {
	items   []T
	cumsum  []int
	totalWt int
}

func newWeighted[T comparable](m map[T]int, order []T) weighted[T] {
	var w weighted[T]
	for _, k := range order {
		if wt := m[k]; wt > 0 {
			w.totalWt += wt
			w.items = append(w.items, k)
			w.cumsum = append(w.cumsum, w.totalWt)
		}
	}
	return w
}

func (w weighted[T]) pick(r *rand.Rand) T {
	n := r.IntN(w.totalWt)
	for i, c := range w.cumsum {
		if n < c {
			return w.items[i]
		}
	}
	return w.items[len(w.items)-1]
}

// New validates opts and creates a generator. Worker distinguishes
// generators sharing a seed.
func New(opts Options, worker uint64) (*Generator, error) {
	if len(opts.Formats) == 0 {
		opts.Formats = map[Format]int{RFC5424: 1}
	}
	if opts.SeverityWeights == nil {
		opts.SeverityWeights = DefaultSeverityWeights
	}
	if opts.Hosts <= 0 {
		opts.Hosts = 50
	}
	if opts.Apps <= 0 || opts.Apps > len(appCatalog) {
		opts.Apps = len(appCatalog)
	}
	if opts.CustomFieldCardinality <= 0 {
		opts.CustomFieldCardinality = 100
	}
	g := &Generator{opts: opts, rng: rand.New(rand.NewPCG(opts.Seed, worker))}
	g.formats = newWeighted(opts.Formats, []Format{RFC5424, RFC3164})
	if g.formats.totalWt == 0 {
		return nil, fmt.Errorf("no valid formats in %v", opts.Formats)
	}
	sevs := map[int]int{}
	for name, wt := range opts.SeverityWeights {
		code, ok := severityCodes[name]
		if !ok {
			return nil, fmt.Errorf("unknown severity %q", name)
		}
		sevs[code] = wt
	}
	g.severity = newWeighted(sevs, []int{0, 1, 2, 3, 4, 5, 6, 7})
	if g.severity.totalWt == 0 {
		return nil, fmt.Errorf("severity weights must not all be zero")
	}
	// Hosts and apps are derived from a fixed seed so every worker shares them.
	hostRng := rand.New(rand.NewPCG(opts.Seed, 0xC0FFEE))
	for i := range opts.Hosts {
		g.hosts = append(g.hosts, fmt.Sprintf("%s%02d", hostPfx[hostRng.IntN(len(hostPfx))], i+1))
	}
	g.apps = appCatalog[:opts.Apps]
	for i := range opts.CustomFields {
		g.fieldKeys = append(g.fieldKeys, fmt.Sprintf("field_%d", i))
	}
	if opts.MinMessageBytes > 0 {
		g.filler = strings.Repeat("lorem ipsum dolor sit amet ", opts.MinMessageBytes/27+1)
	}
	return g, nil
}

// Append appends one message (without framing) to dst.
func (g *Generator) Append(dst []byte, seq uint64, now time.Time) []byte {
	a := g.apps[g.rng.IntN(len(g.apps))]
	host := g.hosts[g.rng.IntN(len(g.hosts))]
	pri := a.facility*8 + g.severity.pick(g.rng)
	pid := 100 + g.rng.IntN(30000)

	switch g.formats.pick(g.rng) {
	case RFC3164:
		dst = append(dst, '<')
		dst = strconv.AppendInt(dst, int64(pri), 10)
		dst = append(dst, '>')
		dst = now.UTC().AppendFormat(dst, time.Stamp)
		dst = append(dst, ' ')
		dst = append(dst, host...)
		dst = append(dst, ' ')
		dst = append(dst, a.name...)
		dst = append(dst, '[')
		dst = strconv.AppendInt(dst, int64(pid), 10)
		dst = append(dst, "]: "...)
		dst = g.appendBody(dst, a)
		for _, k := range g.fieldKeys {
			dst = append(dst, ' ')
			dst = append(dst, k...)
			dst = append(dst, "=v"...)
			dst = strconv.AppendInt(dst, int64(g.rng.IntN(g.opts.CustomFieldCardinality)), 10)
		}
		dst = g.appendMarkers(dst, seq)
	default:
		dst = append(dst, '<')
		dst = strconv.AppendInt(dst, int64(pri), 10)
		dst = append(dst, ">1 "...)
		dst = now.UTC().AppendFormat(dst, "2006-01-02T15:04:05.000000Z07:00")
		dst = append(dst, ' ')
		dst = append(dst, host...)
		dst = append(dst, ' ')
		dst = append(dst, a.name...)
		dst = append(dst, ' ')
		dst = strconv.AppendInt(dst, int64(pid), 10)
		dst = append(dst, " - "...)
		if len(g.fieldKeys) == 0 {
			dst = append(dst, '-')
		} else {
			dst = append(dst, "[fields@32473"...)
			for _, k := range g.fieldKeys {
				dst = append(dst, ' ')
				dst = append(dst, k...)
				dst = append(dst, `="v`...)
				dst = strconv.AppendInt(dst, int64(g.rng.IntN(g.opts.CustomFieldCardinality)), 10)
				dst = append(dst, '"')
			}
			dst = append(dst, ']')
		}
		dst = append(dst, ' ')
		dst = g.appendBody(dst, a)
		dst = g.appendMarkers(dst, seq)
	}
	return dst
}

// appendBody expands a random template of app a.
func (g *Generator) appendBody(dst []byte, a app) []byte {
	start := len(dst)
	tmpl := a.templates[g.rng.IntN(len(a.templates))]
	for {
		i := strings.IndexByte(tmpl, '{')
		if i < 0 {
			dst = append(dst, tmpl...)
			break
		}
		j := strings.IndexByte(tmpl[i:], '}')
		if j < 0 {
			dst = append(dst, tmpl...)
			break
		}
		dst = append(dst, tmpl[:i]...)
		dst = g.appendPlaceholder(dst, tmpl[i+1:i+j])
		tmpl = tmpl[i+j+1:]
	}
	if g.filler != "" && len(dst)-start < g.opts.MinMessageBytes {
		dst = append(dst, " | "...)
		dst = append(dst, g.filler[:g.opts.MinMessageBytes-(len(dst)-start)+3]...)
	}
	return dst
}

// appendMarkers appends run/sequence markers used to verify delivery by
// querying storage. They always end the message.
func (g *Generator) appendMarkers(dst []byte, seq uint64) []byte {
	if g.opts.RunID == "" {
		return dst
	}
	dst = append(dst, " run="...)
	dst = append(dst, g.opts.RunID...)
	dst = append(dst, " seq="...)
	return strconv.AppendUint(dst, seq, 10)
}

func (g *Generator) appendPlaceholder(dst []byte, name string) []byte {
	r := g.rng
	switch name {
	case "user":
		return append(dst, users[r.IntN(len(users))]...)
	case "ip":
		return fmt.Appendf(dst, "10.%d.%d.%d", r.IntN(4), r.IntN(256), 1+r.IntN(254))
	case "port":
		return strconv.AppendInt(dst, int64(1024+r.IntN(64000)), 10)
	case "dport":
		return strconv.AppendInt(dst, int64([]int{22, 53, 80, 443, 3389, 8080}[r.IntN(6)]), 10)
	case "status":
		return strconv.AppendInt(dst, int64([]int{200, 200, 200, 201, 301, 404, 500, 502}[r.IntN(8)]), 10)
	case "bytes":
		return strconv.AppendInt(dst, int64(r.IntN(100_000)), 10)
	case "n":
		return strconv.AppendInt(dst, int64(r.IntN(10_000)), 10)
	case "small":
		return strconv.AppendInt(dst, int64(r.IntN(4)), 10)
	case "policy":
		return strconv.AppendInt(dst, int64(1000+r.IntN(50)), 10)
	case "hex":
		return fmt.Appendf(dst, "%012x", r.Uint64()&0xffffffffffff)
	case "vpn":
		return append(dst, vpns[r.IntN(len(vpns))]...)
	case "action":
		return append(dst, actions[r.IntN(len(actions))]...)
	case "updown":
		return append(dst, []string{"Up", "Down"}[r.IntN(2)]...)
	case "connected":
		return append(dst, []string{"connected", "disconnected"}[r.IntN(2)]...)
	}
	return append(dst, name...)
}

// ParseWeights parses "a=60,b=40" into a map.
func ParseWeights(s string) (map[string]int, error) {
	out := map[string]int{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			out[k] = 1
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid weight %q", part)
		}
		out[strings.TrimSpace(k)] = n
	}
	return out, nil
}
