#!/usr/bin/env bash
# Idempotent repository bootstrap for the Oryxa Cloud Agent environment.
#
# Installs the container runtime the local dependency stack needs, warms the Go
# build cache, and creates a local .env for development. Safe to run repeatedly.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

cd "$(dirname "$0")/.."

# Docker + fuse-overlayfs. The local stack (Postgres, LocalStack, Moto, Mailpit)
# runs in containers, and nested Docker in the Cloud Agent VM cannot use the
# kernel overlay driver (whiteout files fail to extract), so it uses the
# fuse-overlayfs storage driver instead.
if ! command -v docker >/dev/null 2>&1 || ! command -v fuse-overlayfs >/dev/null 2>&1; then
  sudo apt-get update -y
  sudo apt-get install -y docker.io docker-compose-v2 fuse-overlayfs
fi
sudo mkdir -p /etc/docker
printf '{\n  "features": { "containerd-snapshotter": false },\n  "storage-driver": "fuse-overlayfs"\n}\n' \
  | sudo tee /etc/docker/daemon.json >/dev/null
sudo groupadd -f docker
sudo usermod -aG docker "$USER" || true

# Warm the Go module cache and confirm the one binary every service runs in
# compiles. The artifact itself is thrown away; the build only proves the tree.
go mod download
go build -o /tmp/oryxa-build ./cmd/oryxa
rm -f /tmp/oryxa-build

# A local .env for `go run ./cmd/oryxa`, with the emulator-friendly overrides the
# no-AWS-account services (teams, files, billing, email) need. Never clobber an
# existing .env a developer may have edited.
if [ ! -f .env ]; then
  cp .env.example .env
  cat >> .env <<'EOF'

# --- Cloud Agent development overrides (added by .cursor/install.sh) ---
# LocalStack and Moto accept any credentials; these keep the AWS SDK happy.
AWS_ACCESS_KEY_ID=test
AWS_SECRET_ACCESS_KEY=test
# teams and files call the auth service; point them at this same process. auth
# itself needs real Cognito and is not part of the default local service set.
AUTH_URL=http://localhost:8080
EOF
fi

echo "install.sh complete: docker + fuse-overlayfs configured, Go cache warmed, .env ready."
