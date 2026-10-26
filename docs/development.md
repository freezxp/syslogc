# Development

## Toolchain

| Tool | Version | Used for |
|---|---|---|
| Go | 1.27+ (`backend/go.mod`) | backend |
| golangci-lint | 2.13+ | lint, formatting, architecture boundaries |
| Docker + Compose v2 | 24+ | integration tests, image, e2e |
| util-linux `logger` | any | e2e and logger compatibility tests |
| Python 3 | any | e2e script JSON checks |
| Node.js | 24 | web UI (`frontend/`) |
| PostgreSQL | 17 (container) | metadata store tests (`TEST_POSTGRES_DSN`) |

## Layout

```text
backend/
├── cmd/
│   ├── syslogc/          # server binary: serve, config, healthcheck, version
│   └── loggen/           # synthetic traffic generator
├── internal/
│   ├── app/              # composition root: wiring, startup, graceful shutdown
│   ├── api/              # HTTP server: route table, auth middleware, handlers, embedded web UI
│   ├── auth/             # passwords, sessions, API keys, roles and permissions
│   ├── config/           # model, defaults, loading (YAML/env/flags), validation
│   ├── ingestion/
│   │   ├── framing/      # RFC 6587 stream framing
│   │   ├── listener/     # UDP, TCP, TLS receivers
│   │   ├── pipeline/     # bounded queue, parse workers, batching, writers
│   │   ├── source/       # resolved per-source runtime settings
│   │   └── supervisor/   # starts/stops configured sources
│   ├── logentry/         # normalized log model (dependency-free)
│   ├── metadata/         # metadata store interface
│   │   └── postgres/     # PostgreSQL implementation, migrations, pgtest helpers
│   ├── loggen/           # message generator used by cmd/loggen
│   ├── metrics/          # Prometheus metric definitions
│   ├── normalization/    # data-model rules applied after parsing
│   ├── parser/           # parser contract, detection, rfc5424, rfc3164, jsonlog
│   ├── query/            # time ranges, guards, pagination, aggregations, dashboard
│   └── storage/          # backend-neutral interfaces
│       ├── filter/       # filter AST and validation
│       └── victorialogs/ # VictoriaLogs adapter and AST → LogsQL compiler
└── tests/
    ├── integration/      # against real VictoriaLogs + PostgreSQL (build tag: integration)
    └── e2e/              # compose smoke test
frontend/                 # React web UI (see docs/frontend.md), embedded at build time
```

Architecture rules enforced by `golangci-lint` (depguard):

- Only `internal/app` (and tests) may import `internal/storage/victorialogs`; everything else uses `internal/storage` interfaces ([ADR-0002](decisions/0002-storage-abstraction.md)).
- `internal/logentry` imports only the standard library.

## Everyday commands

```bash
make build          # bin/syslogc, bin/loggen
make test           # go test -race ./... (starts PostgreSQL on 127.0.0.1:15432)
make web            # build the UI into backend/internal/api/webui/dist for embedding
make web-dev        # Vite dev server on :5173, proxying /api to :8080
make test-web       # frontend lint, type check, unit tests
make lint           # golangci-lint run
make fmt            # gofmt + goimports
make integration    # starts VictoriaLogs (19428) and PostgreSQL (15432), runs integration tests
make vl-down pg-down # stop them
make fuzz FUZZTIME=2m
make bench
make up && make e2e # compose stack + smoke test
```

Running the server against a local VictoriaLogs:

```bash
make vl-up
bin/syslogc serve --config deploy/compose/syslogc.yaml \
  --storage.victorialogs.insert_url=http://127.0.0.1:19428 \
  --storage.victorialogs.select_url=http://127.0.0.1:19428 \
  --metadata.postgres.dsn="postgres://syslogc:test@127.0.0.1:15432/syslogc?sslmode=disable" \
  --metadata.postgres.dsn_file= --auth.secret_key_file= --auth.cookie_secure=false \
  --log.format=text --shutdown.drain_delay=0s
```

The generated admin password is printed to stderr on first start.

## Testing

| Layer | Where | Notes |
|---|---|---|
| Unit | next to code (`*_test.go`) | table-driven; `go-cmp` for diffs |
| Fuzz | `Fuzz*` in parsers, framing, encoder | seed corpora run in `go test`; crashers are committed under `testdata/fuzz` as regression cases |
| Pipeline | `internal/ingestion/pipeline` | fake storage writer that blocks, fails, or rejects rows |
| Listener | `internal/ingestion/listener` | real sockets on 127.0.0.1, self-signed TLS certificate |
| API | `internal/api` | `httptest` server with fake storage and real PostgreSQL: every route × role, CSRF, pagination, export, SSE |
| Integration | `backend/tests/integration` | `TEST_VICTORIALOGS_URL` + `TEST_POSTGRES_DSN`; hostile-value round trips, UDP/TCP → storage, `logger` compatibility, the query API end to end (tie-group pagination, typed JSON fields, tail, export) |
| E2E | `backend/tests/e2e/smoke.sh` | the Definition-of-Done flow (login, search, fields, histogram, dashboard, tail, export, saved search, health, metrics) against `docker compose` |

Integration tests skip (not fail) when `TEST_VICTORIALOGS_URL` is unset, so
`go test ./...` works without Docker. CI always sets it.

## Synthetic DNS traffic, healthy and broken

`loggen` generates load; `scripts/dnsgen.py` generates something to look at.
It imitates BIND 9, dnsmasq and Unbound on two resolvers, so the collector
sees a realistic mix of formats, severities and fields rather than one shape
repeated.

```sh
./scripts/dnsgen.py --list                    # the scenarios
./scripts/dnsgen.py --rate 50                 # healthy traffic
./scripts/dnsgen.py --incident upstream-down --for 10m
./scripts/dnsgen.py --cycle clear=20m,upstream-down=5m,clear=20m,servfail-storm=8m
```

The incidents are the point. A dashboard, an alert or a saved search can be
watched changing and then recovering, which a generator that only ever
produces healthy traffic cannot prove anything about.

| scenario | what it looks like |
|---|---|
| `clear` | healthy, with the background of NXDOMAIN, RPZ hits and the odd SERVFAIL a real resolver always has |
| `upstream-down` | timeouts then SERVFAIL for anything not cached — about 44% of messages at warning or worse |
| `servfail-storm` | a broken delegation or DNSSEC failure: a slice of names stop resolving |
| `resolver-silent` | one resolver stops logging entirely |
| `client-flood` | one address hammering, until rate limiting denies it |
| `malware-beacon` | a few clients beaconing to blocked domains, so RPZ hits go from nothing to about 18% |

`clear` deliberately still carries a small background of failures. An alert
that fires on a single SERVFAIL was never going to survive production, and a
generator that produces a spotless stream hides that.

`resolver-silent` is the one worth testing against: nothing in the stream
says anything is wrong, because the signal is what stopped arriving. It is
the hardest outage to notice and the reason a per-source rate is worth
watching.

A fixed `--seed` repeats a run exactly, which is what makes a failure worth
reporting.

These messages are indistinguishable from real resolver logs once stored —
that is the point of them, and the reason they belong on a development box
and not in anything anyone reads as a record of what happened.

## Adding a log format

1. Create `internal/parser/<format>/` implementing `parser.Parser`:
   - operate on `in.Data` and set entry fields to substrings of it (one string allocation per message);
   - return `parser.ErrNotThisFormat` only when the input is not this format at all; record partial problems in `Entry.ParseError`;
   - never panic (the worker recovers, but panics are counted as bugs).
2. Add a `logentry.Format` value.
3. Register the parser in `pipeline.New` and, if detectable, extend `parser.Detect`.
4. Add table tests with real samples, a `Fuzz` target and a benchmark.
5. Document field mappings in [syslog.md](syslog.md) (or a format-specific guide).

## Conventions

- Conventional Commits (`feat(parser): …`, `fix: …`, `test: …`, `docs: …`).
- Every I/O function takes a `context.Context`.
- Every queue, buffer and response has a bound; every loss path increments a metric.
- Metric labels are bounded (configured names and enums only — never hostnames, IPs or user input).
- Errors crossing package boundaries are typed or wrapped with `%w`.
- Performance claims require a benchmark result ([testing.md §8](testing.md#8-benchmark-methodology)).
