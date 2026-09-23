#!/usr/bin/env bash
# Checks, link by link, whether DNS service trends are working.
#
#   ./scripts/check-dns-trends.sh [BASE_URL]
#
# BASE_URL defaults to http://127.0.0.1:8080. The admin password is read from
# $SYSLOGC_ADMIN_PASSWORD, or asked for.
#
# Each step prints what it found, so a failure says which link is broken
# rather than only that the chart is empty.
set -uo pipefail

BASE="${1:-http://127.0.0.1:8080}"
JAR="$(mktemp)"
trap 'rm -f "$JAR"' EXIT

if [[ -t 1 ]]; then GREEN=$'\e[32m'; RED=$'\e[31m'; YELLOW=$'\e[33m'; BOLD=$'\e[1m'; RESET=$'\e[0m'
else GREEN=""; RED=""; YELLOW=""; BOLD=""; RESET=""; fi
ok()   { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
bad()  { printf '  %s✗%s %s\n' "$RED" "$RESET" "$*"; FAILED=1; }
warn() { printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$*"; }
step() { printf '\n%s%s%s\n' "$BOLD" "$*" "$RESET"; }
FAILED=0

password="${SYSLOGC_ADMIN_PASSWORD:-}"
if [[ -z "$password" ]]; then
  read -rsp "admin password: " password
  echo
fi

# --- sign in -----------------------------------------------------------------
login="$(curl -fsS -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' -H "Origin: $BASE" \
  -d "{\"username\":\"admin\",\"password\":$(printf '%s' "$password" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')}" 2>/dev/null)" || {
  printf '%scould not sign in to %s%s\n' "$RED" "$BASE" "$RESET" >&2; exit 1; }
CSRF="$(printf '%s' "$login" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("csrf_token",""))')"

api() { # api METHOD PATH [BODY]
  local method="$1" path="$2" body="${3:-}"
  if [[ -n "$body" ]]; then
    curl -fsS -b "$JAR" -X "$method" "$BASE$path" -H 'Content-Type: application/json' \
      -H "X-CSRF-Token: $CSRF" -H "Origin: $BASE" -d "$body"
  else
    curl -fsS -b "$JAR" -X "$method" "$BASE$path" -H "X-CSRF-Token: $CSRF" -H "Origin: $BASE"
  fi
}

# --- 1. is anything extracting the fields the rollup counts? -------------------
step "1. Extract rules"
api GET /api/v1/sources | python3 -c '
import sys, json
sources = json.load(sys.stdin)["sources"]
withrule = [s for s in sources if s["config"].get("extract")]
for s in sources:
    rules = [r.get("name") or "(unnamed)" for r in (s["config"].get("extract") or [])]
    suffix = ": " + ", ".join(rules) if rules else ""
    print("    %-16s %-9s %s extract rules%s" % (
        s["config"]["name"], s["origin"], "with" if rules else "without", suffix))
sys.exit(0 if withrule else 3)
' && ok "at least one source extracts fields" || bad "no source has an extract rule — add the dnsdist preset to the source receiving DNS logs"

# --- 2. do recent logs actually carry those fields? ---------------------------
step "2. Fields in recent logs"
fields="$(api POST /api/v1/fields '{"time_range":{"from":"now-30m","to":"now"}}' 2>/dev/null)"
printf '%s' "$fields" | python3 -c '
import sys, json
names = [f["name"] for f in json.load(sys.stdin).get("fields", [])]
dns = sorted(n for n in names if n.startswith("dns."))
print("    dns.* fields seen in the last 30 minutes:", ", ".join(dns) if dns else "none")
sys.exit(0 if {"dns.qname", "dns.client_ip"} <= set(dns) else 3)
' && ok "dns.qname and dns.client_ip are being stored" || bad "the fields the rollup counts are missing — check the rule is on the source receiving DNS logs, and that logs have arrived since you saved it"

# --- 3. is the catalog counting anything? -------------------------------------
step "3. Service catalog"
api GET /api/v1/analytics/services | python3 -c '
import sys, json
d = json.load(sys.stdin)
services = d.get("services", [])
on = [s for s in services if s.get("enabled")]
print("    %d of %d services enabled; recording: %s" % (len(on), len(services), d.get("recording")))
if d.get("problem"):
    print("    problem:", d["problem"])
sys.exit(0 if on and d.get("recording") else 3)
' && ok "the catalog has enabled services and the rollup is running" || bad "no enabled services, or no metrics store configured (check the victoriametrics container)"

# --- 4. has the rollup recorded anything? -------------------------------------
step "4. Recorded counts"
api POST /api/v1/analytics/service-trends \
  '{"time_range":{"from":"now-6h","to":"now"},"window":"5m"}' | python3 -c '
import sys, json
d = json.load(sys.stdin)
series = [s for s in d.get("series", []) if any(p["value"] > 0 for p in s["points"])]
if not series:
    print("    nothing recorded yet.", d.get("hint", ""))
    sys.exit(3)
for s in sorted(series, key=lambda x: -x["peak"])[:10]:
    print("    %-24s peak %6d clients at %s" % (
        s.get("label") or s["service"], int(s["peak"]), s["peak_at"][11:16]))
peak = d.get("peak")
if peak:
    print("\n    busiest: %s with %d clients at %s" % (
        peak.get("label") or peak["service"], int(peak["value"]), peak["at"][11:16]))
' && ok "counts are being recorded" || bad "no counts yet — the rollup records a window once it has fully elapsed, so allow one interval (5 minutes by default)"

# --- 5. is it still recording, right now? -------------------------------------
step "5. Freshness"
for w in 5m 1h 1d; do
  api POST /api/v1/analytics/service-trends \
    "{\"time_range\":{\"from\":\"now-3d\",\"to\":\"now\"},\"window\":\"$w\"}" |
    W="$w" python3 -c '
import sys, json, os, datetime
w = os.environ["W"]
d = json.load(sys.stdin)
latest = None
for s in d.get("series", []):
    for p in s["points"]:
        if p["value"] > 0 and (latest is None or p["at"] > latest):
            latest = p["at"]
if latest is None:
    print("    %-3s nothing recorded in the last three days" % w)
    sys.exit(3)
at = datetime.datetime.fromisoformat(latest.replace("Z", "+00:00"))
age = (datetime.datetime.now(datetime.timezone.utc) - at).total_seconds()
stale = {"5m": 900, "1h": 7200, "1d": 172800}[w]
print("    %-3s newest count %s (%d minutes ago)%s" % (
    w, at.strftime("%Y-%m-%d %H:%M UTC"), age / 60, "" if age < stale else "  <- stale"))
sys.exit(0 if age < stale else 3)
' || warn "the $w window is not keeping up — see the rollup log line below"
done
printf '    %s\n' "rollup log: docker compose logs syslogc | grep -i 'service trend' | tail"

# --- 6. is ingestion itself healthy? ------------------------------------------
step "6. Sources"
api GET /api/v1/system/ingestion | python3 -c '
import sys, json
for s in json.load(sys.stdin).get("sources", []):
    dropped = s.get("dropped") or {}
    total = sum(dropped.values())
    print("    %-14s %-5s received %-12d dropped %-8d parse errors %-8d connections %d" % (
        s["name"], s.get("protocol", ""), s.get("received", 0), total,
        s.get("parse_errors", 0), s.get("active_connections", 0)))
    if total:
        print("        dropped:", dropped)
'
ok "counters above: dropped should stay 0; one connection means one reader"

step "Result"
if [[ "$FAILED" == 0 ]]; then
  ok "DNS service trends are working"
else
  bad "see the failing step above"
  exit 1
fi
