#!/usr/bin/env bash
# Restores a backup written by backup.sh into a stopped Compose stack.
#
#   deploy/backup/restore.sh /var/backups/syslogc/20260917T020000Z
#
# The stack is stopped, the metadata database is replaced, secrets are
# restored, and log data is restored when the backup contains it.
set -euo pipefail

SRC="${1:-}"
[[ -d "$SRC" ]] || { echo "usage: $0 <backup-directory>" >&2; exit 2; }
COMPOSE="${COMPOSE:-docker compose}"
DOCKER="${DOCKER:-docker}"
PROJECT="${COMPOSE_PROJECT_NAME:-syslogc}"
HELPER="${HELPER_IMAGE:-busybox:1.37}"

log() { printf '[restore] %s\n' "$*"; }

if [[ -f "$SRC/SHA256SUMS" ]]; then
  (cd "$SRC" && sha256sum --check --quiet --ignore-missing SHA256SUMS) || {
    echo "[restore] checksums do not match; refusing to restore" >&2
    exit 1
  }
fi

read -rp "This overwrites the current Syslogc data. Continue? [y/N] " answer
[[ "$answer" == [yY]* ]] || exit 1

log "stopping the server"
$COMPOSE stop syslogc

log "restoring secrets"
$DOCKER run --rm -v "${PROJECT}_secrets:/secrets" -v "$SRC:/backup:ro" "$HELPER" sh -c \
  'tar -C /secrets -xf /backup/secrets.tar && chown 65532:65532 /secrets/postgres_dsn /secrets/secret_key'

log "restoring the metadata database"
$COMPOSE up -d postgres
until $COMPOSE exec -T postgres pg_isready -U syslogc >/dev/null 2>&1; do sleep 1; done
$COMPOSE exec -T postgres psql -U syslogc -d postgres -c \
  "DROP DATABASE IF EXISTS syslogc WITH (FORCE); CREATE DATABASE syslogc OWNER syslogc;" >/dev/null
$COMPOSE exec -T postgres pg_restore -U syslogc --dbname syslogc --no-owner < "$SRC/metadata.dump"

if [[ -f "$SRC/vlogs.tar.gz" ]]; then
  log "restoring log data"
  $COMPOSE stop victorialogs
  $DOCKER run --rm -v "${PROJECT}_vlogs-data:/vlogs" -v "$SRC:/backup:ro" "$HELPER" sh -c \
    'rm -rf /vlogs/* && tar -C /vlogs -xzf /backup/vlogs.tar.gz'
fi

log "starting the stack"
$COMPOSE up -d
log "done; check http://127.0.0.1:8080/ready"
