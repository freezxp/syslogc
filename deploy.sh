#!/usr/bin/env bash
# One-command deployment for Syslogc on Ubuntu.
#
#   ./deploy.sh                      # install prerequisites and start the stack
#   ./deploy.sh --domain logs.example.com --monitoring
#   ./deploy.sh --pull               # run the published image instead of building
#   ./deploy.sh --upgrade            # pull new commits, rebuild and restart
#   ./deploy.sh --status             # what is running, and where
#   ./deploy.sh --stop               # stop the stack (data is kept)
#
# Safe to re-run: it installs only what is missing and never overwrites
# settings you have already chosen in .env.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$REPO_DIR"

# ---- options ----------------------------------------------------------------
BIND="0.0.0.0"
PORT="8080"
RETENTION="30d"
RETENTION_SET=false
DOMAIN=""
PROXY_IP=""
MONITORING=false
FORWARD_URL=""
PULL=false
ACTION="deploy"
ASSUME_YES=false
OPEN_FIREWALL=true
BACKUP=true

usage() {
  sed -n '2,11p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  cat <<'EOF'

Options:
  --domain NAME        Public hostname served by a reverse proxy (sets the
                       allowed browser origins for CSRF checks).
  --proxy-ip ADDR      Reverse proxy address, so audit logs and login
                       throttling see real client IPs.
  --bind ADDR          Address to publish the web UI on (default 0.0.0.0).
  --port PORT          Host port for the web UI (default 8080).
  --retention PERIOD   How long logs are kept, e.g. 90d (default 30d).
  --monitoring         Also run Prometheus and Grafana.
  --forward URL        Mirror stored logs to another VictoriaLogs instance.
  --pull               Use the published image instead of building locally.
  --no-firewall        Do not open syslog and UI ports in ufw.
  --yes                Do not ask for confirmation.
  --upgrade            Fetch new commits, back up metadata, rebuild and
                       restart. Refuses to run with uncommitted changes.
  --no-backup          Skip the metadata backup an upgrade takes first.
  --status             Show what is running and exit.
  --stop               Stop the stack (data is kept) and exit.
  --help               This text.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) DOMAIN="${2:?--domain needs a hostname}"; shift 2 ;;
    --proxy-ip) PROXY_IP="${2:?--proxy-ip needs an address}"; shift 2 ;;
    --bind) BIND="${2:?--bind needs an address}"; shift 2 ;;
    --port) PORT="${2:?--port needs a port}"; shift 2 ;;
    --retention) RETENTION="${2:?--retention needs a period}"; RETENTION_SET=true; shift 2 ;;
    --monitoring) MONITORING=true; shift ;;
    --forward) FORWARD_URL="${2:?--forward needs a URL}"; shift 2 ;;
    --pull) PULL=true; shift ;;
    --no-firewall) OPEN_FIREWALL=false; shift ;;
    --yes|-y) ASSUME_YES=true; shift ;;
    --upgrade) ACTION="upgrade"; shift ;;
    --no-backup) BACKUP=false; shift ;;
    --status) ACTION="status"; shift ;;
    --stop) ACTION="stop"; shift ;;
    --help|-h) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

# ---- output -----------------------------------------------------------------
if [[ -t 1 ]]; then
  BOLD=$'\e[1m'; GREEN=$'\e[32m'; YELLOW=$'\e[33m'; RED=$'\e[31m'; RESET=$'\e[0m'
else
  BOLD=""; GREEN=""; YELLOW=""; RED=""; RESET=""
fi
step() { printf '%s==>%s %s\n' "$BOLD" "$RESET" "$*"; }
ok()   { printf '    %s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '    %s!%s %s\n' "$YELLOW" "$RESET" "$*"; }
die()  { printf '%serror:%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

# ---- privileges -------------------------------------------------------------
SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  command -v sudo >/dev/null || die "run as root, or install sudo"
  SUDO="sudo"
fi

# docker_compose runs compose with the overlays this deployment uses.
compose_files() {
  local files=(-f docker-compose.yml)
  [[ "$MONITORING" == true ]] && files+=(-f docker-compose.monitoring.yml)
  [[ -n "$FORWARD_URL" || -f .forwarding-enabled ]] && files+=(-f docker-compose.forwarding.yml)
  printf '%s\n' "${files[@]}"
}
docker_compose() {
  local files=()
  mapfile -t files < <(compose_files)
  $SUDO docker compose "${files[@]}" "$@"
}

# ---- actions that need no installation ---------------------------------------
case "$ACTION" in
  status)
    command -v docker >/dev/null || die "Docker is not installed; run ./deploy.sh first"
    docker_compose ps
    url="http://$(hostname -I | awk '{print $1}'):${PORT}"
    [[ -f .env ]] && url="http://$(hostname -I | awk '{print $1}'):$(grep -E '^SYSLOGC_HTTP_PORT=' .env | cut -d= -f2)"
    printf '\nWeb UI: %s\n' "$url"
    if curl -fsS "${url}/ready" >/dev/null 2>&1; then
      ok "ready"
    else
      warn "not ready yet"
    fi
    exit 0
    ;;
  upgrade)
    command -v docker >/dev/null || die "Docker is not installed; run ./deploy.sh first"
    command -v git >/dev/null || die "git is required to upgrade"
    git rev-parse --git-dir >/dev/null 2>&1 || die "not a git checkout; upgrade by replacing the files yourself"
    step "Upgrading"
    if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
      git status --short --untracked-files=no
      die "there are uncommitted changes; commit or stash them, then upgrade"
    fi
    before="$(git rev-parse --short HEAD)"
    git fetch --quiet || die "could not reach the git remote"
    git merge --ff-only --quiet "@{u}" || die "the local branch has diverged from its remote; resolve that first"
    after="$(git rev-parse --short HEAD)"
    if [[ "$before" == "$after" ]]; then
      # The checkout can already be current — someone pulled by hand — while
      # the containers still run the previous build, so compare against what
      # was last deployed rather than against the fetch.
      deployed="$(cat .deployed-commit 2>/dev/null || true)"
      if [[ "$deployed" == "$after" ]]; then
        ok "already at $after and running it; nothing to upgrade"
        exit 0
      fi
      ok "already at $after, but the running containers are from ${deployed:-an unknown commit}; redeploying"
    else
      ok "$before → $after"
    fi
    git --no-pager log --oneline --no-decorate "$before..$after" | head -10 | sed 's/^/      /'

    # Migrations run at startup, so take the cheap backup first.
    if [[ "$BACKUP" == true && -x deploy/backup/backup.sh ]]; then
      step "Backing up metadata"
      COMPOSE="$SUDO docker compose" DOCKER="$SUDO docker" deploy/backup/backup.sh ./backups |
        sed -n 's/^\[backup\] wrote /    ✓ /p'
    fi
    ;;
  stop)
    command -v docker >/dev/null || die "Docker is not installed"
    step "Stopping Syslogc (data is kept)"
    docker_compose stop
    ok "stopped; start again with ./deploy.sh"
    exit 0
    ;;
esac

# ---- 1. check the platform ----------------------------------------------------
step "Checking the system"
[[ -r /etc/os-release ]] || die "cannot read /etc/os-release; this script supports Ubuntu"
# shellcheck disable=SC1091
. /etc/os-release
case "${ID:-}:${ID_LIKE:-}" in
  ubuntu:*|*:*ubuntu*|debian:*|*:*debian*) ;;
  *) die "unsupported distribution ${PRETTY_NAME:-unknown}; this script supports Ubuntu (and Debian)" ;;
esac
if [[ "${ID:-}" == "ubuntu" ]]; then
  case "${VERSION_ID:-}" in
    22.04|24.04|25.04|26.04) ;;
    *) warn "untested Ubuntu ${VERSION_ID:-unknown}; continuing" ;;
  esac
fi
ok "${PRETTY_NAME:-Linux} on $(uname -m)"

mem_gb=$(awk '/MemTotal/ {printf "%.1f", $2/1048576}' /proc/meminfo)
disk_gb=$(df -BG --output=avail . | tail -1 | tr -dc '0-9')
awk "BEGIN{exit !($mem_gb < 1.8)}" && warn "only ${mem_gb} GiB RAM; 2 GiB or more is recommended"
[[ "$disk_gb" -lt 10 ]] && warn "only ${disk_gb} GiB free here; logs grow with retention"
ok "${mem_gb} GiB RAM, ${disk_gb} GiB free disk"

# ---- 2. install prerequisites --------------------------------------------------
missing=()
command -v docker >/dev/null || missing+=(docker.io)
docker compose version >/dev/null 2>&1 || missing+=(docker-compose-v2)
$SUDO docker buildx version >/dev/null 2>&1 || missing+=(docker-buildx)
command -v curl >/dev/null || missing+=(curl)

if [[ ${#missing[@]} -gt 0 ]]; then
  step "Installing ${missing[*]}"
  if [[ "$ASSUME_YES" != true && -t 0 ]]; then
    read -rp "    Install these packages with apt? [Y/n] " answer
    [[ -z "$answer" || "$answer" == [yY]* ]] || die "nothing installed"
  fi
  export DEBIAN_FRONTEND=noninteractive
  $SUDO apt-get update -qq
  $SUDO apt-get install -y -qq "${missing[@]}" >/dev/null
  ok "installed ${missing[*]}"
else
  step "Prerequisites"
  ok "docker $(docker --version | awk '{print $3}' | tr -d ,), compose $(docker compose version --short)"
fi

if command -v systemctl >/dev/null && [[ -d /run/systemd/system ]]; then
  $SUDO systemctl enable --now docker >/dev/null 2>&1 || true
fi
$SUDO docker info >/dev/null 2>&1 || die "Docker is installed but not running; check: systemctl status docker"

# Let the invoking user run docker without sudo from the next login.
if [[ -n "$SUDO" ]] && ! id -nG "$USER" | grep -qw docker; then
  $SUDO usermod -aG docker "$USER"
  warn "added $USER to the docker group; log out and back in to use docker without sudo"
fi

# ---- 3. kernel tuning ----------------------------------------------------------
# Syslog over UDP arrives in bursts; the default 208 KiB socket buffer silently
# drops datagrams before Syslogc can read them (see docs/performance.md).
if [[ "$(sysctl -n net.core.rmem_max)" -lt 33554432 ]]; then
  step "Tuning UDP receive buffers"
  printf 'net.core.rmem_max = 134217728\nnet.core.netdev_max_backlog = 250000\n' |
    $SUDO tee /etc/sysctl.d/99-syslogc.conf >/dev/null
  $SUDO sysctl -q --system
  ok "net.core.rmem_max = $(sysctl -n net.core.rmem_max)"
fi

# ---- 4. settings ----------------------------------------------------------------
step "Writing settings"
# set_env writes KEY=VALUE only when the key is absent, so re-running never
# overwrites a choice made earlier or by hand.
set_env() {
  local key="$1" value="$2"
  if grep -qE "^${key}=" .env 2>/dev/null; then
    return
  fi
  printf '%s=%s\n' "$key" "$value" >> .env
}
# force_env replaces a value, for settings that must win over what is there.
force_env() {
  sed -i "/^$1=/d" .env
  printf '%s=%s\n' "$1" "$2" >> .env
}

[[ -f .env ]] || printf '# Syslogc deployment settings (written by deploy.sh).\n' > .env

# Retention lives in three places that must agree: the storage backend's
# start-up flag, Syslogc's configuration, and what an administrator set in
# the UI. The flag wins when given; otherwise the stored setting is applied,
# so a change made in the UI takes effect on this restart.
# psql_settings runs a statement against the metadata database, quietly.
psql_settings() {
  $SUDO docker compose exec -T postgres psql -U syslogc -tAc "$1" 2>/dev/null
}

if [[ "$RETENTION_SET" == true ]]; then
  force_env SYSLOGC_RETENTION "$RETENTION"
  # The stored setting is what the server adopts at startup, so the flag
  # updates it too; otherwise the backend and the server would disagree.
  if psql_settings "insert into settings (key, value) values ('retention', jsonb_build_object('period', '$RETENTION'))
      on conflict (key) do update set value = excluded.value, updated_at = now()" >/dev/null; then
    ok "retention $RETENTION (also saved as the setting shown in the web UI)"
  fi
elif stored="$(psql_settings "select value->>'period' from settings where key = 'retention'" | tr -d '[:space:]')" &&
  [[ -n "$stored" ]]; then
  if ! grep -qx "SYSLOGC_RETENTION=$stored" .env; then
    force_env SYSLOGC_RETENTION "$stored"
    ok "applying retention $stored set in the web UI"
  fi
fi

set_env SYSLOGC_HTTP_BIND "$BIND"
set_env SYSLOGC_HTTP_PORT "$PORT"
set_env SYSLOGC_RETENTION "$RETENTION"
set_env SYSLOGC_AUTH_COOKIE_SECURE false
set_env SYSLOGC_NODE_ID "$(hostname -s)"
[[ -n "$DOMAIN" ]] && set_env SYSLOGC_ALLOWED_ORIGINS "https://${DOMAIN},http://${DOMAIN}"
[[ -n "$PROXY_IP" ]] && set_env SYSLOGC_TRUSTED_PROXIES "$PROXY_IP"
[[ "$PULL" == true ]] && set_env SYSLOGC_VERSION latest
ok "$(grep -c '^[A-Z]' .env) settings in .env"

if [[ -n "$FORWARD_URL" ]]; then
  # The edited copy is untracked, so `git pull` during an upgrade is not
  # blocked by a locally modified file.
  python3 - "$FORWARD_URL" <<'PY'
import re, sys
url = sys.argv[1]
src, dst = "deploy/compose/syslogc-forwarding.yaml", "deploy/compose/syslogc-forwarding.local.yaml"
text = open(src).read()
text = re.sub(r"(\n\s+url:\s*)\S+", r"\g<1>" + url, text, count=1)
open(dst, "w").write(text)
PY
  sed -i '/^SYSLOGC_FORWARD_CONFIG=/d' .env
  printf 'SYSLOGC_FORWARD_CONFIG=%s\n' "./deploy/compose/syslogc-forwarding.local.yaml" >> .env
  touch .forwarding-enabled
  ok "forwarding a copy of every stored log to $FORWARD_URL"
fi

# ---- 5. firewall -----------------------------------------------------------------
if [[ "$OPEN_FIREWALL" == true ]] && command -v ufw >/dev/null && $SUDO ufw status 2>/dev/null | grep -q "Status: active"; then
  step "Opening ports in ufw"
  for rule in "514/udp" "514/tcp" "${PORT}/tcp"; do
    $SUDO ufw allow "$rule" >/dev/null && ok "allowed $rule"
  done
fi

# ---- 6. start ----------------------------------------------------------------------
# Registry hiccups are common, and a quiet first attempt hides why: retry
# once with full output, then say what to check instead of exiting silently.
if [[ "$PULL" == true ]]; then
  step "Pulling images"
  if ! docker_compose pull -q; then
    warn "pull failed; retrying with full output"
    docker_compose pull ||
      die "could not pull the images. Check network access to ghcr.io, or omit --pull to build locally."
  fi
else
  step "Building the image (first run takes a few minutes)"
  if ! docker_compose build -q; then
    warn "build failed; retrying with full output"
    docker_compose build ||
      die "the image build failed. Check network access to the image registries (docker.io, ghcr.io) and disk space, then run ./deploy.sh again."
  fi
fi

step "Starting the stack"
docker_compose up -d --remove-orphans || die "the stack did not start; see: docker compose logs"

url="http://$(hostname -I | awk '{print $1}'):${PORT}"
printf '    waiting for %s/ready ' "$url"
ready=false
for _ in $(seq 1 90); do
  if curl -fsS "${url}/ready" >/dev/null 2>&1; then ready=true; break; fi
  printf '.'
  sleep 2
done
printf '\n'
if [[ "$ready" != true ]]; then
  docker_compose ps
  die "the stack did not become ready; see: docker compose logs syslogc"
fi
ok "ready"

# Record what is running, so --upgrade can tell a current checkout from a
# current deployment.
git rev-parse --short HEAD > .deployed-commit 2>/dev/null || true

# ---- 7. what to do next ----------------------------------------------------------
password="$(docker_compose logs --no-color syslogc 2>/dev/null | sed -n 's/.*Password: \([^ ]*\).*/\1/p' | tail -1)"
cat <<EOF

${BOLD}Syslogc is running.${RESET}

  Web UI     ${url}
  Username   admin
EOF
if [[ -n "$password" ]]; then
  printf '  Password   %s   (change it at first login)\n' "$password"
else
  printf '  Password   already set; recover it with an existing admin account\n'
fi
[[ -n "$DOMAIN" ]] && printf '  Proxy      point %s at %s, then set SYSLOGC_AUTH_COOKIE_SECURE=true in .env\n' "$DOMAIN" "$url"
[[ "$MONITORING" == true ]] && printf '  Grafana    http://%s:3000 (admin / %s)\n' "$(hostname -I | awk '{print $1}')" "${GRAFANA_PASSWORD:-admin}"

cat <<EOF

  Send a test log:   logger --server $(hostname -I | awk '{print $1}') --udp --port 514 "Test syslog message"
  Point devices at:  514/udp and 514/tcp on this host
  Status:            ./deploy.sh --status
  Stop:              ./deploy.sh --stop
  Docs:              docs/installation.md, docs/operations.md
EOF
