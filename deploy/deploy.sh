#!/bin/bash
# Build hostkit for Linux and install or update it on a server over SSH.
#   SSH_HOST=myserver bash deploy/deploy.sh
# The first install also needs: a `hostkit` user, /srv/hostkit, the systemd unit
# and the web server config (see README.md → Install). This script only replaces
# the binary and the default theme, keeps all data, and restarts the service.
set -euo pipefail
cd "$(dirname "$0")/.."
: "${SSH_HOST:?set SSH_HOST (an ssh alias of the server)}"
ROOT="${ROOT:-/srv/hostkit}"
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"

echo "== test"
go test ./...
echo "== build $VERSION"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /tmp/hostkit-linux ./cmd/hostkit
tar czf /tmp/hostkit-default-theme.tgz -C themes default

echo "== upload"
scp -q /tmp/hostkit-linux /tmp/hostkit-default-theme.tgz "$SSH_HOST:/tmp/"
ssh "$SSH_HOST" ROOT="$ROOT" bash -s <<'REMOTE'
set -euo pipefail
install -o hostkit -g hostkit -m 755 /tmp/hostkit-linux "$ROOT/hostkit.new"
mv "$ROOT/hostkit.new" "$ROOT/hostkit"
mkdir -p "$ROOT/themes"
rm -rf "$ROOT/themes/default"
tar xzf /tmp/hostkit-default-theme.tgz -C "$ROOT/themes"
chown -R hostkit:hostkit "$ROOT/themes/default"
rm -f /tmp/hostkit-linux /tmp/hostkit-default-theme.tgz
systemctl restart hostkit
sleep 1
systemctl is-active hostkit
"$ROOT/hostkit" version
REMOTE
