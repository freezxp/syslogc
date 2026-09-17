# Troubleshooting

Start with `/ready` and the **System** page: they name the failing component.

```bash
curl -s http://127.0.0.1:8080/ready | python3 -m json.tool
docker compose logs --tail=100 syslogc
```

## Logs are not arriving

Work outward from the server.

1. **Is the listener running?** System → Health, or `/ready`. A source in
   state `error` shows why (usually a taken port or an unreadable
   certificate). `state: disabled` means `enabled: false`.
2. **Does the server receive anything?**
   ```bash
   curl -s http://127.0.0.1:8080/metrics | grep messages_received_total
   ```
   Zero for the source means the traffic never arrives: check the sender's
   address and port, and any firewall between them. In the Compose stack the
   host ports are 514/udp and 514/tcp, mapped to 5514 inside the container.
3. **Is it being dropped?**
   ```bash
   curl -s http://127.0.0.1:8080/metrics | grep dropped_total
   ```
   - `reason="denied"`: the sender's IP is not in the source's `allowed_cidrs`.
   - `reason="queue_full"`: storage cannot keep up (see below).
   - `reason="oversize"`: raise `max_message_bytes`; UDP datagrams are also
     capped by the sender's MTU.
4. **Is it stored but outside your search?** The time range is the usual
   cause: a device with a wrong clock or timezone lands far from "now".
   Search `now-7d` to `now+1h` for the hostname, then look at `timestamp`,
   `timestamp_raw` and `time_source`. Fix the device clock, or set
   `timezone` on the source for RFC 3164 senders that omit an offset.
5. **Sent to the wrong protocol?** A TCP sender to a UDP-only source gets a
   refused connection; UDP to a TCP-only source disappears silently.

## Everything is slow, or logs are dropped under load

- `syslogc_ingest_queue_messages` near capacity means storage is the
  bottleneck. Check `syslogc_storage_write_duration_seconds` and VictoriaLogs
  disk I/O; raise `ingestion.writers` and `ingestion.batch.max_rows`.
- `syslogc_ingest_udp_kernel_drops_total` rising means the kernel discards
  datagrams before Syslogc sees them. Raise `net.core.rmem_max` (e.g.
  `sysctl -w net.core.rmem_max=16777216`) and the source's
  `udp.read_buffer_bytes`, and increase `udp.sockets`.
- Slow searches: narrow the time range first — it decides how much data is
  scanned. Free-text search across a wide range is the most expensive query
  shape. Per-role limits (range, rows, timeout) are in
  [security](security.md#query-limits).

## Storage problems

`/ready` reporting `storage: failed`, or `syslogc_storage_healthy 0`:

```bash
docker compose logs --tail=50 victorialogs
curl -s http://127.0.0.1:8080/api/v1/system/storage   # requires login
```

- **Disk full**: VictoriaLogs stops accepting writes. Free space, or lower
  retention. It deletes the oldest data itself above
  `-retention.maxDiskUsagePercent`.
- **Unreachable**: check `storage.victorialogs.insert_url` and that both
  containers are on the same network.
- Writes are retried with backoff and batches that the backend rejects are
  bisected, so a transient failure costs latency, not data, until the queue
  fills.

## Cannot log in

- **First login**: the generated administrator password is printed once, at
  startup: `docker compose logs syslogc | grep -A3 "initial administrator"`.
  If the account already exists and the password is lost, reset it from
  another administrator account, or (last resort) delete the row from the
  `users` table and restart to bootstrap again.
- **"you must change your password"**: the account is in forced-change
  state; the UI redirects to the change form. API clients must call
  `PUT /api/v1/auth/me/password` before anything else works.
- **Login returns 403 with `csrf_failed`**: the browser's `Origin` does not
  match the server's `Host`. Behind a reverse proxy, either forward the
  original `Host` header or list the public URL in
  `server.http.allowed_origins`.
- **Login succeeds but every request is 401**: the session cookie is not
  coming back. Over plain HTTP the cookie must not be `Secure` — set
  `auth.cookie_secure: false` (the Compose stack does).
- **Locked out after repeated failures**: logins are delayed per username
  after five failures, up to 15 minutes. Wait, or use another account.

## Reverse proxy issues

- **Live tail shows nothing**: the proxy is buffering the event stream. Set
  `proxy_buffering off` (nginx) and a read timeout of at least an hour.
- **Exports cut off**: raise the proxy's read timeout and body size limits.
- **Every client shows the same IP** in the audit log: set
  `server.http.trusted_proxies` to the proxy's address so `X-Forwarded-For`
  is trusted.

## Sources created in the UI do not start

- Give it five seconds: nodes reconcile on a notification, and poll as a
  fallback.
- A name or address that clashes with the configuration file is ignored on
  purpose; the server logs `managed source ignored` with the reason.
- Check the source's state and error on the Sources page. Ports below 1024
  need privileges the container does not have — bind 5514 and map the port
  outside.

## Useful commands

```bash
# Effective configuration of a running node (secrets redacted)
docker compose exec syslogc syslogc config print --config /etc/syslogc/syslogc.yaml

# Validate a configuration file before restarting
docker compose exec syslogc syslogc config validate --config /etc/syslogc/syslogc.yaml

# Follow one source's ingestion counters
watch -n2 'curl -s localhost:8080/metrics | grep "source=\"syslog-udp\""'

# Generate load (from the repository)
bin/loggen --target 127.0.0.1 --port 514 --protocol udp --rate 1000 --duration 30s
```

If a problem is not covered here, collect `/ready`, the last 100 log lines
and the output of `/metrics`, and open an issue.
