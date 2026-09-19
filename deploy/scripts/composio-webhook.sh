#!/usr/bin/env bash
# Points the Composio project's webhook subscription at the deployment's API
# (APPS_PUBLIC_BASE_URL + /v1/apps/webhooks/composio) and stores the signing
# secret Composio mints for it, which it shows only once, in Secrets Manager.
#
# A project has one subscription, so this takes it away from wherever it
# delivered before (APPS_REPLACE_WEBHOOK=true). Restart the tasks afterwards
# (release.sh, or a forced deployment) so apps verifies with the new secret.

source "$(dirname "$0")/lib.sh"
load_environment

api_key=$(aws secretsmanager get-secret-value --secret-id "$(secret_id composio-api-key)" --query SecretString --output text)
subscription=$(
  COMPOSIO_API_KEY=$api_key APPS_PUBLIC_BASE_URL=$API_URL APPS_REPLACE_WEBHOOK=true \
    go run ./apps/infra/composio/cmd/provision
)
unset api_key

jq -c 'del(.secret)' <<<"$subscription"
if [[ "$(jq -r .created <<<"$subscription")" == true ]]; then
  jq -j '.secret // ""' <<<"$subscription" | put_secret "$(secret_id composio-webhook-secret)"
  step "stored the new webhook secret"
fi
