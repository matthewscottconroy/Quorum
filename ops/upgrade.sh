#!/usr/bin/env bash
# Upgrade the running deployment from GitHub, ON the server itself.
# The from-a-workstation alternative is ops/deploy.sh (needs Go locally).
#
#   sudo ops/upgrade.sh              # upgrade to the newest origin/main
#   sudo ops/upgrade.sh <tag|sha>    # upgrade (or roll back) to a specific ref
#
# What it does, in order: fetch from GitHub, show the incoming commits, take
# a database backup, fast-forward the checkout, build, swap the binary
# (keeping the old one as quorum.prev), restart, and poll /readyz for 30 s —
# restoring the previous binary automatically if the new one never gets
# ready. Migrations are embedded and apply themselves at startup, so the
# deploy IS the migration; the pre-upgrade backup is the undo button.
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "upgrade.sh: run with sudo (it restarts the service)" >&2
    exit 1
fi

DIR=/opt/quorum
REF="${1:-origin/main}"
cd "$DIR"

# git and the build run as the files' owner; only install/restart run as root.
as_owner() { sudo -u quorum "$@"; }

echo "==> fetching from GitHub"
as_owner git fetch --tags --quiet origin

CUR=$(as_owner git rev-parse --short HEAD)
NEW=$(as_owner git rev-parse --short "$REF")
if [ "$CUR" = "$NEW" ]; then
    echo "already at $CUR — nothing to do"
    exit 0
fi
echo "==> upgrading $CUR -> $NEW"
as_owner git log --oneline "HEAD..$REF" | head -20 || true

echo "==> pre-upgrade database backup"
scripts/backup.sh create

echo "==> updating source"
if [ "$REF" = "origin/main" ]; then
    as_owner git checkout --quiet main
    as_owner git merge --quiet --ff-only origin/main
else
    # A specific tag/commit: detached checkout is fine for a deploy dir.
    as_owner git checkout --quiet --detach "$REF"
fi

echo "==> building"
as_owner go build -o quorum.next ./cmd/quorum

[ -f quorum ] && cp -p quorum quorum.prev
mv -f quorum.next quorum
chown quorum:quorum quorum && chmod 755 quorum
# go build renames the binary out of its cache, which carries the cache's
# SELinux label along; on enforcing systems (Rocky) systemd then refuses to
# exec it (203/EXEC "Permission denied"). Restore the directory's label.
command -v restorecon >/dev/null 2>&1 && restorecon quorum || true

echo "==> restarting quorum.service"
systemctl restart quorum
for _ in $(seq 1 30); do
    sleep 1
    if curl -fsS localhost:8080/readyz >/dev/null 2>&1; then
        echo "upgrade ok — running $NEW, /readyz is green"
        exit 0
    fi
done

echo "!! new version never became ready — restoring previous binary" >&2
journalctl -u quorum -n 25 --no-pager >&2 || true
if [ -f quorum.prev ]; then
    mv -f quorum.prev quorum
    systemctl restart quorum
    echo "!! previous binary restored. Source is still at $NEW;" >&2
    echo "!! if the failed version had applied a migration, see RUNBOOK.md" >&2
    echo "!! 'Roll back a bad migration' (a backup was taken just above)." >&2
fi
exit 1
