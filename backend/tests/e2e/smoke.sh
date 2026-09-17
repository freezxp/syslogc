#!/usr/bin/env bash
# End-to-end smoke test against a running Docker Compose stack; it walks the
# Definition of Done through the same APIs the web UI uses.
#
#   docker compose up -d --build
#   backend/tests/e2e/smoke.sh
#
# Credentials: ADMIN_PASSWORD, or the bootstrap password printed in the
# syslogc container logs. If the account still requires a password change,
# the script sets NEW_ADMIN_PASSWORD (random by default) and prints it.
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
SYSLOG_HOST="${SYSLOG_HOST:-127.0.0.1}"
SYSLOG_PORT="${SYSLOG_PORT:-514}"
READY_TIMEOUT="${READY_TIMEOUT:-180}"
ADMIN_USER="${ADMIN_USER:-admin}"
COMPOSE="${COMPOSE:-docker compose}"
RUN="smoke$(date +%s)$RANDOM"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
JAR="$WORK/cookies"

log() { printf '[e2e] %s\n' "$*"; }
fail() { printf '[e2e] FAIL: %s\n' "$*" >&2; exit 1; }
json() { python3 -c "import sys,json; d=json.load(sys.stdin); print($1)"; }

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

# ---- web UI -----------------------------------------------------------------
curl -fsS "${BASE_URL}/" | grep -q '<div id="root">' || fail "web UI index not served"
curl -fsS "${BASE_URL}/explorer" | grep -q '<div id="root">' || fail "SPA route fallback not served"
log "web UI served"

# ---- login --------------------------------------------------------------------
if [[ -z "${ADMIN_PASSWORD:-}" ]]; then
  ADMIN_PASSWORD="$($COMPOSE logs --no-color syslogc 2>/dev/null | sed -n 's/.*Password: \([^ ]*\).*/\1/p' | tail -1)"
  [[ -n "$ADMIN_PASSWORD" ]] || fail "set ADMIN_PASSWORD (bootstrap password not found in container logs)"
fi

# api METHOD PATH [JSON] — prints the body, fails on non-2xx.
api() {
  local method=$1 path=$2 data=${3:-}
  local args=(-sS -o "$WORK/body" -w '%{http_code}' -b "$JAR" -c "$JAR" -X "$method"
    -H "Origin: ${BASE_URL}" -H "X-CSRF-Token: ${CSRF:-}")
  [[ -n "$data" ]] && args+=(-H 'Content-Type: application/json' --data "$data")
  local code
  code="$(curl "${args[@]}" "${BASE_URL}${path}")"
  [[ "$code" == 2* ]] || fail "$method $path -> $code: $(cat "$WORK/body")"
  cat "$WORK/body"
}

login() {
  CSRF=""
  local body
  body="$(api POST /api/v1/auth/login "{\"username\":\"${ADMIN_USER}\",\"password\":\"$1\"}")"
  CSRF="$(json 'd["csrf_token"]' <<<"$body")"
  MUST_CHANGE="$(json 'd["user"]["must_change_password"]' <<<"$body")"
}
login "$ADMIN_PASSWORD"
if [[ "$MUST_CHANGE" == "True" ]]; then
  NEW_ADMIN_PASSWORD="${NEW_ADMIN_PASSWORD:-$(python3 -c 'import secrets; print(secrets.token_urlsafe(18))')}"
  api PUT /api/v1/auth/me/password "{\"current_password\":\"${ADMIN_PASSWORD}\",\"new_password\":\"${NEW_ADMIN_PASSWORD}\"}" >/dev/null
  login "$NEW_ADMIN_PASSWORD"
  log "bootstrap password changed; new admin password: ${NEW_ADMIN_PASSWORD}"
fi
log "logged in as ${ADMIN_USER}"

# ---- ingest via logger (Definition of Done) -----------------------------------
log "sending messages (run ${RUN})"
logger --server "$SYSLOG_HOST" --udp --port "$SYSLOG_PORT" "Test syslog message ${RUN} udp"
logger --server "$SYSLOG_HOST" --tcp --port "$SYSLOG_PORT" "Test syslog message ${RUN} tcp"
logger --server "$SYSLOG_HOST" --udp --port "$SYSLOG_PORT" --rfc3164 -p local4.err -t vpnd "Test syslog message ${RUN} bsd"

RANGE='{"from":"now-15m","to":"now+1m"}'
SEL="\"time_range\":${RANGE},\"filter\":{\"op\":\"text\",\"value\":\"${RUN}\"}"

deadline=$((SECONDS + 30))
while :; do
  body="$(api POST /api/v1/logs/search "{${SEL},\"limit\":50}")"
  count="$(json 'd["page"]["returned"]' <<<"$body")"
  [[ "$count" == "3" ]] && break
  (( SECONDS > deadline )) && fail "expected 3 stored messages, found ${count}: ${body}"
  sleep 1
done
log "search found 3 messages"

RUN="$RUN" python3 -c '
import json, os, sys
rows = {r["message"].rsplit(" ", 1)[-1]: r for r in json.load(sys.stdin)["rows"]}
run = os.environ["RUN"]
def check(kind, **expected):
    row = rows.get(kind)
    if row is None:
        sys.exit(f"missing {kind} message")
    msg = row["message"]
    if msg != f"Test syslog message {run} {kind}":
        sys.exit(f"{kind}: message not parsed cleanly: {msg!r}")
    for k, v in expected.items():
        if row.get(k) != v:
            sys.exit(f"{kind}: {k}={row.get(k)!r}, want {v!r}")
    for k in ("hostname", "source_ip", "received_at", "timestamp", "_ref"):
        if not row.get(k):
            sys.exit(f"{kind}: missing {k}")
    # Default raw_message policy is on_error: cleanly parsed logs keep no raw copy.
    if "raw_message" in row or "parse_error" in row:
        sys.exit(f"{kind}: unexpected raw_message/parse_error for a clean parse")
check("udp", format="rfc5424", protocol="udp", source="syslog-udp", facility="user", severity="notice")
check("tcp", format="rfc5424", protocol="tcp", source="syslog-tcp")
check("bsd", format="rfc3164", facility="local4", severity="error", priority=163, app_name="vpnd")
print("[e2e] fields verified")
' <<<"$body"

# ---- filters, fields, histogram, facets -----------------------------------------
body="$(api POST /api/v1/logs/search "{\"time_range\":${RANGE},\"filter\":{\"op\":\"and\",\"args\":[{\"op\":\"text\",\"value\":\"${RUN}\"},{\"op\":\"eq\",\"field\":\"severity\",\"value\":\"error\"}]}}")"
[[ "$(json 'd["page"]["returned"]' <<<"$body")" == "1" ]] || fail "severity filter: ${body}"
body="$(api POST /api/v1/fields "{${SEL}}")"
json '"\n".join(f["name"] for f in d["fields"])' <<<"$body" | grep -qx app_name || fail "field discovery: ${body}"
body="$(api POST /api/v1/logs/histogram "{${SEL},\"split_by\":\"severity\"}")"
[[ "$(json 'd["total"]' <<<"$body")" == "3" ]] || fail "histogram: ${body}"
body="$(api POST /api/v1/logs/facets "{${SEL},\"fields\":[\"protocol\"]}")"
grep -q '"tcp"' <<<"$body" || fail "facets: ${body}"
log "filters, fields, histogram and facets verified"

# ---- dashboard --------------------------------------------------------------------
api POST /api/v1/dashboard/overview "{\"time_range\":${RANGE}}" | grep -q '"logs_in_range"' || fail "dashboard overview"
api POST /api/v1/dashboard/volume "{\"time_range\":${RANGE}}" >/dev/null
api POST /api/v1/dashboard/top "{\"time_range\":${RANGE},\"field\":\"hostname\"}" >/dev/null
api POST /api/v1/dashboard/ingestion-rate "{\"time_range\":${RANGE}}" | grep -q '"series"' || fail "ingestion rate"
log "dashboard verified"

# ---- live tail ------------------------------------------------------------------------
TAIL_RUN="${RUN}tail"
q="$(printf '{"filter":{"op":"text","value":"%s"}}' "$TAIL_RUN" | base64 -w0 | tr '+/' '-_' | tr -d '=')"
curl -sS -N --max-time 15 -b "$JAR" "${BASE_URL}/api/v1/logs/tail?q=${q}" >"$WORK/tail" 2>/dev/null &
tail_pid=$!
sleep 2
for _ in 1 2 3; do
  logger --server "$SYSLOG_HOST" --udp --port "$SYSLOG_PORT" "Live tail ${TAIL_RUN}"
  sleep 1
done
for _ in $(seq 1 12); do
  grep -q "$TAIL_RUN" "$WORK/tail" && break
  sleep 1
done
kill "$tail_pid" 2>/dev/null || true
grep -q "$TAIL_RUN" "$WORK/tail" || fail "live tail delivered nothing: $(head -c 500 "$WORK/tail")"
log "live tail verified"

# ---- export ---------------------------------------------------------------------------
api POST "/api/v1/logs/export?format=csv" "{${SEL},\"fields\":[\"timestamp\",\"hostname\",\"message\"]}" >"$WORK/export.csv"
[[ "$(grep -c "$RUN" "$WORK/export.csv")" == "3" ]] || fail "CSV export: $(cat "$WORK/export.csv")"
log "export verified"

# ---- saved search -----------------------------------------------------------------------
body="$(api POST /api/v1/saved-searches "{\"name\":\"smoke ${RUN}\",\"query\":{\"filter\":{\"op\":\"text\",\"value\":\"${RUN}\"}},\"default_time_range\":${RANGE},\"columns\":[\"timestamp\",\"message\"]}")"
id="$(json 'd["id"]' <<<"$body")"
api GET /api/v1/saved-searches | grep -q "smoke ${RUN}" || fail "saved search not listed"
api DELETE "/api/v1/saved-searches/${id}" >/dev/null
log "saved search verified"

# ---- source management (Phase 5) ---------------------------------------------------------
port=$(python3 -c 'import socket;s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
body="$(api POST /api/v1/sources "{\"config\":{\"name\":\"smoke-${RUN}\",\"type\":\"syslog\",\"protocol\":\"udp\",\"address\":\"0.0.0.0:${port}\",\"labels\":{\"origin\":\"smoke\"}}}")"
src_id="$(json 'd["id"]' <<<"$body")"
src_version="$(json 'd["version"]' <<<"$body")"
# The listener is published inside the container network only, so verify the
# state the node reports rather than sending traffic to it.
running=false
for _ in $(seq 1 15); do
  api GET /api/v1/sources | grep -q "\"name\":\"smoke-${RUN}\".*\"state\":\"running\"" && { running=true; break; }
  grep -q "smoke-${RUN}" <<<"$(api GET /api/v1/system/health)" && grep -q '"state":"running"' <<<"$(api GET /api/v1/system/health)" && { running=true; break; }
  sleep 1
done
[[ "$running" == true ]] || fail "managed source did not start: $(api GET /api/v1/sources)"
api PUT "/api/v1/sources/${src_id}" "{\"config\":{\"name\":\"smoke-${RUN}\",\"type\":\"syslog\",\"protocol\":\"udp\",\"address\":\"0.0.0.0:${port}\"},\"enabled\":false,\"version\":${src_version}}" >/dev/null
api DELETE "/api/v1/sources/${src_id}" >/dev/null
log "source management verified"

# ---- users and audit log ------------------------------------------------------------------
body="$(api POST /api/v1/users "{\"username\":\"smoke-${RUN}\",\"role\":\"viewer\"}")"
user_id="$(json 'd["id"]' <<<"$body")"
[[ -n "$(json 'd.get("generated_password","")' <<<"$body")" ]] || fail "no generated password: ${body}"
api PUT "/api/v1/users/${user_id}" '{"disabled":true}' >/dev/null
api POST "/api/v1/users/${user_id}/revoke-sessions" >/dev/null
api DELETE "/api/v1/users/${user_id}" >/dev/null
body="$(api GET "/api/v1/audit?limit=200")"
for action in users.create users.delete sources.create sources.delete auth.login; do
  grep -q "\"action\":\"${action}\"" <<<"$body" || fail "audit log missing ${action}"
done
log "user administration and audit log verified"

# ---- retention and configuration ------------------------------------------------------------
api GET /api/v1/system/retention | grep -q '"instructions"' || fail "retention endpoint"
api GET /api/v1/system/config | grep -q '"yaml"' || fail "config endpoint"
api GET /api/v1/system/config | grep -q 'REDACTED' || log "note: no secrets present to redact"
log "retention and configuration verified"

# ---- system health and metrics ----------------------------------------------------------
body="$(api GET /api/v1/system/health)"
json 'd["status"]' <<<"$body" | grep -qx ready || fail "system health: ${body}"
metrics="$(curl -fsS "${BASE_URL}/metrics")"
grep -q '^syslogc_storage_healthy{backend="victorialogs"} 1' <<<"$metrics" || fail "storage not healthy in metrics"
grep -Eq '^syslogc_ingest_messages_stored_total\{source="syslog-udp"\} [1-9]' <<<"$metrics" || fail "no stored UDP messages in metrics"
log "health and metrics verified"
log "PASS"
