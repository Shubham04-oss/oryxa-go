#!/usr/bin/env bash
# Builds the web app as the production Worker and uploads it with wrangler
# (its own login: `wrangler login`), binding Hyperdrive to it, then has
# OpenTofu route app.<domain>/* to it; app.<domain>/v1/* stays with the API
# tunnel, so the app calls its API on its own origin.

source "$(dirname "$0")/lib.sh"

hyperdrive=$(tf_output hyperdrive_id)
cd "$ROOT/web"
npx --yes pnpm@12.4.2 install --frozen-lockfile
CLOUDFLARE_ENV=production npx --yes pnpm@12.4.2 run build

# The Cloudflare Vite plugin writes the configuration wrangler deploys, and
# says where; the Hyperdrive binding's id is the deployment's, so it is added
# there rather than kept in wrangler.jsonc.
config=$(jq -r .configPath .wrangler/deploy/config.json)
config=$(cd .wrangler/deploy && cd "$(dirname "$config")" && pwd)/$(basename "$config")
jq --arg id "$hyperdrive" '.hyperdrive = [{binding: "HYPERDRIVE", id: $id}]' "$config" >"$config.new"
mv "$config.new" "$config"
worker=$(jq -r .name "$config")
npx wrangler deploy

jq -n --arg worker "$worker" '{web_worker_name: $worker}' >"$TF/web.auto.tfvars.json"
step "routing app. to $worker"
tofu -chdir="$TF" apply
