# Development

## Toolchain

| Tool | Version | Used for |
|---|---|---|
| Go | 1.27+ (`backend/go.mod`) | backend |
| golangci-lint | 2.13+ | lint, formatting, architecture boundaries |
| Docker + Compose v2 | 24+ | integration tests, image, e2e |
| util-linux `logger` | any | e2e and logger compatibility tests |
| Python 3 | any | e2e script JSON checks |

## Layout

```text
backend/
├── cmd/
│   ├── syslogc/          # server binary: serve, config, healthcheck, version
│   └── loggen/           # synthetic traffic generator
├── internal/
│   ├── app/              # composition root: wiring, startup, graceful shutdown
│   ├── api/              # HTTP server: health, readiness, metrics, dev search
│   ├── config/           # model, defaults, loading (YAML/env/flags), validation
│   ├── ingestion/
│   │   ├── framing/      # RFC 6587 stream framing
│   │   ├── listener/     # UDP, TCP, TLS receivers
│   │   ├── pipeline/     # bounded queue, parse workers, batching, writers
│   │   ├── source/       # resolved per-source runtime settings
│   │   └── supervisor/   # starts/stops configured sources
│   ├── logentry/         # normalized log model (dependency-free)
│   ├── loggen/           # message generator used by cmd/loggen
│   ├── metrics/          # Prometheus metric definitions
│   ├── normalization/    # data-model rules applied after parsing
│   ├── parser/           # parser contract, detection, rfc5424, rfc3164
│   └── storage/          # backend-neutral interfaces
│       └── victorialogs/ # VictoriaLogs adapter
└── tests/
    ├── integration/      # against real VictoriaLogs (build tag: integration)
    └── e2e/              # compose smoke test
```

Architecture rules enforced by `golangci-lint` (depguard):

- Only `internal/app` (and tests) may import `internal/storage/victorialogs`; everything else uses `internal/storage` interfaces ([ADR-0002](decisions/0002-storage-abstraction.md)).
- `internal/logentry` imports only the standard library.

## Everyday commands

```bash
make build          # bin/syslogc, bin/loggen
make test           # go test -race ./...
make lint           # golangci-lint run
make fmt            # gofmt + goimports
make integration    # starts VictoriaLogs on 127.0.0.1:19428, runs integration tests
make vl-down        # stop it
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
  --log.format=text --shutdown.drain_delay=0s
```

## Testing

| Layer | Where | Notes |
|---|---|---|
| Unit | next to code (`*_test.go`) | table-driven; `go-cmp` for diffs |
| Fuzz | `Fuzz*` in parsers, framing, encoder | seed corpora run in `go test`; crashers are committed under `testdata/fuzz` as regression cases |
| Pipeline | `internal/ingestion/pipeline` | fake storage writer that blocks, fails, or rejects rows |
| Listener | `internal/ingestion/listener` | real sockets on 127.0.0.1, self-signed TLS certificate |
| Integration | `backend/tests/integration` | `TEST_VICTORIALOGS_URL`; hostile-value round trips, UDP/TCP → storage, util-linux `logger` compatibility |
| E2E | `backend/tests/e2e/phase1-smoke.sh` | the Definition-of-Done flow against `docker compose` |

Integration tests skip (not fail) when `TEST_VICTORIALOGS_URL` is unset, so
`go test ./...` works without Docker. CI always sets it.

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
