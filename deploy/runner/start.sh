#!/usr/bin/env bash
# Starts the self-hosted GitHub Actions runner for this repository in Docker,
# registering it on the first start. Run it again to update the image or bring
# the runner back; README.md here says when and why.
#
#   deploy/runner/start.sh               # runner "oryxa-mac-1", on the Mac's Docker
#   RUNNER_NAME=oryxa-mac-2 deploy/runner/start.sh  # a second, with its own Docker
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
# The first runner uses the Mac's Docker engine and network. Any other gets its
# own engine and network: CI's PostgreSQL service takes port 5432 on the network
# it runs on, so two jobs on one engine would fight over it, as would any test
# that listens on a fixed port.
docker_mode=${RUNNER_DOCKER:-$([ "$name" = oryxa-mac-1 ] && echo host || echo own)}

docker build --pull --tag "$image" "$here"

# The runner's registration, work directory and tool cache live in a volume
# named after it and survive container recreation. Local Go module/build
# caches under /root last only until recreation. The first mount copies the
# image's runner in.
if ! docker volume inspect "$name" >/dev/null 2>&1 || [ "${REREGISTER:-}" = 1 ]; then
  token=$(gh api -X POST "repos/$repository/actions/runners/registration-token" --jq .token)
  docker run --rm --user root -e RUNNER_ALLOW_RUNASROOT=1 -e TOKEN="$token" \
    -v "$name:/home/runner" "$image" \
    bash -c './config.sh --unattended --replace --url "https://github.com/'"$repository"'" --token "$TOKEN" --name "'"$name"'" --labels "'"$labels"'" --work _work'
fi

docker rm -f "$name" >/dev/null 2>&1 || true
if [ "$docker_mode" = host ]; then
  # Host networking puts the job on the network Docker publishes service ports
  # to, so CI's PostgreSQL service answers on localhost:5432 as it does on
  # GitHub's machines. The socket lets jobs start services and build images.
  docker run --detach --name "$name" --restart unless-stopped --network host \
    --user root -e RUNNER_ALLOW_RUNASROOT=1 \
    -v "$name:/home/runner" -v /var/run/docker.sock:/var/run/docker.sock \
    "$image" ./run.sh
else
  # Its own engine (Docker in Docker), whose network the runner joins: the
  # services a job starts publish their ports there, so they answer on this
  # runner's localhost and never meet the first runner's. The engine listens
  # only on that network's loopback. The memory caps keep a busy second runner
  # from starving the first, which deploys; fewer parallel Go packages keep
  # its Go jobs under its cap.
  engine="$name-docker"
  docker rm -f "$engine" >/dev/null 2>&1 || true
  docker run --detach --name "$engine" --restart unless-stopped --privileged \
    --memory 3g -v "$engine:/var/lib/docker" \
    docker:dind dockerd --host=tcp://127.0.0.1:2375 --tls=false
  for _ in $(seq 60); do
    docker exec "$engine" docker --host tcp://127.0.0.1:2375 info >/dev/null 2>&1 && break
    sleep 1
  done
  docker run --detach --name "$name" --restart unless-stopped --network "container:$engine" \
    --memory 4g --user root -e RUNNER_ALLOW_RUNASROOT=1 \
    -e DOCKER_HOST=tcp://127.0.0.1:2375 -e GOFLAGS=-p=4 \
    -v "$name:/home/runner" \
    "$image" ./run.sh
fi
echo "runner $name is running; follow it with: docker logs -f $name"
