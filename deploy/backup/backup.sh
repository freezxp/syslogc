#!/usr/bin/env bash
# Backs up Syslogc state: the PostgreSQL metadata database, the secrets
# volume and (optionally) the VictoriaLogs data.
#
#   deploy/backup/backup.sh /var/backups/syslogc
#   INCLUDE_LOGS=1 deploy/backup/backup.sh /var/backups/syslogc
#
# Metadata is small and irreplaceable (users, API keys, saved searches,
# audit log, sources); log data is large and often acceptable to lose, so it
# is skipped unless INCLUDE_LOGS=1. Run from the repository directory, or
# set COMPOSE to a full `docker compose -f ...` command.
set -euo pipefail

DEST="${1:-}"
[[ -n "$DEST" ]] || { echo "usage: $0 <destination-directory>" >&2; exit 2; }
COMPOSE="${COMPOSE:-docker compose}"
DOCKER="${DOCKER:-docker}"
PROJECT="${COMPOSE_PROJECT_NAME:-syslogc}"
HELPER="${HELPER_IMAGE:-busybox:1.37}"
INCLUDE_LOGS="${INCLUDE_LOGS:-0}"
KEEP_DAYS="${KEEP_DAYS:-14}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="$DEST/$STAMP"

log() { printf '[backup] %s\n' "$*"; }

mkdir -p "$OUT"

# 1. PostgreSQL metadata (consistent dump; no downtime).
log "dumping PostgreSQL"
$COMPOSE exec -T postgres pg_dump -U syslogc --format=custom syslogc > "$OUT/metadata.dump"

# 2. Secrets (losing the signing key invalidates sessions and cursors).
log "copying secrets"
$DOCKER run --rm -v "${PROJECT}_secrets:/secrets:ro" -v "$OUT:/backup" "$HELPER" \
  tar -C /secrets -cf /backup/secrets.tar .

# 3. VictoriaLogs data. It must be stopped for a consistent copy: the
#    snapshot API is not available in the open-source single-node image.
if [[ "$INCLUDE_LOGS" == "1" ]]; then
  log "stopping victorialogs for a consistent copy"
  $COMPOSE stop victorialogs
  trap '$COMPOSE start victorialogs' EXIT
  $DOCKER run --rm -v "${PROJECT}_vlogs-data:/vlogs:ro" -v "$OUT:/backup" "$HELPER" \
    tar -C /vlogs -czf /backup/vlogs.tar.gz .
  $COMPOSE start victorialogs
  trap - EXIT
  log "victorialogs restarted"
else
  log "skipping log data (set INCLUDE_LOGS=1 to include it)"
fi

sha256sum "$OUT"/* > "$OUT/SHA256SUMS"
log "wrote $OUT ($(du -sh "$OUT" | cut -f1))"

# 4. Prune old backups.
if [[ "$KEEP_DAYS" -gt 0 ]]; then
  find "$DEST" -mindepth 1 -maxdepth 1 -type d -mtime "+$KEEP_DAYS" -print -exec rm -rf {} + |
    while read -r old; do log "pruned $old"; done
fi
log "done"
