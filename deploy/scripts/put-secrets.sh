#!/usr/bin/env bash
# Copies the secrets an operator holds into Secrets Manager, where the tasks
# read them: COMPOSIO_API_KEY, and TYPESAFE_API_KEY when it is set, from the
# environment or the repository's .env.
# Values go from the file to AWS on stdin; nothing is printed. The Cognito
# agent client's secret and Composio's webhook secret are stored by the
# scripts that create them (provision-app.sh, composio-webhook.sh).

source "$(dirname "$0")/lib.sh"
load_environment

# The value of a variable: the environment's, or else the .env file's last
# assignment of it.
value_of() {
  if [[ -n "${!1:-}" ]]; then
    printf '%s' "${!1}"
  elif [[ -r "$ROOT/.env" ]]; then
    sed -n "s/^$1=//p" "$ROOT/.env" | tail -n 1 | tr -d '\n'
  fi
}

for variable in COMPOSIO_API_KEY; do
  slug=$(tr '[:upper:]_' '[:lower:]-' <<<"$variable")
  if [[ -z "$(value_of "$variable")" ]]; then
    echo "$variable is not set in the environment or $ROOT/.env" >&2
    exit 1
  fi
  value_of "$variable" | put_secret "$(secret_id "$slug")"
  step "stored $variable"
done

# TypeSafe's key is needed only once knowledge_judge is jev, which makes its
# secret: create that first (tofu apply -target), then run this.
if [[ -n "$(value_of TYPESAFE_API_KEY)" ]]; then
  value_of TYPESAFE_API_KEY | put_secret "$(secret_id typesafe-api-key)"
  step "stored TYPESAFE_API_KEY"
fi
