#!/usr/bin/env bash
# Local Quorum stack via plain Podman (no compose provider required).
#
# Runs Postgres 16 and the Quorum app inside a single Podman *pod*, so they
# share a network namespace and the app reaches the DB on localhost:5432 —
# exactly matching .env's QUORUM_DATABASE_URL. This mirrors what podman-compose
# does, but needs nothing beyond `podman` itself.
#
# Usage:
#   scripts/local.sh up       Build the image (if needed) and start the stack
#   scripts/local.sh down      Stop and remove the pod (DB volume is KEPT)
#   scripts/local.sh reset     Down + delete the DB volume (wipes all data)
#   scripts/local.sh restart   Rebuild the app image and restart just the app
#   scripts/local.sh logs      Follow the app logs
#   scripts/local.sh status    Show pod/container status
#   scripts/local.sh bootstrap Create the first admin user (interactive)
set -euo pipefail

cd "$(dirname "$0")/.."

POD=quorum
IMAGE=localhost/quorum:dev
VOLUME=quorum-pgdata
PORT=8080

need_env() {
  if [[ ! -f .env ]]; then
    echo "No .env found. Copy .env.example to .env and set QUORUM_JWT_SECRET (make secret) + DB_PASSWORD." >&2
    exit 1
  fi
}

db_pass() { grep '^DB_PASSWORD=' .env | cut -d= -f2-; }

wait_for_db() {
  echo -n "Waiting for Postgres"
  for _ in $(seq 1 30); do
    if podman exec ${POD}-db pg_isready -U quorum >/dev/null 2>&1; then echo " — ready."; return 0; fi
    echo -n "."; sleep 1
  done
  echo " — timed out." >&2; return 1
}

case "${1:-}" in
  up)
    need_env
    echo "==> Building ${IMAGE} (skips layers that haven't changed)…"
    podman build --format oci -t "${IMAGE}" . >/dev/null
    echo "==> (Re)creating pod '${POD}' on 127.0.0.1:${PORT}…"
    podman pod rm -f "${POD}" >/dev/null 2>&1 || true
    podman pod create --name "${POD}" -p "127.0.0.1:${PORT}:${PORT}" >/dev/null
    echo "==> Starting Postgres 16…"
    podman run -d --pod "${POD}" --name "${POD}-db" \
      -e POSTGRES_DB=quorum -e POSTGRES_USER=quorum -e POSTGRES_PASSWORD="$(db_pass)" \
      -v "${VOLUME}:/var/lib/postgresql/data" \
      postgres:16-alpine >/dev/null
    wait_for_db
    echo "==> Starting Quorum app (migrations run on startup)…"
    podman run -d --pod "${POD}" --name "${POD}-app" \
      --env-file .env --restart unless-stopped "${IMAGE}" >/dev/null
    sleep 3
    echo "==> Health check:"
    curl -fsS "http://localhost:${PORT}/readyz" && echo
    echo
    echo "Quorum is up at http://localhost:${PORT}"
    echo "If this is a fresh database, create the first admin: scripts/local.sh bootstrap"
    ;;
  down)
    echo "==> Removing pod '${POD}' (data volume '${VOLUME}' is kept)…"
    podman pod rm -f "${POD}" >/dev/null 2>&1 || true
    echo "Done. Run 'scripts/local.sh up' to start again (data preserved)."
    ;;
  reset)
    echo "==> Removing pod '${POD}' AND deleting data volume '${VOLUME}'…"
    podman pod rm -f "${POD}" >/dev/null 2>&1 || true
    podman volume rm "${VOLUME}" >/dev/null 2>&1 || true
    echo "Clean slate. Run 'scripts/local.sh up' then 'scripts/local.sh bootstrap'."
    ;;
  restart)
    need_env
    echo "==> Rebuilding image and restarting the app container only…"
    podman build --format oci -t "${IMAGE}" . >/dev/null
    podman rm -f "${POD}-app" >/dev/null 2>&1 || true
    podman run -d --pod "${POD}" --name "${POD}-app" \
      --env-file .env --restart unless-stopped "${IMAGE}" >/dev/null
    sleep 3
    curl -fsS "http://localhost:${PORT}/readyz" && echo
    ;;
  logs)
    podman logs -f "${POD}-app"
    ;;
  status)
    podman pod ps --filter name="${POD}"
    echo
    podman ps --filter pod="${POD}" --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}'
    ;;
  bootstrap)
    read -rp "Admin email: " email
    read -rsp "Admin password (min 10 chars): " pw; echo
    curl -s -X POST "http://localhost:${PORT}/api/v1/auth/bootstrap" \
      -H 'Content-Type: application/json' \
      -d "{\"email\":\"${email}\",\"password\":\"${pw}\"}" | python3 -m json.tool
    ;;
  *)
    grep '^#' "$0" | sed 's/^# \{0,1\}//' | sed '1d'
    exit 1
    ;;
esac
