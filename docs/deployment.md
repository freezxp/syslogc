# Deployment Architecture

Status: **Proposed** · Related: [architecture.md §7](architecture.md#7-runtime-roles-and-scaling), [ADR-0003](decisions/0003-single-binary-runtime-roles.md), [ADR-0004](decisions/0004-postgresql-metadata-store.md)

---

## 1. Artifacts

| Artifact | Contents |
|---|---|
| `syslogc` binary | Go server, all roles, web UI embedded, DB migrations embedded, `loggen` shipped separately |
| `ghcr.io/<org>/syslogc:<version>` | Distroless `static:nonroot` image, linux/amd64 + linux/arm64 |
| `ghcr.io/<org>/syslogc-loggen:<version>` | Generator image for load tests |
| `docker-compose.yml` | Single-node stack |
| `deploy/kubernetes/` | Placeholder + design notes (Phase 7: Helm chart) |

### 1.1 Image build

```dockerfile
# 1. frontend
FROM node:<lts>-alpine AS web
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
COPY docs/openapi.yaml /src/docs/openapi.yaml
RUN npm run build                        # → dist/

# 2. backend
FROM golang:1.27 AS build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=web /src/frontend/dist ./internal/api/webui/dist
ARG VERSION COMMIT
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/syslogc ./cmd/syslogc

# 3. runtime
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/syslogc /syslogc
USER 65532:65532
EXPOSE 8080 5514/udp 5514/tcp 6514/tcp
ENTRYPOINT ["/syslogc"]
CMD ["serve", "--config", "/etc/syslogc/syslogc.yaml"]
```

(Image and base versions pinned by digest in the real Dockerfile.)

## 2. CLI

```text
syslogc serve     [--config FILE] [--roles ingest,api] [--<any.config.key>=value]
syslogc migrate   up|status        # also run automatically on serve (advisory-locked)
syslogc config    validate|print   # print effective config (secrets redacted)
syslogc user      create|reset-password   # break-glass admin operations
syslogc version
```

## 3. Configuration

Precedence: **flags > environment > YAML > defaults**.

- Env mapping: `SYSLOGC_` + upper-snake path, e.g. `storage.victorialogs.endpoint` → `SYSLOGC_STORAGE_VICTORIALOGS_ENDPOINT`.
- Flags: dotted path, e.g. `--storage.victorialogs.endpoint=http://vl:9428`.
- Lists (sources) are YAML-only (or DB-managed via UI); env/flags cannot express them sensibly.

Reference configuration (excerpt; full reference generated into
`docs/configuration.md` in Phase 1):

```yaml
node:
  id: ""                          # default: hostname
  roles: [ingest, api]            # or [all]

server:
  http:
    address: ":8080"
    public_url: "http://localhost:8080"
    read_header_timeout: 10s
    idle_timeout: 120s
    max_body_bytes: 10MiB
    trusted_proxies: []
    tls: { enabled: false, cert_file: "", key_file: "" }
  metrics:
    address: ""                   # "" = same listener at /metrics; e.g. ":9090" for separate
    bearer_token_file: ""

log: { level: info, format: json }

storage:
  type: victorialogs
  victorialogs:
    insert_url: "http://victorialogs:9428"   # vlinsert in cluster mode
    select_url: "http://victorialogs:9428"   # vlselect in cluster mode
    stream_fields: [source, hostname, app_name]
    write_timeout: 30s
    query_timeout: 60s
    compression: gzip             # decided by spike S5
    auth: { basic_username: "", basic_password_file: "", bearer_token_file: "" }

metadata:
  postgres:
    dsn_file: /run/secrets/pg_dsn
    max_conns: 20

ingestion:
  queue: { max_messages: 500000, max_bytes: 256MiB }
  parse_workers: 0                # 0 = GOMAXPROCS
  batch: { max_rows: 10000, max_bytes: 8MiB, max_wait: 500ms }
  writers: 4
  time: { max_future_skew: 10m }
  http_enqueue_timeout: 2s
  sources: [ ... ]                # see ingestion.md

query:
  default_limit: 200
  max_tie_group: 5000
  max_concurrent: 64
  audit_all: false
  roles: { viewer: {...}, operator: {...}, admin: {...} }   # guards, see api.md

tail: { max_sessions: 200, max_session_duration: 1h }

retention:
  period: 30d                     # must equal VictoriaLogs -retentionPeriod (drift check)

auth:
  secret_key_file: /var/lib/syslogc/secret.key
  session: { ttl: 12h, idle_timeout: 1h, cookie_secure: true }
  bootstrap_admin: { username: admin, password_file: "" }

shutdown: { drain_delay: 5s, timeout: 30s }
```

## 4. Docker Compose (single node)

Target experience: `docker compose up -d` → UI at `http://localhost:8080`,
syslog on host ports 514/udp, 514/tcp, 6514/tcp.

```yaml
name: syslogc

services:
  syslogc:
    image: ghcr.io/<org>/syslogc:${SYSLOGC_VERSION:-latest}
    build: .
    restart: unless-stopped
    depends_on:
      victorialogs: { condition: service_healthy }
      postgres:     { condition: service_healthy }
    ports:
      - "8080:8080"          # UI + API
      - "514:5514/udp"       # syslog UDP
      - "514:5514/tcp"       # syslog TCP
      - "6514:6514/tcp"      # syslog TLS (source disabled by default)
    environment:
      SYSLOGC_STORAGE_VICTORIALOGS_INSERT_URL: http://victorialogs:9428
      SYSLOGC_STORAGE_VICTORIALOGS_SELECT_URL: http://victorialogs:9428
      SYSLOGC_METADATA_POSTGRES_DSN_FILE: /run/secrets/pg_dsn
      SYSLOGC_RETENTION_PERIOD: ${RETENTION:-30d}
      SYSLOGC_AUTH_COOKIE_SECURE: "false"      # local HTTP only; set true behind TLS
    secrets: [pg_dsn]
    volumes:
      - ./deploy/compose/syslogc.yaml:/etc/syslogc/syslogc.yaml:ro
      - syslogc-data:/var/lib/syslogc           # secret key, generated TLS material
    read_only: true
    security_opt: ["no-new-privileges:true"]
    cap_drop: [ALL]
    healthcheck:
      test: ["CMD", "/syslogc", "healthcheck"]   # distroless has no curl
      interval: 10s
      timeout: 3s
      retries: 5

  victorialogs:
    image: victoriametrics/victoria-logs:v1.52.0   # pinned; bump deliberately
    restart: unless-stopped
    command:
      - -storageDataPath=/vlogs
      - -retentionPeriod=${RETENTION:-30d}
      - -retention.maxDiskUsagePercent=85
      - -search.maxQueryDuration=120s
      - -search.maxConcurrentRequests=32
      - -httpListenAddr=:9428
    volumes: [vlogs-data:/vlogs]
    # no published ports: internal only. Uncomment for debugging:
    # ports: ["127.0.0.1:9428:9428"]
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:9428/health"]
      interval: 10s
      retries: 5

  postgres:
    image: postgres:17-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB: syslogc
      POSTGRES_USER: syslogc
      POSTGRES_PASSWORD_FILE: /run/secrets/pg_password
    secrets: [pg_password]
    volumes: [pg-data:/var/lib/postgresql/data]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U syslogc -d syslogc"]
      interval: 10s
      retries: 5

  # Optional observability profile: docker compose --profile monitoring up -d
  vmsingle:
    profiles: [monitoring]
    image: victoriametrics/victoria-metrics:<pinned>
    command: ["-promscrape.config=/etc/prometheus.yml", "-retentionPeriod=30d"]
    volumes: ["./deploy/compose/prometheus.yml:/etc/prometheus.yml:ro", "vm-data:/storage"]
  grafana:
    profiles: [monitoring]
    image: grafana/grafana:<pinned>
    ports: ["3000:3000"]
    volumes: ["./deploy/compose/grafana:/etc/grafana/provisioning:ro"]

secrets:
  pg_password: { file: ./deploy/compose/secrets/pg_password }   # created by `make compose-secrets`
  pg_dsn:      { file: ./deploy/compose/secrets/pg_dsn }

volumes: { syslogc-data: {}, vlogs-data: {}, pg-data: {}, vm-data: {} }
```

Notes:

- **Secrets without prerequisites.** The DoD requires plain `docker compose up -d` on a fresh clone. The file-based `secrets:` shown above therefore come from a one-shot `init` service (same image, `syslogc init-secrets`) that writes random secrets into a named volume on first run, with `syslogc` and `postgres` depending on it (`condition: service_completed_successfully`). Operators who manage secrets externally replace the volume with their own files. (Shown above in file form for readability; the final compose file is written in Phase 1.)
- The bootstrap admin password is printed in `docker compose logs syslogc` on first start when not provided.
- **Source IP preservation.** With Docker's default iptables port publishing, remote senders' source IPs are preserved for UDP/TCP. Traffic to `127.0.0.1` published ports, or setups where the userland proxy handles traffic (e.g. some IPv6 configurations, rootless Docker), shows the Docker gateway IP instead. For production syslog collection on Docker, `network_mode: host` is recommended and documented as an override file (`docker-compose.hostnet.yml`).
- Syslogc binds 5514 inside the container so it runs without `NET_BIND_SERVICE`.

### 4.1 Definition-of-Done smoke test

```bash
docker compose up -d
logger --server 127.0.0.1 --udp --port 514 "Test syslog message"
# UI: http://localhost:8080 → Logs → search "Test syslog message"
```

This exact sequence is automated in the e2e suite ([testing.md](testing.md#6-e2e)).

## 5. Scale-out topology

```text
                 Devices (syslog)                         Shippers (HTTP)          Users
                        │                                       │                     │
            ┌───────────▼───────────┐                 ┌─────────▼────────┐  ┌────────▼───────┐
            │ L4 LB: UDP 514,        │                 │ L7 LB / ingress  │  │ L7 LB / ingress│
            │ TCP 514, TCP 6514      │                 │ /api/v1/ingest   │  │ /, /api/v1/*   │
            │ (IPVS, NLB, MetalLB)   │                 └───┬──────────┬───┘  └───┬────────┬───┘
            └───┬──────────────┬─────┘                     │          │          │        │
         ┌──────▼─────┐ ┌──────▼─────┐              ┌──────▼─────┐ ┌──▼──────┐ ┌─▼──────┐ ┌▼───────┐
         │ syslogc    │ │ syslogc    │  …           │ syslogc    │ │ …       │ │syslogc │ │syslogc │
         │ role=ingest│ │ role=ingest│              │ role=ingest│ │         │ │role=api│ │role=api│
         └──────┬─────┘ └──────┬─────┘              └──────┬─────┘ └─────────┘ └───┬────┘ └──┬─────┘
                └──────────────┴────────────┬──────────────┘                       │         │
                                            ▼                                      ▼         ▼
                               ┌─────────────────────────┐              ┌─────────────────────────┐
                               │ vlinsert × N            │              │ vlselect × M            │
                               └────────────┬────────────┘              └────────────┬────────────┘
                                            └────────────▶ vlstorage × K ◀───────────┘
                                   PostgreSQL (managed / Patroni) ◀── all syslogc nodes
```

Scaling considerations:

| Topic | Guidance |
|---|---|
| UDP load balancing | Stateless per datagram; many LBs hash on 5-tuple, so one chatty device sticks to one node — acceptable; monitor per-node rate. Use DSR/`externalTrafficPolicy: Local` to keep source IPs |
| TCP syslog | Balanced per connection; long-lived connections do not rebalance → use `max_connection_age` (optional, off by default; closes gracefully so senders reconnect) |
| HTTP ingest | Standard L7 balancing |
| API nodes | Stateless; sessions in PostgreSQL; SSE requires proxy buffering off and idle timeout > heartbeat (15 s) |
| Rate limiting | Per-node in MVP; with many API nodes, effective limit = N × per-node limit (documented), shared limiter later |
| Config propagation | PostgreSQL `LISTEN/NOTIFY` + 30 s reconcile |
| Shared secrets | `auth.secret_key_file` must be identical on all API nodes |
| Migrations | Advisory-locked; only one node migrates; others wait; backwards-compatible (expand/contract) migrations for rolling upgrades |

## 6. Kubernetes readiness (Phase 7 implementation)

Design constraints satisfied from Phase 1:

- Stateless pods, configuration via ConfigMap (YAML) + Secrets (`*_file`), env overrides.
- Probes: `livenessProbe: /health`, `readinessProbe: /ready`, `startupProbe` covers migrations.
- `terminationGracePeriodSeconds` > `shutdown.drain_delay + shutdown.timeout` (default ≥ 45 s).
- Separate Deployments per role (`syslogc-ingest`, `syslogc-api`) from one image; HPA on CPU for ingest, on CPU/requests for API.
- UDP/TCP syslog via `Service type: LoadBalancer` with `externalTrafficPolicy: Local`; HTTP via Ingress/Gateway API (SSE timeouts annotated).
- PodDisruptionBudgets, topology spread, non-root security context, read-only root fs.
- VictoriaLogs via its official Helm charts (single or cluster); PostgreSQL via operator (CloudNativePG) or managed.
- `deploy/kubernetes/README.md` created in Phase 1 capturing these notes; Helm chart in Phase 7.

## 7. Resource sizing (initial estimates — to be replaced by Phase 6 measurements)

| Profile | Rate | syslogc | VictoriaLogs | PostgreSQL |
|---|---|---|---|---|
| Small | ≤ 2K logs/s | 1 vCPU, 512 MiB | 2 vCPU, 2 GiB, disk ≈ rate × retention × bytes/log (measured) | 1 vCPU, 512 MiB |
| Medium | ≤ 20K logs/s | 4 vCPU, 2 GiB | 8 vCPU, 16 GiB | 2 vCPU, 2 GiB |
| Large | 100K+ logs/s | multiple ingest nodes | VictoriaLogs cluster | 2–4 vCPU, 4 GiB |

VictoriaLogs guidance: keep ≥ 50 % free RAM and CPU headroom and ≥ 20 % free
disk. Disk sizing formula documented once bytes/log is measured (spike S7).

## 8. Operations

### 8.1 Backups

| Component | Method |
|---|---|
| PostgreSQL | `pg_dump` nightly (compose: sidecar/cron example) or managed PITR |
| VictoriaLogs | Per-day partition snapshots: `POST /internal/partition/snapshot/create?partition_prefix=YYYYMMDD`, rsync snapshot dirs to backup storage; restore via partition detach → copy → attach. Script `deploy/scripts/vl-backup.sh` + runbook (Phase 5) |
| Syslogc config | YAML in version control; DB-managed sources included in PostgreSQL backup |

### 8.2 Retention

- VictoriaLogs retention is set by `-retentionPeriod` (global) and optionally a disk cap (`-retention.maxDiskUsagePercent` or `-retention.maxDiskSpaceUsageBytes`, mutually exclusive).
- `retention.period` in Syslogc config must match; the storage adapter reads VictoriaLogs' effective flag values (via its `/flags` endpoint where available, otherwise config) and `/system/retention` reports `in_sync | drift | unknown`.
- Changing retention = change both values and restart VictoriaLogs (compose: edit `RETENTION` in `.env`). The UI shows these instructions; UI-driven retention changes are a capability of backends that support them.

### 8.3 Upgrades

1. Read release notes (migrations flagged).
2. Rolling: upgrade API nodes, then ingest nodes (ingest nodes drain on SIGTERM).
3. Migrations are expand/contract so N and N+1 run concurrently.
4. VictoriaLogs upgrades follow its own notes; pinned version bumped via PR with the integration suite.

### 8.4 Monitoring & alerts (shipped as examples)

`deploy/monitoring/alerts.yml` (Prometheus rules), e.g.:

- `SyslogcDroppingLogs`: `sum(rate(syslogc_ingest_messages_dropped_total[5m])) > 0` for 5 m
- `SyslogcStorageUnhealthy`: `min(syslogc_storage_healthy) == 0` for 2 m
- `SyslogcQueueSaturated`: `syslogc_ingest_queue_bytes / syslogc_ingest_queue_capacity_bytes > 0.8` for 5 m
- `SyslogcParseErrorsHigh`: parse error ratio > 5 % for 15 m
- `SyslogcUdpKernelDrops`: `rate(syslogc_ingest_udp_kernel_drops_total[5m]) > 0`
- `SyslogcIngestLatencyHigh`: p99 e2e latency > 10 s

Grafana dashboard JSON shipped in `deploy/monitoring/grafana/`.

## 9. Repository layout (deployment-related)

```text
.
├── docker-compose.yml
├── docker-compose.hostnet.yml
├── Dockerfile
├── Makefile                      # build, test, lint, compose-secrets, gen
├── deploy/
│   ├── compose/                  # syslogc.yaml, prometheus.yml, grafana provisioning, secrets/ (gitignored)
│   ├── kubernetes/               # README (Phase 1), Helm chart (Phase 7)
│   ├── monitoring/               # alerts.yml, grafana dashboards
│   └── scripts/                  # vl-backup.sh, gen-certs.sh
├── backend/
├── frontend/
└── docs/
```
