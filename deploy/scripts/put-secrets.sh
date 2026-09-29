#!/usr/bin/env bash
# Copies the secrets an operator holds into Secrets Manager, where the tasks
# read them: COMPOSIO_API_KEY, and TYPESAFE_API_KEY, REALTIME_PURGE_SECRET,
# APPS_DEFAULT_OAUTH_CLIENTS and APPS_HOOK_SECRETS when they are set, from the
# environment or the repository's .env; the last two, Oryxa's own sign-in's,
# may also be typed at a hidden prompt. Values go from the file, or the
# prompt, to AWS on stdin; nothing is printed. The Cognito agent client's
# secret and Composio's webhook secret are stored by the scripts that create
# them (provision-app.sh, composio-webhook.sh).

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

# Realtime's secret is an HMAC key the Worker holds too, character for
# character, and neither side strips the quotes, or the comment after the
# value, that .env can carry with it. So it is stored only as openssl rand
# -hex 32 makes it, and checked before anything else is stored.
realtime_purge_secret=$(value_of REALTIME_PURGE_SECRET)
if [[ -n $realtime_purge_secret && ! $realtime_purge_secret =~ ^[0-9a-fA-F]{64}$ ]]; then
  echo "REALTIME_PURGE_SECRET must be exactly 64 hex characters, as openssl rand -hex 32 makes, with no quotes, spaces or comment in .env; nothing was stored" >&2
  exit 1
fi
unset realtime_purge_secret

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

# Realtime signs every purge it asks the web app's Worker for with this, and
# checks the Worker's events with it; the Worker holds the same 64
# characters. Its secret's container exists whatever realtime_purge says.
if [[ -n "$(value_of REALTIME_PURGE_SECRET)" ]]; then
  value_of REALTIME_PURGE_SECRET | put_secret "$(secret_id realtime-purge-secret)"
  step "stored REALTIME_PURGE_SECRET"
fi

# Oryxa's own sign-in's two secrets (docs/design/integrations-plan.md, work
# package 1F) are each JSON holding more than one value: Oryxa's own OAuth
# apps by toolkit, and the deployment's webhook secrets by toolkit. Each is
# read like the ones above if it is already in the environment or .env, or
# else, on a terminal, typed once at a hidden prompt that never echoes it.
# Leaving a prompt blank skips it, same as never setting the variable: both
# are needed only once a toolkit is switched on, and their containers exist
# whatever apps_signin_secrets_ready says (deploy/README.md, "Oryxa's own
# sign-in").
for entry in "APPS_DEFAULT_OAUTH_CLIENTS:apps-default-oauth-clients" "APPS_HOOK_SECRETS:apps-hook-secrets"; do
  variable=${entry%%:*}
  slug=${entry#*:}
  signin_secret_value=$(value_of "$variable")
  if [[ -z $signin_secret_value && -t 0 ]]; then
    read -rsp "$variable (JSON, blank to skip): " signin_secret_value
    echo >&2
  fi
  if [[ -n $signin_secret_value ]]; then
    if ! jq -e . >/dev/null 2>&1 <<<"$signin_secret_value"; then
      echo "$variable is not valid JSON; nothing was stored" >&2
      exit 1
    fi
    printf '%s' "$signin_secret_value" | put_secret "$(secret_id "$slug")"
    step "stored $variable"
  fi
  unset signin_secret_value variable slug
done
