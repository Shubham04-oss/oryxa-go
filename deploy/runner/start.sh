#!/usr/bin/env bash
# Starts the self-hosted GitHub Actions runner for this repository in Docker,
# registering it on the first start. Run it again to update the image or bring
# the runner back; README.md here says when and why.
#
#   deploy/runner/start.sh               # runner "oryxa-mac-1"
#   RUNNER_NAME=oryxa-mac-2 deploy/runner/start.sh
#   REREGISTER=1 deploy/runner/start.sh  # after removing it on GitHub
#
# Needs Docker, and gh signed in as someone who administers the repository:
# registering asks GitHub for a one-hour registration token.
set -euo pipefail

name=${RUNNER_NAME:-oryxa-mac-1}
repository=${RUNNER_REPOSITORY:-Shubham04-oss/oryxa-go}
labels=${RUNNER_LABELS:-oryxa-mac}
image=oryxa-runner:latest
here=$(cd "$(dirname "$0")" && pwd)

docker build --pull --tag "$image" "$here"

# The runner's state lives in a volume named after it: its registration
# (.runner, .credentials), its work directory and its tool cache, so the Go and
# pnpm caches survive a restart. The first mount copies the image's runner in.
if ! docker volume inspect "$name" >/dev/null 2>&1 || [ "${REREGISTER:-}" = 1 ]; then
  token=$(gh api -X POST "repos/$repository/actions/runners/registration-token" --jq .token)
  docker run --rm --user root -e RUNNER_ALLOW_RUNASROOT=1 -e TOKEN="$token" \
    -v "$name:/home/runner" "$image" \
    bash -c './config.sh --unattended --replace --url "https://github.com/'"$repository"'" --token "$TOKEN" --name "'"$name"'" --labels "'"$labels"'" --work _work'
fi

# Host networking puts the job on the network Docker publishes service ports
# to, so CI's PostgreSQL service answers on localhost:5432 as it does on
# GitHub's machines. The socket lets jobs start services and build images.
docker rm -f "$name" >/dev/null 2>&1 || true
docker run --detach --name "$name" --restart unless-stopped --network host \
  --user root -e RUNNER_ALLOW_RUNASROOT=1 \
  -v "$name:/home/runner" -v /var/run/docker.sock:/var/run/docker.sock \
  "$image" ./run.sh
echo "runner $name is running; follow it with: docker logs -f $name"
