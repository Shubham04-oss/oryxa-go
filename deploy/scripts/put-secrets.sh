#!/usr/bin/env bash
# Copies the secrets an operator holds into Secrets Manager, where the tasks
# read them: COMPOSIO_API_KEY, and TYPESAFE_API_KEY, REALTIME_PURGE_SECRET,
# APPS_DEFAULT_OAUTH_CLIENTS and APPS_HOOK_SECRETS when they are set, from the
# environment or the repository's .env; the last two, Oryxa's own sign-in's,
# may also be typed at a hidden prompt. So may the Slack app's values,
# APPS_SLACK_APP (the Client ID, Client Secret and Signing Secret, stored as
# the one JSON value apps reads), and AGENTS_PRIMARY_SLACK_TICKET_KEY, the key
# the Slack notices' buttons are signed with, which is made here when there is
# none (deploy/README.md, "The Slack bot"). Values go from the file, or the
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

# Oryxa's Slack bot (docs/design/slack-implementation.md, section 4; deploy/README.md,
# "The Slack bot"). Its two secrets' containers are OpenTofu's, made by the apply
# that carries the Slack bot; without them there is nothing to store into, and a
# run that was given no Slack value says so and goes on, so this script still
# does what it did for everyone who does not use the bot.
secret_exists() {
  aws secretsmanager describe-secret --secret-id "$1" >/dev/null 2>&1
}

# Whether the secret holds a value. Secrets Manager says which versions it has
# without saying what is in them, so the value is not read.
secret_has_value() {
  aws secretsmanager describe-secret --secret-id "$1" --query VersionIdsToStages --output json 2>/dev/null |
    jq -e 'type == "object" and length > 0' >/dev/null 2>&1
}

slack_app=$(value_of APPS_SLACK_APP)
# .env may quote the JSON, as .env.example does.
slack_app=${slack_app#\'}
slack_app=${slack_app%\'}
slack_ticket_key=$(value_of AGENTS_PRIMARY_SLACK_TICKET_KEY)
slack_app_secret=$(secret_id slack-app)
slack_ticket_secret=$(secret_id slack-ticket-key)

if ! secret_exists "$slack_app_secret"; then
  if [[ -n $slack_app || -n $slack_ticket_key ]]; then
    echo "APPS_SLACK_APP or AGENTS_PRIMARY_SLACK_TICKET_KEY is set, but its container in Secrets Manager does not exist yet: run tofu apply in deploy/production first (deploy/README.md, \"The Slack bot\"); nothing for Slack was stored" >&2
    exit 1
  fi
  step "Slack skipped: its secrets' containers do not exist yet (tofu apply in deploy/production makes them; deploy/README.md, \"The Slack bot\")"
else
  # The app's values are either the JSON in APPS_SLACK_APP or, on a terminal,
  # typed once at hidden prompts that never echo them. Leaving the first blank
  # skips them, same as never setting the variable. The three reach jq through
  # its environment and never its arguments, which any user on the machine can
  # read, and the JSON goes on to AWS on stdin.
  if [[ -z $slack_app && -t 0 ]]; then
    read -rsp "Slack app Client ID (blank to skip): " slack_client_id
    echo >&2
    if [[ -n $slack_client_id ]]; then
      read -rsp "Slack app Client Secret: " slack_client_secret
      echo >&2
      read -rsp "Slack app Signing Secret: " slack_signing_secret
      echo >&2
      slack_app=$(SLACK_CLIENT_ID=$slack_client_id SLACK_CLIENT_SECRET=$slack_client_secret SLACK_SIGNING_SECRET=$slack_signing_secret \
        jq -cn '{client_id: env.SLACK_CLIENT_ID, client_secret: env.SLACK_CLIENT_SECRET, signing_secret: env.SLACK_SIGNING_SECRET}')
    fi
    unset slack_client_id slack_client_secret slack_signing_secret
  fi

  # Both are checked before either is stored. For the app, what apps checks
  # when it starts (apps/internal/serviceconfig): a client ID of printable
  # characters with no spaces, and secrets of at least 16. It refuses to start
  # with the Slack surface on and a value it cannot read, and the release is
  # rolled back for it, so a mistake is caught here instead. What is stored is
  # those three values, trimmed, and nothing else. The ticket key is an HMAC
  # key that agents and nothing else holds, so it is only ever made here, as
  # openssl rand -hex 32 makes it, and checked when one is given.
  if [[ -n $slack_app ]]; then
    slack_app_program='
      def clean: if type == "string" then gsub("^\\s+|\\s+$"; "") else "" end;
      {client_id: (.client_id | clean), client_secret: (.client_secret | clean), signing_secret: (.signing_secret | clean)}
      | select(
          (.client_id | test("^[!-~]+$"))
          and (.client_secret | test("^[!-~]{16,}$"))
          and (.signing_secret | test("^[!-~]{16,}$")))'
    if ! slack_app=$(printf '%s' "$slack_app" | jq -ce "$slack_app_program" 2>/dev/null) || [[ -z $slack_app ]]; then
      echo 'APPS_SLACK_APP must be JSON, {"client_id": "…", "client_secret": "…", "signing_secret": "…"}, with a client ID and secrets of at least 16 printable characters and no spaces, as the Slack app'"'"'s Basic Information page gives them; nothing for Slack was stored' >&2
      exit 1
    fi
    unset slack_app_program
  fi
  if [[ -n $slack_ticket_key && ! $slack_ticket_key =~ ^[0-9a-fA-F]{64}$ ]]; then
    echo "AGENTS_PRIMARY_SLACK_TICKET_KEY must be exactly 64 hex characters, as openssl rand -hex 32 makes, with no quotes, spaces or comment in .env; nothing for Slack was stored" >&2
    exit 1
  fi

  if [[ -n $slack_app ]]; then
    printf '%s' "$slack_app" | put_secret "$slack_app_secret"
    step "stored APPS_SLACK_APP"
  fi
  unset slack_app

  # A key set in the environment or .env is stored, to change it on purpose.
  # Otherwise one is made, once: a run that finds one stored leaves it, since a
  # new key makes every button already posted refuse its click.
  if [[ -n $slack_ticket_key ]]; then
    printf '%s' "$slack_ticket_key" | put_secret "$slack_ticket_secret"
    step "stored AGENTS_PRIMARY_SLACK_TICKET_KEY"
  elif secret_has_value "$slack_ticket_secret"; then
    step "AGENTS_PRIMARY_SLACK_TICKET_KEY is stored already: left as it is"
  else
    openssl rand -hex 32 | tr -d '\n' | put_secret "$slack_ticket_secret"
    step "made and stored AGENTS_PRIMARY_SLACK_TICKET_KEY"
  fi
  unset slack_ticket_key
fi
