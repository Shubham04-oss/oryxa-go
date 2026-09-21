#!/usr/bin/env bash
# Per-boot startup for the Oryxa Cloud Agent environment.
#
# Brings up the container runtime and the local dependency stack, then
# provisions the emulator resources the default local service set needs. Safe to
# run repeatedly: every step reconciles rather than duplicates.
set -euo pipefail

cd "$(dirname "$0")/.."

# 1. Docker daemon (nested; fuse-overlayfs storage driver). systemd is not the
#    init here, so the daemon is started directly and left running.
if ! docker info >/dev/null 2>&1; then
  sudo chmod 666 /var/run/docker.sock 2>/dev/null || true
  if ! docker info >/dev/null 2>&1; then
    sudo bash -c 'nohup dockerd >/var/log/dockerd.log 2>&1 &'
    for _ in $(seq 1 60); do
      sudo chmod 666 /var/run/docker.sock 2>/dev/null || true
      docker info >/dev/null 2>&1 && break
      sleep 1
    done
  fi
fi
sudo chmod 666 /var/run/docker.sock 2>/dev/null || true

# 2. Local dependencies: Postgres (pgvector), LocalStack (EventBridge/SQS/
#    DynamoDB/S3), Moto (SES), Mailpit (SMTP).
docker compose up -d

# 3. Wait until every dependency reports healthy.
for _ in $(seq 1 90); do
  pending="$(docker compose ps --format '{{.Health}}' | grep -cvw healthy || true)"
  [ "${pending:-1}" -eq 0 ] && break
  sleep 2
done

# 4. Provision emulator resources for the services that need no AWS account.
#    LocalStack and Moto are in-memory, so this runs on every boot.
if [ -f .env ]; then
  set -a; . ./.env; set +a
  go run ./teams/infra/events/cmd/provision >/dev/null
  go run ./billing/infra/events/cmd/provision >/dev/null
  go run ./files/infra/storage/cmd/provision >/dev/null
  go run ./email/infra/delivery/cmd/provision >/dev/null
fi

echo "Oryxa local dependencies are up and provisioned (Postgres, LocalStack, Moto, Mailpit)."
