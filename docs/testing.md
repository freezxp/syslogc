# Testing Strategy

Status: **Proposed** · Principles from the brief: *write tests before optimizing; benchmark before making performance claims.*

---

## 1. Test pyramid

```text
                    ┌───────────────┐
                    │  Load & bench │  nightly / pre-release; results in docs/performance.md
                  ┌─┴───────────────┴─┐
                  │   E2E (compose)   │  DoD scenario, Playwright, per PR (smoke) / nightly (full)
                ┌─┴───────────────────┴─┐
                │ Integration (containers)│  VictoriaLogs + PostgreSQL via testcontainers, per PR
              ┌─┴─────────────────────────┴─┐
              │ Contract (storage adapters)  │  shared suite, per adapter, per PR
            ┌─┴─────────────────────────────┴─┐
            │ Unit + fuzz + property + golden  │  every commit, < 60 s
            └─────────────────────────────────┘
```

## 2. Backend unit tests

Conventions: standard `testing` package, table-driven tests, `go-cmp` for diffs,
golden files under `testdata/` updated with `-update`, `t.Parallel()` where
safe, `go test -race` in CI.

| Area | What is tested | Techniques |
|---|---|---|
| RFC 5424 parser | RFC examples, every NILVALUE combination, SD escaping, BOM, invalid UTF-8, max lengths, malformed SD recovery | Table-driven, golden, **fuzz** (`FuzzParse`: never panics, output invariants), differential fuzz vs reference parser |
| RFC 3164 parser | Vendor corpus (Cisco, Juniper, Fortinet, Palo Alto, pfSense, MikroTik, ESXi, rsyslog…), missing PRI/hostname/tag, year rollover, timezones, ISO timestamps | Golden corpus, fuzz, clock injection |
| Format detection | Prefix cases, ambiguous inputs | Table-driven, fuzz |
| Framing | Octet counting, LF, CRLF, NUL, mixed on one connection, oversize frames, partial reads, `logger --tcp` output captures | Table-driven with `iotest.OneByteReader`/`HalfReader`, fuzz |
| JSON parser | Object/array/NDJSON, aliases, flattening, depth/field limits, collisions, big numbers, invalid lines | Table-driven, fuzz |
| Normalization | Severity/facility mapping, timestamp policy (skew, fallback), network identity rules, raw policy, limits | Table-driven |
| Filter AST | Validation limits, JSON (un)marshalling of discriminated union | Table-driven, round-trip property tests |
| LogsQL compiler | Each op → expected LogsQL; escaping of hostile strings; native split lexer (quotes, escapes, parentheses, pipes inside strings) | Golden, **fuzz**, property: `lex(compile(ast))` well-formed |
| URL filter text form (shared spec with frontend) | text ↔ AST round trip | Shared test vectors JSON consumed by Go and TS tests |
| Cursor codec | Encode/decode, HMAC tamper detection, query-hash binding, version | Table-driven, fuzz decoder |
| Pipeline | Batching triggers (rows/bytes/wait), backpressure per protocol, drop accounting, retry/backoff classification, bisect on rejected rows, shutdown draining | Fake `LogWriter` (slow, failing, rejecting), `testing/synctest` for deterministic time |
| Config | Precedence (defaults/YAML/env/flags), validation errors aggregated, secret redaction | Table-driven |
| Auth | Argon2id verify/rehash, session lifecycle (idle/absolute expiry, rotation, revocation), API key parsing, CSRF, rate limiter | Table-driven, fake clock |
| Authorization | Permission matrix per role; every route declared | Generated route × role table test |
| Export encoders | CSV quoting & formula neutralization, JSON array streaming, truncation marker | Golden |
| Query guards | Range/limit/timeout/concurrency per role | Table-driven |

Coverage targets (guidance, not a gate by itself): parsers, compiler, cursor,
auth ≥ 90 % statements; overall backend ≥ 75 %. Mutation testing
(`go-mutesting`) on parsers and compiler before MVP release.

## 3. Storage contract suite

`internal/storage/storagetest` exports:

```go
func RunContract(t *testing.T, newBackend func(t *testing.T) storage.Backend)
```

Every adapter's integration test calls it. Cases:

1. Write → Search round trip preserves all core and dynamic fields (value equality per data model).
2. Projection returns only requested fields.
3. Time range boundaries: `[start, end)` semantics, nanosecond precision.
4. Ordering desc/asc; **cursor paging over tie groups**: 50K rows with identical second-precision timestamps across 3 streams → complete, no duplicates, `tie_overflow` behaviour at cap.
5. Every AST operator against a fixture dataset with known expected row sets.
6. **Injection property test**: 1,000 random hostile values stored and filtered with `eq` → exactly the expected rows.
7. Tenant isolation: rows written for tenant A invisible to B on every read method.
8. Histogram: bucket alignment, empty buckets, split field counts sum to total, `other` bucket.
9. Facets/top values/field names/field values counts match fixture expectations.
10. Tail: rows written after start appear within deadline; context cancel closes stream.
11. Export: streams N rows with bounded memory (allocation check).
12. Write error classification: rejected rows, retryable failures (via fault-injecting proxy).
13. Native dialect: supported dialect validates; unsupported dialect returns `ErrUnsupportedDialect`.

## 4. Integration tests

Located in `backend/tests/integration`, build tag `integration`, using
`testcontainers-go` with pinned VictoriaLogs and PostgreSQL images.

| Scenario | Path |
|---|---|
| Syslog UDP → parser → normalizer → storage | Real UDP socket → pipeline → VictoriaLogs → query via adapter |
| Syslog TCP (octet + LF framing) and TLS | Real listeners, self-signed certs generated in test |
| `logger` compatibility | Run util-linux `logger` in a container against the listener (UDP, TCP, RFC 3164/5424 modes) |
| HTTP JSON → storage | `POST /api/v1/ingest` with object/array/NDJSON/gzip, API key auth |
| API → storage → query | Seed fixture dataset; call search/histogram/facets/fields/export over HTTP; compare to expected |
| Backpressure | Toxiproxy between Syslogc and VictoriaLogs: latency, outage → verify TCP no loss, UDP drops counted, HTTP 503, recovery |
| Source supervisor | Create/update/disable sources via API → listeners change without restart |
| Auth flows | Login, CSRF, session expiry, role enforcement end to end |
| Migrations | Up from empty; up from previous release schema snapshot |
| Retention drift | VictoriaLogs started with a different `-retentionPeriod` → `/system/retention` reports drift |

Runtime target: < 8 min on CI runners, parallelized per package.

## 5. Frontend tests

| Level | Tooling | Scope |
|---|---|---|
| Unit | Vitest | Time range parsing/resolution, URL codecs, filter text form (shared vectors), formatters, tail ring buffer |
| Component | Vitest + React Testing Library + MSW | Query builder interactions, facets filter/exclude, time picker validation, log detail actions, source form validation, permission-based rendering |
| Accessibility | `axe-core` in component tests + Playwright | No critical violations on each page |
| Visual regression | Playwright screenshots (dark + light) on key pages with fixed data | Nightly; diffs reviewed, not auto-failing on 1 px |
| Performance | Playwright trace on explorer with 10K rows and tail at 2K rows/s | Long tasks > 50 ms counted; budget regression fails nightly |

## 6. E2E

`backend/tests/e2e` + `frontend/tests` (Playwright) against
`docker compose up -d` of the real images.

**DoD scenario (automated, per PR smoke):**

1. `docker compose up -d`, wait for `/ready`.
2. Read bootstrap admin password from logs; log in; change password.
3. `logger --server 127.0.0.1 --udp --port 514 "Test syslog message <uuid>"`.
4. Search for the UUID → row visible (retry up to 10 s).
5. Filter by hostname, severity, facility via facets.
6. Set custom absolute time range containing the message.
7. Ingest a JSON log with dynamic fields via API key; open detail; dynamic fields shown.
8. Histogram shows ≥ 1 in the right bucket.
9. Dashboard ingestion rate tile > 0 while `loggen` runs at 100/s.
10. Live tail shows messages sent after opening.
11. Export CSV/NDJSON; file contains the rows.
12. Save the search; reload; open from list.
13. System health page shows all components healthy.
14. Ingestion metrics page shows received/parsed/stored counters increasing.

Full nightly E2E adds: TLS syslog with client certs, role restrictions per page,
source create/disable from UI, backpressure banner behaviour, XSS corpus
rendering (payloads in messages and field names must render inertly).

## 7. Load testing

Tools: `loggen` (syslog/HTTP ingest), **k6** (query API and SSE clients),
`vmstat`/cAdvisor + Prometheus scraping of Syslogc and VictoriaLogs metrics.

Profiles (`backend/tests/load/profiles/*.yaml`):

| Profile | Rate | Mix | Duration |
|---|---|---|---|
| L1 | 1K logs/s | 60 % RFC 5424 / 30 % RFC 3164 / 10 % JSON, UDP+TCP | 10 min |
| L10 | 10K logs/s | same | 30 min |
| L50 | 50K logs/s | same | 30 min |
| L100 | 100K logs/s | same; TCP with 32 connections + UDP | 60 min |
| Soak | 20K logs/s | same | 24 h (pre-release) |
| Burst | 5K baseline, 10 s bursts of 100K | UDP | 30 min |
| Outage | 20K logs/s, storage paused 60 s via Toxiproxy | TCP + HTTP | 15 min |
| Query | 20 concurrent users: explorer page, histogram, facets, 1h/24h/7d | k6 | 15 min, during L10 ingest |
| Tail | 100 SSE clients during L10 | k6 | 15 min |

Measured for every run:

| Metric | Source |
|---|---|
| Throughput: target vs generated vs received vs stored | loggen report, `syslogc_ingest_*` counters, storage `count` by `loggen_run` |
| Loss: dropped by reason, UDP kernel drops, sequence gaps | metrics + `loggen_seq` verification query |
| CPU, RAM (RSS, Go heap), GC pause | cAdvisor/`process_*`, `go_*` metrics |
| Ingest latency p50/p95/p99 | `syslogc_ingest_e2e_latency_seconds` |
| Storage write latency, errors | `syslogc_storage_write_*` |
| Query latency p50/p95/p99 per endpoint | k6 + `syslogc_http_request_duration_seconds` |
| Storage bytes on disk per log | VictoriaLogs metrics before/after |

## 8. Benchmark methodology

Rules to keep numbers honest:

1. **Record the environment**: CPU model, cores, RAM, disk type, kernel, sysctls (`net.core.rmem_max`), Go/VictoriaLogs/Syslogc versions and commit, full config.
2. **Separate generator from system under test** (different machines, or pinned cores with documented contention) and verify the generator achieved its target rate.
3. **Warm up** 2 minutes; measure steady state for ≥ 10 minutes; report the median of 3 runs.
4. **Pass criteria** for claiming "N logs/s": sustained for the full window with (a) zero drops for TCP/HTTP, (b) UDP drops < 0.01 % including kernel drops, (c) p99 ingest latency ≤ 2 s, (d) queue occupancy not trending upward, (e) memory flat.
5. **Micro-benchmarks** (`go test -bench`, `-benchmem`, `benchstat` with ≥ 10 runs) for parsers, encoder, compiler; CI tracks allocations per op and fails on > 10 % regressions vs main.
6. **Publish** results (including failures and the bottleneck found) in `docs/performance.md`. No throughput claim appears in README/docs without a linked result.
7. Profiles (`pprof` CPU/heap/mutex) captured for every run above L10 and attached to the result.

## 9. CI pipeline (GitHub Actions)

| Stage | Trigger | Jobs |
|---|---|---|
| Lint | PR | `golangci-lint` (incl. gosec, staticcheck, depguard boundaries, forbidigo), `eslint`, `prettier --check`, Spectral lint of `openapi.yaml`, `hadolint`, generated-code drift check |
| Unit | PR | `go test -race ./...` (short fuzz seed corpus), `vitest run` |
| Contract + integration | PR | testcontainers suite (VictoriaLogs pinned) |
| Build | PR | Multi-arch image build (no push), SBOM, Trivy scan |
| E2E smoke | PR | Compose DoD scenario (headless Playwright) |
| Security | PR + weekly | `govulncheck`, `osv-scanner` |
| Fuzz | Nightly | 10 min per fuzz target; crashers committed as regression cases |
| E2E full + visual + perf budgets | Nightly | |
| Compat | Weekly | Integration suite against latest VictoriaLogs release |
| Load (L10/L50) | Weekly, self-hosted runner | Results archived; regressions flagged |
| Release | Tag | Build, sign (cosign), push, attach SBOM, changelog |

## 10. Test data

- `testdata/corpus/` — anonymized real-world syslog samples per vendor (no customer data; synthetic where licensing unclear).
- `testdata/hostile/` — XSS payloads, control characters, huge fields, deep JSON, invalid UTF-8, LogsQL metacharacters.
- `tests/fixtures/dataset-small.ndjson` — deterministic dataset (~50K rows) with known facet counts for contract/API tests, generated by `loggen --seed`.
