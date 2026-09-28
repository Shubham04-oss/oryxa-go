#!/usr/bin/env bash
# Creates what the services' own provisioners own, under the names OpenTofu
# fixed for them: each service's event queue, its dead-letter queue and the
# EventBridge rules that feed it (generated from the code that consumes them,
# which is why they are not in OpenTofu); the files, artifacts and vector
# buckets; email's tables, queue and SES configuration set; and auth's Cognito
# user pool, whose identifiers it writes to app.auto.tfvars.json for the next
# `tofu apply`.
#
# Every provisioner is repeatable, so this is too. Run it after the first
# `tofu apply` and again whenever a service subscribes to a new event.
#
# Cognito signs members in with Google, so its provisioner needs the Google
# OAuth client: GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET in the
# environment, or in ~/.config/oryxa/google-oauth.env. Without them the
# Cognito step is skipped and said to be.

source "$(dirname "$0")/lib.sh"
load_environment

provision() {
  local package=$1
  shift
  step "$package"
  env "$@" go run "./$package/cmd/provision" | jq -c .
}

# Every queue this names, looked up before anything is provisioned. A service
# OpenTofu names no queue for is one whose configuration has not been applied
# yet, and stopping on it part way through would leave the services after it
# unprovisioned, without a word.
missing=()
for service in agents apps auth batching billing files knowledge rag realtime teams; do
  queue_name "$service" >/dev/null || missing+=("$service")
done
if ((${#missing[@]} > 0)); then
  echo "OpenTofu names no queue for ${missing[*]} yet: run tofu apply in deploy/production first, then this again" >&2
  exit 1
fi

for service in apps auth batching billing knowledge realtime teams; do
  provision "$service/infra/events" "$(tr '[:lower:]' '[:upper:]' <<<"$service")_EVENTS_QUEUE_NAME=$(queue_name "$service")"
done
provision agents/primary/infra/events "AGENTS_EVENTS_QUEUE_NAME=$(queue_name agents)"
provision files/infra/storage "FILES_EVENTS_QUEUE_NAME=$(queue_name files)"
provision rag/infra/vectors "RAG_EVENTS_QUEUE_NAME=$(queue_name rag)"
provision agents/primary/infra/artifacts
# SES_REGION, EMAIL_QUEUE_NAME, the tables and the configuration set come from
# the OpenTofu outputs.
provision email/infra/delivery

oauth_file=~/.config/oryxa/google-oauth.env
if [[ -z "${GOOGLE_OAUTH_CLIENT_ID:-}" && -r $oauth_file ]]; then
  set -a
  # shellcheck source=/dev/null
  source "$oauth_file"
  set +a
fi
if [[ -z "${GOOGLE_OAUTH_CLIENT_ID:-}" || -z "${GOOGLE_OAUTH_CLIENT_SECRET:-}" ]]; then
  step "auth/infra/cognito skipped: no Google OAuth client (see the top of this script)"
  exit 0
fi

step auth/infra/cognito
cognito=$(
  COGNITO_POOL_NAME=$COGNITO_POOL_NAME \
    COGNITO_APP_CLIENT_NAME=$COGNITO_APP_CLIENT_NAME \
    COGNITO_DOMAIN_PREFIX=$COGNITO_DOMAIN_PREFIX \
    COGNITO_CALLBACK_URLS="$APP_URL/v1/auth/callback" \
    COGNITO_LOGOUT_URLS="$APP_URL" \
    COGNITO_AGENT_CALLBACK_URL="$APP_URL/v1/agent-grant/callback" \
    COGNITO_CLI_CALLBACK_URL="http://127.0.0.1:8765/callback" \
    go run ./auth/infra/cognito/cmd/provision
)
jq -c . <<<"$cognito"
jq '{cognito: {
  user_pool_id: .user_pool_id,
  client_id: .app_client_id,
  domain: .cognito_domain,
  agent_client_id: (.agent_app_client_id // ""),
  cli_client_id: (.cli_app_client_id // "")
}}' <<<"$cognito" >"$TF/app.auto.tfvars.json"

# The agent client is confidential. Its secret goes from Cognito straight to
# Secrets Manager, where the tasks read it.
pool=$(jq -r .user_pool_id <<<"$cognito")
agent_client=$(jq -r '.agent_app_client_id // empty' <<<"$cognito")
if [[ -n $agent_client ]]; then
  aws cognito-idp describe-user-pool-client --user-pool-id "$pool" --client-id "$agent_client" \
    --query UserPoolClient.ClientSecret --output text | tr -d '\n' |
    put_secret "$(secret_id cognito-agent-client-secret)"
  step "stored the agent client's secret"
fi

step "done: add $(jq -r .google_redirect_uri <<<"$cognito") to the Google OAuth client's authorized redirect URIs, then tofu apply"
