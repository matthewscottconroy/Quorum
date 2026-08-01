#!/usr/bin/env bash
# One-command upgrade for the systemd deployment described in DEPLOY-EC2.md:
#
#   ops/deploy.sh deploy@app-host [/opt/quorum]
#
# Builds the fully static linux/amd64 binary locally (or in CI), ships it,
# swaps it atomically, restarts the service, and waits for /readyz — rolling
# back to the previous binary if the new one never becomes ready. Schema
# migrations are embedded and apply themselves at startup under an advisory
# lock, so a deploy IS the migration. Take a backup first when a release
# changes the schema (make backup); `quorum -migrate-down N` exists for the
# rare deliberate schema rollback.
set -euo pipefail

HOST="${1:?usage: ops/deploy.sh user@host [remote-dir]}"
DIR="${2:-/opt/quorum}"

TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
echo "==> building static linux/amd64 binary"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o "$TMP" ./cmd/quorum

echo "==> uploading to $HOST"
scp -q "$TMP" "$HOST:/tmp/quorum.new"

echo "==> swapping binary and restarting quorum.service"
ssh "$HOST" "sudo bash -s -- '$DIR'" <<'REMOTE'
set -euo pipefail
DIR="$1"
install -o quorum -g quorum -m 0755 /tmp/quorum.new "$DIR/quorum.next"
rm -f /tmp/quorum.new
[ -f "$DIR/quorum" ] && cp -p "$DIR/quorum" "$DIR/quorum.prev"
mv -f "$DIR/quorum.next" "$DIR/quorum"   # same filesystem: atomic
systemctl restart quorum
for _ in $(seq 1 30); do
    sleep 1
    if curl -fsS localhost:8080/readyz >/dev/null 2>&1; then
        echo "deploy ok - /readyz is green"
        exit 0
    fi
done
echo "!! new binary never became ready - rolling back" >&2
journalctl -u quorum -n 25 --no-pager >&2 || true
if [ -f "$DIR/quorum.prev" ]; then
    mv -f "$DIR/quorum.prev" "$DIR/quorum"
    systemctl restart quorum
fi
exit 1
REMOTE
