#!/usr/bin/env bash
# Load-test harness: runs loggen profiles against a running stack and
# archives the generator report together with server-side counters.
#
#   backend/tests/load/run.sh steady            # one profile
#   backend/tests/load/run.sh steady burst soak # several, in order
#   PROFILES=list backend/tests/load/run.sh     # show the profiles
#
# Results land in tests/load/results/<timestamp>/<profile>.json with the
# achieved rate, losses, latency quantiles and bytes stored per log, so a
# claim can always be traced back to a run.
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
TARGET="${TARGET:-127.0.0.1}"
PORT="${PORT:-514}"
LOGGEN="${LOGGEN:-bin/loggen}"
OUT_ROOT="${OUT_ROOT:-backend/tests/load/results}"
RUN_STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="$OUT_ROOT/$RUN_STAMP"

log() { printf '[load] %s\n' "$*"; }
fail() { printf '[load] FAIL: %s\n' "$*" >&2; exit 1; }

# profile <name> → loggen arguments. Rates are per second; 0 means unthrottled.
profile_args() {
  case "$1" in
    smoke)   echo "--protocol udp --rate 1000 --duration 30s --connections 1 --hosts 20" ;;
    steady)  echo "--protocol tcp --rate 10000 --duration 120s --connections 4 --hosts 200 --custom-fields 2" ;;
    udp)     echo "--protocol udp --rate 10000 --duration 120s --connections 4 --hosts 200" ;;
    burst)   echo "--protocol tcp --rate 0 --count 2000000 --connections 8 --hosts 500" ;;
    soak)    echo "--protocol tcp --rate 5000 --duration 1800s --connections 4 --hosts 500 --custom-fields 3" ;;
    max)     echo "--protocol tcp --rate 0 --duration 60s --connections 8 --hosts 500" ;;
    *) fail "unknown profile $1 (smoke, steady, udp, burst, soak, max)" ;;
  esac
}

[[ "${PROFILES:-}" != "list" ]] || { echo "smoke steady udp burst soak max"; exit 0; }
[[ $# -gt 0 ]] || set -- steady
[[ -x "$LOGGEN" ]] || fail "$LOGGEN not found; run: make build"
command -v python3 >/dev/null || fail "python3 is required"

curl -fsS "$BASE_URL/ready" >/dev/null || fail "$BASE_URL is not ready"
mkdir -p "$OUT"

# metrics_snapshot writes the Prometheus text exposition to a file.
metrics_snapshot() { curl -fsS "$BASE_URL/metrics" > "$1"; }

for name in "$@"; do
  args="$(profile_args "$name")"
  run_id="load-$name-$(date +%s)"
  log "profile $name: $args"

  metrics_snapshot "$OUT/$name.before.prom"
  start=$(date +%s)
  # shellcheck disable=SC2086 # arguments are intentionally word-split
  "$LOGGEN" --target "$TARGET" --port "$PORT" --run-id "$run_id" --stats-interval 0 \
    --json-report "$OUT/$name.loggen.json" $args >/dev/null
  # Let the pipeline drain before reading the counters.
  sleep 10
  metrics_snapshot "$OUT/$name.after.prom"
  end=$(date +%s)

  RUN_ID="$run_id" PROFILE="$name" ELAPSED=$((end - start)) python3 - "$OUT/$name.loggen.json" \
    "$OUT/$name.before.prom" "$OUT/$name.after.prom" > "$OUT/$name.json" <<'PY'
import json, os, re, sys

report, before_path, after_path = sys.argv[1], sys.argv[2], sys.argv[3]
gen = json.load(open(report))

def parse(path):
    """Sums counters by metric name, and keeps histogram buckets."""
    totals, buckets = {}, {}
    for line in open(path):
        if line.startswith("#") or not line.strip():
            continue
        name, _, value = line.rpartition(" ")
        try:
            value = float(value)
        except ValueError:
            continue
        metric = name.split("{")[0]
        if metric.endswith("_bucket"):
            le = re.search(r'le="([^"]+)"', name)
            if le:
                buckets.setdefault(metric, {})
                buckets[metric][le.group(1)] = buckets[metric].get(le.group(1), 0) + value
            continue
        totals[metric] = totals.get(metric, 0.0) + value
    return totals, buckets

before, bbuckets = parse(before_path)
after, abuckets = parse(after_path)
delta = {k: after.get(k, 0) - before.get(k, 0) for k in after}

def quantile(q):
    """Upper bound of the bucket holding quantile q of the run's samples."""
    name = "syslogc_ingest_e2e_latency_seconds_bucket"
    a, b = abuckets.get(name, {}), bbuckets.get(name, {})
    points = sorted(((float("inf") if k == "+Inf" else float(k)), a[k] - b.get(k, 0)) for k in a)
    if not points or points[-1][1] <= 0:
        return None
    target = points[-1][1] * q
    for le, count in points:
        if count >= target:
            return le
    return None

sent = gen["sent"]
stored = delta.get("syslogc_ingest_messages_stored_total", 0)
received = delta.get("syslogc_ingest_messages_received_total", 0)
dropped = delta.get("syslogc_ingest_messages_dropped_total", 0)
bytes_stored = delta.get("syslogc_ingest_bytes_stored_total", 0)
out = {
    "profile": os.environ["PROFILE"],
    "run_id": os.environ["RUN_ID"],
    "generator": {k: gen[k] for k in ("protocol", "target_rate", "sent", "errors", "bytes",
                                      "duration_seconds", "achieved_rate", "generator_limited")},
    "server": {
        "received": received,
        "stored": stored,
        "dropped": dropped,
        "parse_errors": delta.get("syslogc_ingest_parse_errors_total", 0),
        "udp_kernel_drops": delta.get("syslogc_ingest_udp_kernel_drops_total", 0),
        "storage_write_errors": delta.get("syslogc_storage_write_errors_total", 0),
        "stored_per_second": round(stored / max(int(os.environ["ELAPSED"]), 1), 1),
        "bytes_stored_per_log": round(bytes_stored / stored, 1) if stored else None,
        "loss_percent": round(100 * (sent - stored) / sent, 3) if sent else None,
        "e2e_latency_p50_seconds": quantile(0.5),
        "e2e_latency_p99_seconds": quantile(0.99),
    },
}
print(json.dumps(out, indent=2))
PY
  python3 -c "
import json,sys
d=json.load(open('$OUT/$name.json'))
g,s=d['generator'],d['server']
print(f\"[load] {d['profile']}: sent {g['sent']:,} at {g['achieved_rate']:,.0f}/s, stored {s['stored']:,.0f}, \"
      f\"dropped {s['dropped']:,.0f}, loss {s['loss_percent']}%, p99 {s['e2e_latency_p99_seconds']}s\")"
done

log "results in $OUT"
