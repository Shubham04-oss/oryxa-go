#!/usr/bin/env bash
# Builds the oryxa image for Fargate's arm64 from the commit checked out,
# pushes it to ECR tagged with that commit, and rolls the service onto it
# through OpenTofu, which waits until the new task is healthy and serving; the
# deployment circuit breaker rolls back one that never is.
#
# The tag and task count are kept in release.auto.tfvars.json, so any later
# `tofu apply` keeps the release that runs rather than going back to the
# defaults. Commit that file with the release. DESIRED_COUNT sets the count
# (1 unless it is already set there).

source "$(dirname "$0")/lib.sh"

if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "commit or stash your changes first: the image is tagged with the commit it is built from" >&2
  exit 1
fi
tag=$(git rev-parse --short=12 HEAD)
release="$TF/release.auto.tfvars.json"
count=${DESIRED_COUNT:-$(jq -r '.desired_count // 1' "$release" 2>/dev/null || echo 1)}
repository=$(tf_output ecr_repository)

step "building $repository:$tag"
aws ecr get-login-password | docker login --username AWS --password-stdin "${repository%%/*}"
docker build --platform linux/arm64 --build-arg VERSION="$tag" --tag "$repository:$tag" .
docker push "$repository:$tag"

jq -n --arg tag "$tag" --argjson count "$count" '{image_tag: $tag, desired_count: $count}' >"$release"
step "rolling out $tag to $count task(s)"
tofu -chdir="$TF" apply
step "$tag is serving"
