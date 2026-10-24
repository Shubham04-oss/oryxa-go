#!/usr/bin/env bash
# Checks job-started.sh against the events it must refuse and the ones it must
# let through. Needs bash and jq: deploy/runner/job-started-test.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
repo=Shubham04-oss/oryxa-go
ci="$repo/.github/workflows/ci.yml@refs/heads/main"
failed=0

# expect allow|refuse EVENT_NAME HEAD_REPO WORKFLOW_REF [REPOSITORY]
expect() {
  printf '{"pull_request":{"head":{"repo":%s}}}' "$([ -n "$3" ] && jq -n --arg r "$3" '{full_name:$r}' || echo null)" >"$tmp/event.json"
  if ORYXA_RUNNER_REPOSITORY=$repo GITHUB_REPOSITORY=${5:-$repo} GITHUB_EVENT_NAME=$2 \
    GITHUB_EVENT_PATH=$tmp/event.json GITHUB_WORKFLOW_REF=$4 "$here/job-started.sh" 2>/dev/null; then
    got=allow
  else
    got=refuse
  fi
  if [ "$got" != "$1" ]; then
    echo "FAIL: $2 from ${3:-nowhere} ($4, ${5:-$repo}): want $1, got $got"
    failed=1
  fi
}

expect allow pull_request "$repo" "$ci"
expect allow push "" "$ci"
expect refuse pull_request someone/oryxa-go "$ci"
expect refuse pull_request "" "$ci" # a deleted fork
expect refuse pull_request_review someone/oryxa-go "$ci"
expect refuse pull_request_target "$repo" "$ci"
expect refuse workflow_run "" "$repo/.github/workflows/deploy.yml@refs/heads/main"
expect refuse workflow_dispatch "" "$repo/.github/workflows/deploy.yml@refs/heads/main"
expect refuse push "" "$ci" someone/other

[ "$failed" = 0 ] && echo "job-started.sh: all cases pass"
exit "$failed"
