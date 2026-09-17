# Syslogc

A modern syslog server and log analytics platform: high-throughput ingestion
over standard protocols, normalized structured logs, and fast search on top of
[VictoriaLogs](https://docs.victoriametrics.com/victorialogs/).

> **Status: Phase 4 — web UI.** Syslog and JSON ingestion, an authenticated
> query API and the web UI (explorer, dashboard, live tail, saved searches,
> export) are in place. Source management in the UI, alerting and clustering
> follow — see the [roadmap](docs/roadmap.md).

## Quick start

Requirements: Docker with Compose v2.

```bash
git clone https://github.com/freezxp/syslogc.git
cd syslogc
docker compose up -d

# initial administrator password (printed once; you must change it at first login)
docker compose logs syslogc | grep -A3 "initial administrator"

# send a message exactly like a device would
logger --server 127.0.0.1 --udp --port 514 "Test syslog message"
```

Open `http://<host>:8080`, sign in as `admin`, and search for `Test`.

Operational endpoints: `/health`, `/ready` (component and source status),
`/metrics` (Prometheus). The UI is plain HTTP in the Compose stack — put a TLS
reverse proxy in front before exposing it; see
[installation](docs/installation.md) for production notes, TLS syslog and
preserving sender IP addresses.

## What works today

| Area | Capability |
|---|---|
| Protocols | Syslog over UDP (multi-socket `SO_REUSEPORT`), TCP (RFC 6587 octet counting and LF framing, auto-detected), TLS (RFC 5425, certificate hot reload, optional client certificates); JSON over HTTP (objects, arrays, NDJSON, gzip) with API keys |
| Formats | RFC 5424 (including structured data), lenient RFC 3164 with vendor variations, structured JSON with field aliases and nested flattening; unparseable messages are stored, never dropped |
| Normalization | Canonical severity/facility, timestamp policy with skew fallback, UTF-8 sanitization, dynamic field naming rules and limits, raw message policy (default: keep only on parse errors), static labels |
| Pipeline | Bounded queue (count + byte budget), parallel parsing, batched writes, retry with backoff, bisecting of rejected batches, protocol-specific backpressure, graceful drain |
| Search | Query builder filters (text, equals, contains, regex, numeric, CIDR, in, exists) and LogsQL advanced mode; relative/absolute time ranges with time zones; stable cursor pagination; histograms, facets, field discovery; live tail over SSE; streaming CSV/NDJSON/JSON export |
| Web UI | Explorer with virtualized results, field sidebar and log detail, dashboard (volume, errors, top hosts/apps, ingestion rate), live tail, saved searches, API keys, system health |
| Security | Local accounts (Argon2id), server-side sessions with CSRF protection, scoped API keys, viewer/operator/admin roles, per-role query limits, audit log |
| Operations | Prometheus metrics for every loss path, readiness checks, retention drift detection, structured JSON logs, config via YAML/env/flags, PostgreSQL metadata with automatic migrations |
| Tooling | `loggen` traffic generator, distroless non-root image with embedded UI, Compose stack, OpenAPI 3.1 contract, CI |

## Documentation

| Guide | |
|---|---|
| [Installation](docs/installation.md) | Compose, host networking, TLS, upgrades |
| [Configuration](docs/configuration.md) | Every setting, environment variables, sources |
| [Syslog ingestion](docs/syslog.md) | Formats, framing, field mapping, sender examples |
| [Operations](docs/operations.md) | Sources, users, retention, monitoring, backups |
| [Troubleshooting](docs/troubleshooting.md) | Symptoms, causes and checks |
| [JSON ingestion](docs/json-ingestion.md) | HTTP ingest API, API keys, field mapping |
| [Querying](docs/querying.md) | Filters, LogsQL, pagination, tail, export |
| [API reference](docs/openapi.yaml) | OpenAPI 3.1 |
| [Development](docs/development.md) | Code layout, tests, fuzzing, adding a parser |
| [Architecture](docs/architecture.md) | Design and decisions ([ADRs](docs/decisions/README.md)) |
| [Roadmap](docs/roadmap.md) | Phases and status |

## Development

```bash
make web          # build the web UI for embedding
make build        # bin/syslogc, bin/loggen
make test         # unit tests with -race (starts a PostgreSQL container)
make lint         # golangci-lint, including architecture boundary rules
make integration  # tests against real VictoriaLogs and PostgreSQL containers
make up && make e2e
```

Requires Go 1.27+, Node.js 24, Docker, and util-linux `logger` for end-to-end tests.

## License

[Apache License 2.0](LICENSE).
