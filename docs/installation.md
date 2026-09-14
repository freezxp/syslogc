# Installation

The web UI and API require login. Serve them over HTTPS (a TLS-terminating
reverse proxy) before exposing them beyond a trusted network.

## Docker Compose (single node)

Requirements: Docker Engine 24+ with Compose v2, ~2 GB RAM, disk sized for
your log volume and retention.

```bash
git clone https://github.com/freezxp/syslogc.git
cd syslogc
docker compose up -d
docker compose ps          # syslogc should become (healthy)
curl -s http://127.0.0.1:8080/ready | python3 -m json.tool

# initial administrator password (printed once on first start)
docker compose logs syslogc | grep -A3 "initial administrator"
```

Open `http://<host>:8080`, sign in as `admin` with that password and choose a
new one.

The stack:

| Service | Image | Ports | Data |
|---|---|---|---|
| `syslogc` | built from this repository (`ghcr.io/freezxp/syslogc`) | `514/udp`, `514/tcp` → container `5514`; `8080` web UI/API | stateless |
| `init` | same image, runs `syslogc init-secrets` once | — | volume `secrets` |
| `postgres` | `postgres:17.6-trixie` (users, sessions, saved searches, audit) | internal only | volume `postgres-data` |
| `victorialogs` | `victoriametrics/victoria-logs:v1.52.0` (logs) | internal only | volume `vlogs-data` |

Environment variables (e.g. in `.env`):

| Variable | Default | |
|---|---|---|
| `SYSLOGC_HTTP_BIND` | `0.0.0.0` | Set `127.0.0.1` to keep the UI local (e.g. behind a reverse proxy on the host). |
| `SYSLOGC_HTTP_PORT` | `8080` | Host port for the UI/API. |
| `SYSLOGC_AUTH_COOKIE_SECURE` | `false` | Set `true` once the UI is served over HTTPS. |
| `SYSLOGC_RETENTION` | `30d` | See [Retention](#retention). |

Configuration is mounted from [`deploy/compose/syslogc.yaml`](../deploy/compose/syslogc.yaml).

### Verify

```bash
logger --server 127.0.0.1 --udp --port 514 "Test syslog message"
logger --server 127.0.0.1 --tcp --port 514 --rfc3164 -p local4.err -t vpnd "VPN tunnel down"
# open the explorer and search for "Test", or run the automated check:
make e2e    # logs in, searches, tails, exports; see backend/tests/e2e/smoke.sh
```

### Retention

Set `SYSLOGC_RETENTION` (for example in a `.env` file next to
`docker-compose.yml`) before `docker compose up -d`. It configures both
VictoriaLogs `-retentionPeriod` and Syslogc `retention.period`, which must
match; `/ready` reports `retention.status: drift` if they differ.

```bash
echo "SYSLOGC_RETENTION=90d" > .env
docker compose up -d        # recreates victorialogs with the new retention
```

VictoriaLogs additionally deletes the oldest data when disk usage exceeds 85 %
(`-retention.maxDiskUsagePercent=85` in the compose file).

### Preserving sender IP addresses

With Docker's default port publishing, traffic that passes through Docker's
userland proxy arrives with the Docker gateway as source address. This always
happens for traffic sent to `127.0.0.1` (so local `logger` tests show
`source_ip` like `172.18.0.1`) and in some IPv6 and rootless setups. Remote
IPv4 senders are normally preserved via iptables DNAT, but for production
syslog collection host networking is the reliable option:

```bash
docker compose -f docker-compose.yml -f docker-compose.hostnet.yml up -d
```

Syslogc runs as a non-root user and listens on port **5514** in this mode.
Point devices at 5514 or redirect 514 on the host:

```bash
sudo iptables -t nat -A PREROUTING -p udp --dport 514 -j REDIRECT --to-ports 5514
sudo iptables -t nat -A PREROUTING -p tcp --dport 514 -j REDIRECT --to-ports 5514
```

### Syslog over TLS

1. Place the certificate and key in `deploy/compose/tls/` (`server.crt`, `server.key`).
2. Mount them and publish the port in `docker-compose.yml`:
   ```yaml
   services:
     syslogc:
       ports: ["6514:6514/tcp"]
       volumes:
         - ./deploy/compose/tls:/etc/syslogc/tls:ro
   ```
3. Set `enabled: true` on the `syslog-tls` source in `deploy/compose/syslogc.yaml`.
   For mutual TLS set `tls.client_auth: require_and_verify` and `tls.client_ca_file`.
4. `docker compose up -d`. Certificates are reloaded automatically when the files change.

Test with: `logger --server 127.0.0.1 --tcp --port 6514 ...` does **not** speak
TLS; use `openssl s_client -connect 127.0.0.1:6514` or a TLS-capable sender
(rsyslog `omfwd` with `StreamDriver="gtls"`, syslog-ng `transport("tls")`,
Vector `socket` sink with TLS). `loggen --protocol tls --tls-insecure` also works.

### Kernel tuning for UDP

Under bursts the kernel may drop datagrams before Syslogc reads them.
Syslogc requests an 8 MiB receive buffer per socket but the kernel caps it at
`net.core.rmem_max` and logs a warning when that happens. Drops are exported as
`syslogc_ingest_udp_kernel_drops_total`.

```bash
sudo sysctl -w net.core.rmem_max=33554432
echo 'net.core.rmem_max=33554432' | sudo tee /etc/sysctl.d/90-syslogc.conf
```

### Memory

The ingest queue is bounded by `ingestion.queue.max_bytes` (default 256 MiB)
and the batch queue by `writers × 2 × batch.max_bytes`. Measured resident
memory with a completely full default queue was ~520–600 MiB, because the Go
garbage collector needs headroom above live data. When running with a
container memory limit, set `GOMEMLIMIT` to about 80 % of the limit so the
collector works harder instead of the process being OOM-killed, and reduce
`ingestion.queue.max_bytes` for small limits.

## Running the binary directly

```bash
make build
bin/syslogc config validate --config deploy/compose/syslogc.yaml \
  --storage.victorialogs.insert_url=http://127.0.0.1:9428 \
  --storage.victorialogs.select_url=http://127.0.0.1:9428
bin/syslogc serve --config my-config.yaml
```

Binding ports below 1024 requires root or `CAP_NET_BIND_SERVICE`
(`sudo setcap cap_net_bind_service=+ep bin/syslogc`).

## Upgrades

1. Read the release notes.
2. `git pull && docker compose up -d --build`.
3. Syslogc drains in-flight logs on `SIGTERM` (compose `stop_grace_period: 45s`).
   TCP senders reconnect automatically; UDP datagrams sent while the container
   restarts are lost — use TCP/TLS with a sender-side queue if that matters.

## Uninstall

```bash
docker compose down        # keeps stored logs (volume vlogs-data)
docker compose down -v     # also deletes stored logs
```
