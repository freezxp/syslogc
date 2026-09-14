#!/usr/bin/env bash
# Phase 1 end-to-end smoke test against a running Docker Compose stack.
#
#   docker compose up -d --build
#   backend/tests/e2e/phase1-smoke.sh
#
# Sends syslog with util-linux `logger` exactly as in the Definition of Done
# and verifies the messages are stored, normalized and searchable.
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
SYSLOG_HOST="${SYSLOG_HOST:-127.0.0.1}"
SYSLOG_PORT="${SYSLOG_PORT:-514}"
READY_TIMEOUT="${READY_TIMEOUT:-120}"
RUN="smoke$(date +%s)$RANDOM"

log() { printf '[e2e] %s\n' "$*"; }
fail() { printf '[e2e] FAIL: %s\n' "$*" >&2; exit 1; }

command -v logger >/dev/null || fail "util-linux logger is required"
command -v python3 >/dev/null || fail "python3 is required"

log "waiting for ${BASE_URL}/ready (up to ${READY_TIMEOUT}s)"
deadline=$((SECONDS + READY_TIMEOUT))
until curl -fsS "${BASE_URL}/ready" >/dev/null 2>&1; do
  if (( SECONDS > deadline )); then
    curl -sS "${BASE_URL}/ready" || true
    fail "syslogc not ready"
  fi
  sleep 2
done
log "ready"

log "sending messages (run ${RUN})"
logger --server "$SYSLOG_HOST" --udp --port "$SYSLOG_PORT" "Test syslog message ${RUN} udp"
logger --server "$SYSLOG_HOST" --tcp --port "$SYSLOG_PORT" "Test syslog message ${RUN} tcp"
logger --server "$SYSLOG_HOST" --udp --port "$SYSLOG_PORT" --rfc3164 -p local4.err -t vpnd "Test syslog message ${RUN} bsd"

deadline=$((SECONDS + 30))
while :; do
  body="$(curl -fsS "${BASE_URL}/api/v1/dev/search?query=${RUN}&from=15m")" || fail "search request failed"
  count="$(printf '%s' "$body" | python3 -c 'import sys,json; print(json.load(sys.stdin)["meta"]["returned"])')"
  [[ "$count" == "3" ]] && break
  (( SECONDS > deadline )) && fail "expected 3 stored messages, found ${count}: ${body}"
  sleep 1
done
log "found 3 messages"

printf '%s' "$body" | RUN="$RUN" python3 -c '
import json, os, sys
rows = {r["_msg"].rsplit(" ", 1)[-1]: r for r in json.load(sys.stdin)["rows"]}
run = os.environ["RUN"]
def check(kind, **expected):
    row = rows.get(kind)
    if row is None:
        sys.exit(f"missing {kind} message")
    msg = row["_msg"]
    if msg != f"Test syslog message {run} {kind}":
        sys.exit(f"{kind}: message not parsed cleanly: {msg!r}")
    for k, v in expected.items():
        if row.get(k) != v:
            sys.exit(f"{kind}: {k}={row.get(k)!r}, want {v!r}")
    for k in ("hostname", "source_ip", "received_at", "raw_message"):
        if not row.get(k):
            sys.exit(f"{kind}: missing {k}")
check("udp", format="rfc5424", protocol="udp", source="syslog-udp", facility="user", severity="notice")
check("tcp", format="rfc5424", protocol="tcp", source="syslog-tcp")
check("bsd", format="rfc3164", facility="local4", severity="error", priority="163", app_name="vpnd")
print("[e2e] fields verified")
'

metrics="$(curl -fsS "${BASE_URL}/metrics")"
grep -q '^syslogc_storage_healthy{backend="victorialogs"} 1' <<<"$metrics" || fail "storage not healthy in metrics"
grep -Eq '^syslogc_ingest_messages_stored_total\{source="syslog-udp"\} [1-9]' <<<"$metrics" || fail "no stored UDP messages in metrics"
log "metrics verified"
log "PASS"
