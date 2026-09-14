# Syslogc

A modern syslog server and log analytics platform: high-throughput ingestion
over standard protocols, normalized structured logs, and fast search on top of
[VictoriaLogs](https://docs.victoriametrics.com/victorialogs/).

> **Status: Phase 1 — core ingestion.** Syslog over UDP/TCP/TLS is parsed,
> normalized, batched and stored with full ingestion metrics. The web UI,
> authenticated query API, HTTP/JSON ingestion and source management arrive
> in later phases — see the [roadmap](docs/roadmap.md).

## Quick start

Requirements: Docker with Compose v2.

```bash
git clone https://github.com/freezxp/syslogc.git
cd syslogc
docker compose up -d

# send a message exactly like a device would
logger --server 127.0.0.1 --udp --port 514 "Test syslog message"

# find it (Phase 1 development endpoint; the web UI replaces this later)
curl -s "http://127.0.0.1:8080/api/v1/dev/search?query=Test&from=15m" | python3 -m json.tool
```

Operational endpoints on `http://127.0.0.1:8080`: `/health`, `/ready`
(component and source status), `/metrics` (Prometheus).

The HTTP port is bound to localhost in Phase 1 because the development search
endpoint is unauthenticated. See [installation](docs/installation.md) for
production notes, TLS syslog and preserving sender IP addresses.

## What works today

| Area | Phase 1 capability |
|---|---|
| Protocols | Syslog over UDP (multi-socket `SO_REUSEPORT`), TCP (RFC 6587 octet counting and LF framing, auto-detected), TLS (RFC 5425, certificate hot reload, optional client certificates) |
| Formats | RFC 5424 (including structured data) and lenient RFC 3164 with vendor variations (Cisco sequence numbers and timezones, ISO timestamps, missing hostname/PRI); per-message auto-detection; unparseable messages are stored, never dropped |
| Normalization | Canonical severity/facility names and codes, timestamp policy with skew fallback, RFC 3164 year inference, UTF-8 sanitization, dynamic field naming rules and limits, raw message retention policy, static labels |
| Pipeline | Bounded queue (count + byte budget), parallel parsing, batched writes, retry with backoff, bisecting of rejected batches, protocol-specific backpressure (UDP drops are counted, TCP blocks), graceful drain on shutdown |
| Storage | VictoriaLogs adapter behind a backend-neutral storage interface |
| Operations | Prometheus metrics for every loss path, readiness with storage/source/queue checks, retention drift detection, structured JSON logs, config via YAML/env/flags |
| Tooling | `loggen` synthetic traffic generator, Docker image (distroless, non-root), Compose stack, CI |

## Documentation

| Guide | |
|---|---|
| [Installation](docs/installation.md) | Compose, host networking, TLS, upgrades |
| [Configuration](docs/configuration.md) | Every setting, environment variables, sources |
| [Syslog ingestion](docs/syslog.md) | Formats, framing, field mapping, sender examples |
| [Development](docs/development.md) | Code layout, tests, fuzzing, adding a parser |
| [Architecture](docs/architecture.md) | Design and decisions ([ADRs](docs/decisions/README.md)) |
| [Roadmap](docs/roadmap.md) | Phases and status |

## Development

```bash
make build        # bin/syslogc, bin/loggen
make test         # unit tests with -race
make lint         # golangci-lint, including architecture boundary rules
make integration  # tests against a real VictoriaLogs container
make up && make e2e
```

Requires Go 1.27+, Docker, and util-linux `logger` for end-to-end tests.

## License

Not yet chosen (see [open questions](docs/roadmap.md#open-questions-for-review)).
