#!/usr/bin/env bash
# One-command install of the nightly-backup machinery: nightly dump (02:00),
# weekly restore-verification (Sun 03:00), and daily audit-chain verification
# (04:00). Rewrites the unit templates in ops/systemd/ to point at THIS
# checkout, then installs and starts the timers.
#
#   make backups-install            # this repo, system-wide if root else --user
#   ops/install-backup-timers.sh    # same
#
# Root installs to /etc/systemd/system (survives logout); a non-root install
# uses `systemctl --user` (needs `loginctl enable-linger $USER` to run while
# logged out — the script reminds you).
set -euo pipefail

cd "$(dirname "$0")/.."
REPO="$(pwd)"

if [[ $EUID -eq 0 ]]; then
  DEST=/etc/systemd/system
  SCTL=(systemctl)
else
  DEST="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
  SCTL=(systemctl --user)
  mkdir -p "$DEST"
fi

UNITS=(quorum-backup quorum-backup-verify quorum-verify-audit)
for u in "${UNITS[@]}"; do
  # Point the units at this checkout instead of the /opt/quorum default. The
  # verify-audit unit needs the built binary; backup units need the scripts.
  sed -e "s#/opt/quorum#${REPO}#g" "ops/systemd/${u}.service" > "${DEST}/${u}.service"
  cp "ops/systemd/${u}.timer" "${DEST}/${u}.timer"
done

# The audit-chain check needs the compiled binary next to the units' paths.
if [[ ! -x "${REPO}/quorum" ]]; then
  echo "note: ${REPO}/quorum not built — quorum-verify-audit will fail until you run 'make build'."
fi
if [[ ! -f "${REPO}/.env" ]]; then
  echo "note: ${REPO}/.env missing — units read their config from it."
fi

"${SCTL[@]}" daemon-reload
for u in "${UNITS[@]}"; do
  "${SCTL[@]}" enable --now "${u}.timer"
done

echo
echo "Installed and started:"
"${SCTL[@]}" list-timers --no-pager | grep -E 'quorum|NEXT' || true
if [[ $EUID -ne 0 ]]; then
  echo
  echo "User-level install: run 'loginctl enable-linger $USER' so timers fire while you are logged out."
fi
echo
echo "Backups land in ${REPO}/backups/ with a .manifest (sha256 + audit chain head)."
echo "SHIP THEM OFF-BOX — a backup on the database's own disk does not survive that disk. See BACKUP.md."
