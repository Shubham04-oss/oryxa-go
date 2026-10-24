#!/usr/bin/env bash
# Runs one job on a self-hosted runner, on a clean slate. linux.sh installs
# this as the oryxa-runner@NAME systemd service, which runs it as root and
# starts it again after every job; nothing of a job runs as root.
#
# Each start:
# 1. deletes the runner's user's home and shared memory. systemd already
#    killed everything the last job left in this service's cgroup, its
#    containers included, and gave this start a /tmp of its own, so a job
#    can't plant anything a later job would run (#460);
# 2. starts a fresh rootless Docker engine for that user, so jobs can start
#    containers without the docker group, which is root on the host;
# 3. registers a just-in-time runner, which takes one job and is then removed
#    from GitHub, and runs it with job-started.sh as its gate.
set -uo pipefail

name=${1:?usage: ephemeral.sh NAME}
# shellcheck source=/dev/null
. "/etc/oryxa-runner/$name.env" # RUNNER_USER, REPOSITORY, LABELS
lib=/usr/local/lib/oryxa-runner
uid=$(id -u "$RUNNER_USER")
home=/home/$RUNNER_USER
run=/run/user/$uid
log=/var/log/oryxa-runner
mkdir -p "$log"

# The runner's user reaches the internet and its own loopback only: not the
# LAN or VCN, the tailnet (except its DNS), or the cloud's metadata service,
# which would hand out the instance's credentials. Rules are by user, so they
# cover its rootless containers too, whose traffic leaves as that user.
rule() { "$1" -C OUTPUT "${@:2}" 2>/dev/null || "$1" -I OUTPUT "${@:2}"; }
for net in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 169.254.0.0/16; do
  rule iptables -m owner --uid-owner "$uid" -d "$net" -j REJECT
done
for net in fc00::/7 fe80::/10; do
  rule ip6tables -m owner --uid-owner "$uid" -d "$net" -j REJECT
done
for proto in udp tcp; do # inserted last, so first: Tailscale's DNS stays reachable
  rule iptables -m owner --uid-owner "$uid" -d 100.100.100.100 -p "$proto" --dport 53 -j ACCEPT
done

as() {
  runuser -u "$RUNNER_USER" -- env -i HOME="$home" USER="$RUNNER_USER" LANG=C.UTF-8 \
    PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    XDG_RUNTIME_DIR="$run" DOCKER_HOST="unix://$run/docker.sock" \
    ORYXA_RUNNER_REPOSITORY="$REPOSITORY" ACTIONS_RUNNER_HOOK_JOB_STARTED="$lib/job-started.sh" \
    "$@"
}

rm -rf "$home" "$run"
find /dev/shm -user "$uid" -delete 2>/dev/null
install -d -o "$RUNNER_USER" -g "$RUNNER_USER" -m 700 "$home" "$home/runner" "$run"
as tar -xzf "$lib/runner.tar.gz" -C "$home/runner"

as dockerd-rootless.sh --data-root "$home/.docker" >"$log/$name-dockerd.log" 2>&1 &
for _ in $(seq 60); do
  as docker info >/dev/null 2>&1 && break
  sleep 1
done
if ! as docker info >/dev/null 2>&1; then
  echo "rootless Docker did not start; see $log/$name-dockerd.log" >&2
  sleep 30
  exit 1
fi

# One registration per job. The token file holds a fine-grained token with
# Administration: write on the repository and nothing else. It and the
# runner's config stay off command lines, which any local user can read.
body=$(jq -n --arg name "$name-$(date +%s)" --arg labels "$LABELS" \
  '{name: $name, runner_group_id: 1, labels: ($labels | split(",")), work_folder: "_work"}')
jit=$(printf 'header = "Authorization: Bearer %s"\n' "$(cat /etc/oryxa-runner/token)" |
  curl -fsS -K - -X POST -H "Accept: application/vnd.github+json" -d "$body" \
    "https://api.github.com/repos/$REPOSITORY/actions/runners/generate-jitconfig" |
  jq -r '.encoded_jit_config // empty')
if [ -z "$jit" ]; then
  echo "could not register a runner for $REPOSITORY" >&2
  sleep 30
  exit 1
fi
(umask 077 && printf '%s' "$jit" >"$home/.jitconfig")
chown "$RUNNER_USER:" "$home/.jitconfig"
# The runner reads ACTIONS_RUNNER_INPUT_JITCONFIG as its --jitconfig.
cd "$home/runner" &&
  as bash -c 'export ACTIONS_RUNNER_INPUT_JITCONFIG=$(cat ~/.jitconfig) && rm ~/.jitconfig && exec ./run.sh'
