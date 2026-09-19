# Sourced by the other scripts: where the repository and the OpenTofu
# configuration are, and what that configuration says the rest should use.
# AWS credentials come from the usual chain (AWS_PROFILE and the like); the
# region is the deployment's own.

set -euo pipefail

ROOT=$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)
TF="$ROOT/deploy/production"
cd "$ROOT"

# One output of the configuration, as plain text.
tf_output() {
  tofu -chdir="$TF" output -raw "$1"
}

# Exports what the service provisioners are handed: region, bus, queue and
# bucket names, the public URLs (see provisioner_environment in outputs.tf).
load_environment() {
  eval "$(tofu -chdir="$TF" output -json provisioner_environment |
    jq -r 'to_entries[] | "export \(.key)=\(.value | @sh)"')"
  export AWS_REGION AWS_DEFAULT_REGION="$AWS_REGION"
}

# The queue OpenTofu named for a service.
queue_name() {
  jq -er --arg service "$1" '.[$service]' <<<"$QUEUE_NAMES"
}

# Stores what arrives on stdin as the secret's current value. The value never
# appears in an argument list, a file or the terminal.
put_secret() {
  aws secretsmanager put-secret-value --secret-id "$1" --secret-string file:///dev/stdin >/dev/null
}

# The secret's name in Secrets Manager, from its slug in secrets.tf.
secret_id() {
  echo "$(tf_output name)/$1"
}

step() {
  printf '\n==> %s\n' "$*" >&2
}
