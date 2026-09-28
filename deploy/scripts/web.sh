#!/usr/bin/env bash
# Builds web-next as the production Worker and uploads it with wrangler (its
# own login: `wrangler login`), then has OpenTofu route app.<domain>/* to it;
# app.<domain>/v1/* stays with the API tunnel, so the app calls its API on its
# own origin. .github/workflows/deploy.yml does the same on every release.

source "$(dirname "$0")/lib.sh"

cd "$ROOT/web-next"
npx --yes pnpm@12.4.2 install --frozen-lockfile
CLOUDFLARE_ENV=production npx --yes pnpm@12.4.2 run build

# The Cloudflare Vite plugin writes the configuration wrangler deploys, and
# says where.
config=$(jq -r .configPath .wrangler/deploy/config.json)
config=$(cd .wrangler/deploy && cd "$(dirname "$config")" && pwd)/$(basename "$config")
worker=$(jq -r .name "$config")
npx wrangler deploy

jq -n --arg worker "$worker" '{web_worker_name: $worker}' >"$TF/web.auto.tfvars.json"
step "routing app. to $worker"
tofu -chdir="$TF" apply
