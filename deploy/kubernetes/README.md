# Kubernetes

A Helm chart is planned for Phase 7. The application is already designed to
run on Kubernetes; these notes capture the constraints for whoever writes the
chart.

## Workloads

One image, two Deployments selected by role:

| Deployment | Args | Scales with | Probes |
|---|---|---|---|
| `syslogc-ingest` | `serve --node.roles=ingest` | ingest volume (CPU) | liveness `/health`, readiness `/ready` |
| `syslogc-api` | `serve --node.roles=api` | users / queries | liveness `/health`, readiness `/ready` |

Small installations can run a single Deployment with `--node.roles=all`.

- `terminationGracePeriodSeconds` ≥ `shutdown.drain_delay` + `shutdown.timeout` + 10 s (defaults: 45 s).
- Configuration: ConfigMap mounted at `/etc/syslogc/syslogc.yaml`; overrides and secrets via `SYSLOGC_*` env vars and `*_file` keys from Secrets.
- Security context: `runAsNonRoot`, UID 65532, `readOnlyRootFilesystem: true`, drop all capabilities. Listen on ports ≥ 1024 (e.g. 5514) and map 514 in the Service.
- Set `GOMEMLIMIT` to ~80 % of the container memory limit.

## Services

Syslog is not HTTP:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: syslogc-syslog
spec:
  type: LoadBalancer
  externalTrafficPolicy: Local   # preserve sender source IPs
  selector: {app: syslogc-ingest}
  ports:
    - {name: syslog-udp, protocol: UDP, port: 514, targetPort: 5514}
    - {name: syslog-tcp, protocol: TCP, port: 514, targetPort: 5514}
    - {name: syslog-tls, protocol: TCP, port: 6514, targetPort: 6514}
```

- `externalTrafficPolicy: Local` keeps source IPs but only routes to nodes running an ingest pod: use a DaemonSet or topology spread + enough replicas.
- TCP syslog connections are long-lived and do not rebalance when pods are added.
- UDP load balancing hashes on the 5-tuple: one busy device sticks to one pod.

## Storage

- VictoriaLogs: official Helm charts (single-node or cluster). Point `storage.victorialogs.insert_url` at `vlinsert` and `select_url` at `vlselect` in cluster mode.
- PostgreSQL (from Phase 2): CloudNativePG or a managed service.

## Monitoring

Scrape `/metrics` on port 8080 (PodMonitor/ServiceMonitor). Alert on
`syslogc_ingest_messages_dropped_total`, `syslogc_storage_reachable == 0`,
`syslogc_ingest_udp_kernel_drops_total` and queue saturation
(`syslogc_ingest_queue_bytes / syslogc_ingest_queue_capacity_bytes`).
