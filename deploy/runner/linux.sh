#!/usr/bin/env bash
# Provisions this repository's self-hosted runners on a Linux host (a1, or
# better a machine of their own), one systemd service per runner. README.md
# here says what each runner may reach and why (#459, #460).
#
#   sudo deploy/runner/linux.sh oryxa-go-1 oryxa-go-2
#
# Each runner gets:
# - a user of its own, runner-NAME, with no sudo, not in the docker group, and
#   a home that every job starts with empty (ephemeral.sh);
# - rootless Docker running as that user, so jobs keep `docker run` and
#   service containers without root on the host;
# - a just-in-time registration per job, and job-started.sh as the gate;
# - no route to private networks, the tailnet or cloud metadata.
#
# Needs root, Docker CE's apt repository, and in /etc/oryxa-runner/token a
# fine-grained GitHub token with Administration: write on the repository only.
# Run it again to update the runner or the scripts (that cancels running
# jobs); it changes nothing it doesn't own.
set -euo pipefail

[ "$(id -u)" = 0 ] || { echo "run as root: sudo $0 $*" >&2; exit 1; }
[ $# -gt 0 ] || { echo "usage: $0 NAME..." >&2; exit 1; }
[ -s /etc/oryxa-runner/token ] || { echo "write the registration token to /etc/oryxa-runner/token first" >&2; exit 1; }
chown root:root /etc/oryxa-runner /etc/oryxa-runner/token
chmod 700 /etc/oryxa-runner
chmod 600 /etc/oryxa-runner/token

here=$(cd "$(dirname "$0")" && pwd)
repository=${RUNNER_REPOSITORY:-Shubham04-oss/oryxa-go}
labels=${RUNNER_LABELS:-self-hosted,Linux,$([ "$(uname -m)" = aarch64 ] && echo ARM64 || echo X64),oryxa-go}
lib=/usr/local/lib/oryxa-runner

apt-get update -q
apt-get install -y -q --no-install-recommends ca-certificates curl git jq unzip libatomic1 \
  iptables uidmap slirp4netns docker-ce-cli docker-ce-rootless-extras docker-buildx-plugin

# Ubuntu 24.04 confines unprivileged user namespaces, which rootless Docker
# needs; this is the profile Docker's own documentation gives rootlesskit.
if [ "$(sysctl -n kernel.apparmor_restrict_unprivileged_userns 2>/dev/null || echo 0)" = 1 ]; then
  cat >/etc/apparmor.d/usr.bin.rootlesskit <<'EOF'
abi <abi/4.0>,
include <tunables/global>

/usr/bin/rootlesskit flags=(unconfined) {
  userns,

  include if exists <local/usr.bin.rootlesskit>
}
EOF
  apparmor_parser -r /etc/apparmor.d/usr.bin.rootlesskit
fi

# The latest runner, checked against the checksum its release publishes.
install -d -m 755 "$lib"
arch=$([ "$(uname -m)" = aarch64 ] && echo arm64 || echo x64)
release=$(curl -fsSL https://api.github.com/repos/actions/runner/releases/latest)
version=$(jq -r .tag_name <<<"$release")
sum=$(jq -r .body <<<"$release" | sed -n "s/.*<!-- BEGIN SHA linux-$arch -->\([0-9a-f]\{64\}\)<!-- END SHA linux-$arch -->.*/\1/p")
curl -fsSL -o "$lib/runner.tar.gz.new" \
  "https://github.com/actions/runner/releases/download/$version/actions-runner-linux-$arch-${version#v}.tar.gz"
echo "${sum:?no checksum in the release notes}  $lib/runner.tar.gz.new" | sha256sum -c --quiet -
mv "$lib/runner.tar.gz.new" "$lib/runner.tar.gz"
deps=$(mktemp -d)
tar -xzf "$lib/runner.tar.gz" -C "$deps"
"$deps/bin/installdependencies.sh" >/dev/null
rm -rf "$deps"
install -m 755 "$here/ephemeral.sh" "$here/job-started.sh" "$lib/"

cat >/etc/systemd/system/oryxa-runner@.service <<EOF
[Unit]
Description=GitHub Actions runner %i (deploy/runner/linux.sh)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$lib/ephemeral.sh %i
# One job per start; stopping kills everything the job left in this cgroup.
Restart=always
RestartSec=2
KillMode=control-group
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload

# The first free 65536 ids after every range a file already hands out.
next_range() { awk -F: '{ end = $2 + $3; if (end > max) max = end } END { print (max > 100000 ? max : 100000) }' "$1"; }

for name in "$@"; do
  user=runner-$name
  id "$user" >/dev/null 2>&1 ||
    useradd --no-create-home --home-dir "/home/$user" --shell /usr/sbin/nologin --user-group "$user"
  usermod --lock "$user"
  for group in docker sudo adm; do gpasswd --delete "$user" "$group" >/dev/null 2>&1 || true; done
  if ! grep -q "^$user:" /etc/subuid; then
    start=$(next_range /etc/subuid)
    usermod --add-subuids "$start-$((start + 65535))" "$user"
  fi
  if ! grep -q "^$user:" /etc/subgid; then
    start=$(next_range /etc/subgid)
    usermod --add-subgids "$start-$((start + 65535))" "$user"
  fi
  printf 'RUNNER_USER=%q\nREPOSITORY=%q\nLABELS=%q\n' "$user" "$repository" "$labels" >"/etc/oryxa-runner/$name.env"
  systemctl enable "oryxa-runner@$name"
  systemctl restart "oryxa-runner@$name"

  # What a job could still read: other people's homes and runners. Fix these
  # by hand (chmod 700, or move the runner to a host of its own).
  for path in /root /home/* /opt/actions-runner*; do
    [ -e "$path" ] && [ "$path" != "/home/$user" ] || continue
    if runuser -u "$user" -- test -r "$path" -a -x "$path"; then
      echo "warning: $user can read $path" >&2
    fi
  done
done
echo "runners: systemctl status 'oryxa-runner@*'; logs: journalctl -u oryxa-runner@NAME"
