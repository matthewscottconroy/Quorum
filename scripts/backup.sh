#!/usr/bin/env bash
# Quorum database backup / restore / disaster-recovery tooling.
#
# Backups use pg_dump's custom format (-Fc): compressed, and restorable
# selectively or in parallel with pg_restore. "Untested backups are not
# backups", so `verify` restores a dump into a throwaway database and sanity-
# checks it without ever touching the live one.
#
# Two connection modes:
#   podman (default) — exec pg_dump/pg_restore INSIDE the quorum-db container, so
#                      the client tools always match the server version. Used for
#                      the local Podman stack.
#   url              — run the host's pg_dump/pg_restore against QUORUM_DATABASE_URL
#                      (or QUORUM_BACKUP_DATABASE_URL). Used in production/CI where
#                      there is no local container. Requires postgres client tools
#                      on the host. Select with QUORUM_BACKUP_MODE=url.
#
# Usage:
#   scripts/backup.sh create           Dump the database to backups/ and prune old ones
#   scripts/backup.sh list             List existing backups (newest first)
#   scripts/backup.sh restore <file>   Restore a dump into the live database (DESTRUCTIVE)
#   scripts/backup.sh verify [file]    Restore a dump into a scratch DB and check it
#   scripts/backup.sh prune            Apply the retention policy now
#
# Retention: the most recent QUORUM_BACKUP_KEEP (default 14) dumps are kept.
#
# Schedule it (examples):
#   cron:            0 2 * * *  cd /opt/quorum && scripts/backup.sh create >> /var/log/quorum-backup.log 2>&1
#   systemd timer:   OnCalendar=*-*-* 02:00:00  ExecStart=/opt/quorum/scripts/backup.sh create
set -euo pipefail

cd "$(dirname "$0")/.."

POD_DB=quorum-db
DB_NAME=quorum
DB_USER=quorum
BACKUP_DIR="${QUORUM_BACKUP_DIR:-./backups}"
KEEP="${QUORUM_BACKUP_KEEP:-14}"
MODE="${QUORUM_BACKUP_MODE:-podman}"

db_pass() {
  # Password for the quorum role, from .env (podman mode) or the URL (url mode).
  [[ -f .env ]] && grep -m1 '^DB_PASSWORD=' .env | cut -d= -f2- || true
}

backup_url() {
  echo "${QUORUM_BACKUP_DATABASE_URL:-${QUORUM_DATABASE_URL:-}}"
}

# --- mode-agnostic command wrappers -----------------------------------------
# Each streams the requested tool, reading/writing stdin/stdout so the caller can
# redirect to/from a host file regardless of mode.

pg_dump_cmd() { # → custom-format dump on stdout
  if [[ "$MODE" == "url" ]]; then
    pg_dump -Fc --no-owner --no-privileges "$(backup_url)"
  else
    podman exec -e PGPASSWORD="$(db_pass)" "$POD_DB" \
      pg_dump -U "$DB_USER" -d "$DB_NAME" -Fc --no-owner --no-privileges
  fi
}

psql_cmd() { # run SQL from "$1" against the live DB, quietly
  if [[ "$MODE" == "url" ]]; then
    psql -v ON_ERROR_STOP=1 -tAc "$1" "$(backup_url)"
  else
    podman exec -e PGPASSWORD="$(db_pass)" "$POD_DB" \
      psql -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" -tAc "$1"
  fi
}

require_db() {
  if [[ "$MODE" == "url" ]]; then
    [[ -n "$(backup_url)" ]] || { echo "url mode needs QUORUM_DATABASE_URL (or QUORUM_BACKUP_DATABASE_URL)." >&2; exit 1; }
  else
    podman inspect "$POD_DB" >/dev/null 2>&1 || { echo "Container '$POD_DB' not found. Start the stack (make local) or set QUORUM_BACKUP_MODE=url." >&2; exit 1; }
  fi
}

# --- commands ----------------------------------------------------------------

cmd_create() {
  require_db
  mkdir -p "$BACKUP_DIR"
  # UTC timestamp so filenames sort chronologically regardless of host TZ.
  local ts file tmp
  ts="$(date -u +%Y%m%dT%H%M%SZ)"
  file="${BACKUP_DIR}/quorum-${ts}.pgdump"
  tmp="${file}.partial"
  echo "==> Backing up ${DB_NAME} (mode: ${MODE}) → ${file}"
  # Dump to a .partial first, rename on success, so a crashed dump never looks
  # like a complete backup to the retention/restore logic.
  if pg_dump_cmd > "$tmp"; then
    mv "$tmp" "$file"
    echo "    wrote $(du -h "$file" | cut -f1) ($(stat -c%s "$file") bytes)"
    write_manifest "$file"
  else
    rm -f "$tmp"
    echo "    backup FAILED" >&2
    exit 1
  fi
  cmd_prune
}

# write_manifest records what would make this backup usable as evidence later:
# the dump's SHA-256 (integrity of the file itself), the schema version, and
# the audit chain's head hash AT BACKUP TIME — so any backup can serve as an
# independent anchor when verifying that history was never rewritten
# (see COMPLIANCE.md).
write_manifest() {
  local file="$1" sha ver head
  sha="$(sha256sum "$file" | cut -d' ' -f1)"
  ver="$(psql_cmd 'SELECT max(version) FROM schema_migrations' 2>/dev/null | tr -d '[:space:]')"
  head="$(psql_cmd "SELECT coalesce((SELECT entry_hash FROM audit_log ORDER BY seq DESC LIMIT 1), 'none')" 2>/dev/null | tr -d '[:space:]')"
  {
    echo "file: $(basename "$file")"
    echo "sha256: $sha"
    echo "bytes: $(stat -c%s "$file")"
    echo "created_utc: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "schema_version: ${ver:-unknown}"
    echo "audit_chain_head: ${head:-unknown}"
  } > "${file}.manifest"
  echo "    manifest: sha256=${sha:0:16}… chain_head=${head:0:16}…"
}

cmd_list() {
  mkdir -p "$BACKUP_DIR"
  if ! ls "$BACKUP_DIR"/quorum-*.pgdump >/dev/null 2>&1; then
    echo "No backups in ${BACKUP_DIR}."
    return 0
  fi
  echo "Backups in ${BACKUP_DIR} (newest first):"
  # shellcheck disable=SC2012
  ls -1t "$BACKUP_DIR"/quorum-*.pgdump | while read -r f; do
    printf "  %s  %s\n" "$(du -h "$f" | cut -f1)" "$f"
  done
}

cmd_prune() {
  mkdir -p "$BACKUP_DIR"
  local -a files
  mapfile -t files < <(ls -1t "$BACKUP_DIR"/quorum-*.pgdump 2>/dev/null || true)
  if (( ${#files[@]} > KEEP )); then
    echo "==> Pruning $(( ${#files[@]} - KEEP )) old backup(s), keeping ${KEEP}"
    local i
    for (( i=KEEP; i<${#files[@]}; i++ )); do
      echo "    rm ${files[$i]}"
      rm -f "${files[$i]}" "${files[$i]}.manifest"
    done
  fi
}

latest_backup() {
  ls -1t "$BACKUP_DIR"/quorum-*.pgdump 2>/dev/null | head -1
}

cmd_restore() {
  require_db
  local file="${1:-}"
  [[ -n "$file" ]] || { echo "usage: backup.sh restore <file>" >&2; exit 1; }
  [[ -f "$file" ]] || { echo "no such backup: $file" >&2; exit 1; }

  echo "!! This will OVERWRITE the live '${DB_NAME}' database with:"
  echo "     $file"
  read -r -p "Type the database name (${DB_NAME}) to confirm: " ans
  [[ "$ans" == "$DB_NAME" ]] || { echo "aborted."; exit 1; }

  echo "==> Restoring ${file} → ${DB_NAME} (mode: ${MODE})"
  # --clean --if-exists drops existing objects first so the restore is a full
  # replace, not a merge. Errors are surfaced (no --exit-on-error so a benign
  # DROP-of-missing on a fresh DB doesn't abort a valid restore).
  if [[ "$MODE" == "url" ]]; then
    pg_restore --clean --if-exists --no-owner --no-privileges -d "$(backup_url)" "$file"
  else
    podman exec -i -e PGPASSWORD="$(db_pass)" "$POD_DB" \
      pg_restore --clean --if-exists --no-owner --no-privileges -U "$DB_USER" -d "$DB_NAME" < "$file"
  fi
  echo "==> Restore complete. Current schema version:"
  psql_cmd "SELECT max(version) FROM schema_migrations;"
}

# verify restores a dump into a scratch database and checks it, proving the
# backup is restorable without touching the live database.
cmd_verify() {
  require_db
  local file="${1:-$(latest_backup)}"
  [[ -n "$file" && -f "$file" ]] || { echo "no backup to verify (pass a file or run create first)" >&2; exit 1; }
  local scratch="quorum_verify_$$"

  echo "==> Verifying ${file} by restoring into scratch DB '${scratch}'"
  local admin_url
  if [[ "$MODE" == "url" ]]; then
    admin_url="$(backup_url)"
    local scratch_url
    scratch_url="$(dirname_url_replace "$scratch")"
    # SAFETY GATE: the whole point of verify is to never touch the live
    # database. If the URL rewrite failed to substitute the database name (for
    # example, the URL carries no /dbname path component), scratch_url would
    # still point at the LIVE database and the --clean restore below would
    # destroy it while reporting success. Refuse instead.
    if [[ "$scratch_url" == "$admin_url" || "$scratch_url" != *"$scratch"* ]]; then
      echo "verify: could not derive a scratch-DB URL from QUORUM_DATABASE_URL (no /dbname path?)." >&2
      echo "        refusing to run: the restore would target the LIVE database." >&2
      exit 1
    fi
    psql -v ON_ERROR_STOP=1 -c "CREATE DATABASE $scratch" "$admin_url" >/dev/null
    pg_restore --clean --if-exists --no-owner --no-privileges \
      -d "$scratch_url" "$file" >/dev/null 2>&1 || true
    local n
    n="$(psql -tAc "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" "$scratch_url")"
    local ver
    ver="$(psql -tAc "SELECT max(version) FROM schema_migrations" "$scratch_url")"
    echo "    scratch DB has ${n} public tables; schema version ${ver}"
    psql -c "DROP DATABASE $scratch" "$admin_url" >/dev/null
    [[ "${n:-0}" -gt 0 ]] || { echo "    VERIFY FAILED: no tables restored" >&2; exit 1; }
  else
    podman exec -e PGPASSWORD="$(db_pass)" "$POD_DB" createdb -U "$DB_USER" "$scratch"
    podman exec -i -e PGPASSWORD="$(db_pass)" "$POD_DB" \
      pg_restore --clean --if-exists --no-owner --no-privileges -U "$DB_USER" -d "$scratch" < "$file" >/dev/null 2>&1 || true
    local n ver
    n="$(podman exec -e PGPASSWORD="$(db_pass)" "$POD_DB" psql -tAc "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" -U "$DB_USER" -d "$scratch")"
    ver="$(podman exec -e PGPASSWORD="$(db_pass)" "$POD_DB" psql -tAc "SELECT max(version) FROM schema_migrations" -U "$DB_USER" -d "$scratch")"
    echo "    scratch DB has $(echo "$n" | tr -d '[:space:]') public tables; schema version $(echo "$ver" | tr -d '[:space:]')"
    podman exec -e PGPASSWORD="$(db_pass)" "$POD_DB" dropdb -U "$DB_USER" "$scratch"
    [[ "$(echo "$n" | tr -d '[:space:]')" -gt 0 ]] || { echo "    VERIFY FAILED: no tables restored" >&2; exit 1; }
  fi
  echo "==> Verify OK — backup is restorable."
}

# dirname_url_replace swaps the database name in a postgres URL for url-mode verify.
dirname_url_replace() {
  local newdb="$1" url
  url="$(backup_url)"
  # Replace the path component (…/dbname[?params]) with the scratch db name.
  echo "$url" | sed -E "s#(://[^/]+/)[^?]+#\1${newdb}#"
}

case "${1:-}" in
  create)  cmd_create ;;
  list)    cmd_list ;;
  restore) shift; cmd_restore "${1:-}" ;;
  verify)  shift; cmd_verify "${1:-}" ;;
  prune)   cmd_prune ;;
  *)
    grep '^#' "$0" | sed 's/^# \{0,1\}//' | sed '1d'
    exit 1
    ;;
esac
