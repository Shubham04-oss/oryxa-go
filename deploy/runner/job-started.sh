#!/usr/bin/env bash
# Runs before every job on the Linux runners linux.sh provisions (the runner's
# ACTIONS_RUNNER_HOOK_JOB_STARTED) and fails the job, before any of its code
# runs, unless it belongs on them. A pull request can change the workflow it
# runs, so ci.yml's own routing of forks to GitHub's machines is not enough:
# this check lives on the runner, where a pull request can't reach it.
#
# Refused: another repository's jobs, a pull request from a fork (#459),
# pull_request_target, and the production deploy, which holds production
# credentials and runs on GitHub's machines (#461).
set -euo pipefail

refuse() {
  echo "::error::this self-hosted runner refuses $1; see deploy/runner/README.md" >&2
  exit 1
}

[ "${GITHUB_REPOSITORY:-}" = "${ORYXA_RUNNER_REPOSITORY:?}" ] ||
  refuse "jobs for ${GITHUB_REPOSITORY:-an unknown repository}"

case "${GITHUB_EVENT_NAME:-}" in
pull_request_target) refuse "pull_request_target" ;;
pull_request | pull_request_review | pull_request_review_comment)
  head=$(jq -r '.pull_request.head.repo.full_name // ""' "${GITHUB_EVENT_PATH:?}")
  [ "$head" = "$GITHUB_REPOSITORY" ] || refuse "a pull request from ${head:-a deleted fork}"
  ;;
esac

case "${GITHUB_WORKFLOW_REF:-}" in
*/.github/workflows/deploy.yml@*) refuse "the production deploy" ;;
esac
