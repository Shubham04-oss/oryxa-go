# Secrets whose values come from outside this configuration: from the
# operator's .env, or from what a provisioner answered. OpenTofu makes the
# containers; deploy/scripts/put-secrets.sh fills them, so their values never
# enter the state. The oryxa tasks read them at start through ECS.

locals {
  external_secrets = merge(
    {
      COMPOSIO_API_KEY            = "composio-api-key"
      COMPOSIO_WEBHOOK_SECRET     = "composio-webhook-secret"
      COGNITO_AGENT_CLIENT_SECRET = "cognito-agent-client-secret"
      # Realtime signs every purge it asks the web app's Worker for with
      # this, and checks the Worker's events with it; the Worker holds the
      # same value. Its container exists whatever realtime_purge says, so it
      # can be filled before realtime is on, and an apply with the switch off
      # never deletes what was stored.
      REALTIME_PURGE_SECRET = "realtime-purge-secret"
      # Oryxa's own sign-in (docs/design/integrations-plan.md, work package
      # 1F): Oryxa's own OAuth apps (github, slack, notion, ...) and the
      # deployment's webhook secrets, by toolkit. Both containers exist
      # whatever apps_signin_secrets_ready says, so put-secrets.sh can fill
      # them in before the task ever names them; see task_secrets below and
      # deploy/README.md, "Oryxa's own sign-in".
      APPS_DEFAULT_OAUTH_CLIENTS = "apps-default-oauth-clients"
      APPS_HOOK_SECRETS          = "apps-hook-secrets"
      # Oryxa's Slack bot (docs/design/slack-implementation.md; deploy/README.md,
      # "The Slack bot"). APPS_SLACK_APP is the Slack app's Client ID, Client
      # Secret and Signing Secret as one JSON value, {"client_id",
      # "client_secret", "signing_secret"}; AGENTS_PRIMARY_SLACK_TICKET_KEY is the
      # key agents signs its notices' buttons with, 64 hex characters, which
      # put-secrets.sh makes when there is none. Both containers exist whatever
      # slack_app_ready says, so put-secrets.sh can fill them in before the task
      # ever names them.
      APPS_SLACK_APP                  = "slack-app"
      AGENTS_PRIMARY_SLACK_TICKET_KEY = "slack-ticket-key"
      # Pipedream Connect, the broker beside Composio (internal/pipedream):
      # the workspace's OAuth client and the Connect project it acts in. The
      # containers exist whatever pipedream_ready says, so put-secrets.sh can
      # fill them in before the task ever names them.
      PIPEDREAM_CLIENT_ID     = "pipedream-client-id"
      PIPEDREAM_CLIENT_SECRET = "pipedream-client-secret"
      PIPEDREAM_PROJECT_ID    = "pipedream-project-id"
      # The keys the integrations service signs connectors' egress handles
      # with, {"current": "<kid>", "keys": {"<kid>": "<64 hex>"}}, which
      # put-secrets.sh makes when there are none.
      INTEGRATIONS_EGRESS_KEYS = "integrations-egress-keys"
    },
    # Only when Jev judges: the tasks cannot start while a secret they name
    # holds no value, so it exists only once there is one to put in it.
    var.knowledge_judge == "jev" ? { TYPESAFE_API_KEY = "typesafe-api-key" } : {},
  )

  # Variables the task definition references only once their gate is true.
  # An ECS task whose definition names a secret with no value fails to
  # start, so each container above exists as soon as external_secrets does,
  # but the task itself waits for an operator to say the value is there.
  # Anything left out of this map is ungated: named as soon as its container
  # exists.
  secret_gates = {
    REALTIME_PURGE_SECRET      = var.realtime_purge
    APPS_DEFAULT_OAUTH_CLIENTS = var.apps_signin_secrets_ready
    APPS_HOOK_SECRETS          = var.apps_signin_secrets_ready
    # Both of the Slack bot's, and everything else of it in compute.tf, wait for
    # the one switch.
    APPS_SLACK_APP                  = var.slack_app_ready
    AGENTS_PRIMARY_SLACK_TICKET_KEY = var.slack_app_ready
    # All three of Pipedream's wait for the one switch: none is any use alone.
    PIPEDREAM_CLIENT_ID      = var.pipedream_ready
    PIPEDREAM_CLIENT_SECRET  = var.pipedream_ready
    PIPEDREAM_PROJECT_ID     = var.pipedream_ready
    INTEGRATIONS_EGRESS_KEYS = var.integrations_ready
  }

  task_secrets = { for variable, slug in local.external_secrets : variable => slug if lookup(local.secret_gates, variable, true) }
}

# Declared here, beside the secrets it gates, rather than in variables.tf.
# Put APPS_DEFAULT_OAUTH_CLIENTS and APPS_HOOK_SECRETS in Secrets Manager
# first (deploy/scripts/put-secrets.sh), then turn this on and apply; see
# deploy/README.md, "Oryxa's own sign-in".
variable "apps_signin_secrets_ready" {
  description = "Whether APPS_DEFAULT_OAUTH_CLIENTS and APPS_HOOK_SECRETS hold real values yet. False keeps them out of the task definition, since ECS refuses to start a task whose definition names a secret with no value."
  type        = bool
  default     = false
}

# Declared here too, beside the secrets it gates. It is the one switch of the
# Slack consent loop: with it false the task names neither Slack secret, apps
# serves no Slack surface (APPS_SLACK_SURFACE), agents asks no one for consent in
# Slack (AGENTS_PRIMARY_SLACK_CONSENT) and apps admits no internal caller
# (APPS_SERVICE_CALLERS), so the task is what it is without any of it. Create the
# Slack app, put its values in Secrets Manager (deploy/scripts/put-secrets.sh),
# then turn this on and apply; deploy/README.md, "The Slack bot", gives the order.
variable "slack_app_ready" {
  description = "Whether the Slack app's secrets (APPS_SLACK_APP and AGENTS_PRIMARY_SLACK_TICKET_KEY) hold real values yet, which turns the Slack consent loop on. False keeps both out of the task definition, since ECS refuses to start a task whose definition names a secret with no value, and keeps the whole loop off: no Slack surface in apps, no consent asked in Slack by agents, no internal caller for apps. Set in slack.auto.tfvars.json, so every checkout applies the same."
  type        = bool
  default     = false
}

# Declared here too, beside the secrets it gates. Put the three PIPEDREAM_
# values in Secrets Manager first (deploy/scripts/put-secrets.sh), then turn
# this on and apply.
variable "pipedream_ready" {
  description = "Whether PIPEDREAM_CLIENT_ID, PIPEDREAM_CLIENT_SECRET and PIPEDREAM_PROJECT_ID hold real values yet. False keeps them out of the task definition, since ECS refuses to start a task whose definition names a secret with no value."
  type        = bool
  default     = false
}

# The integrations service ships off (INTEGRATIONS_ENABLED). Run
# put-secrets.sh to make INTEGRATIONS_EGRESS_KEYS, then turn this on and apply
# before switching the service on.
variable "integrations_ready" {
  description = "Whether INTEGRATIONS_EGRESS_KEYS holds a value yet. False keeps it out of the task definition, since ECS refuses to start a task whose definition names a secret with no value."
  type        = bool
  default     = false
}

# Pipedream's development environment lets only the Pipedream workspace's own
# members connect accounts: right for trying it in production before the plan
# includes the production environment, and set in pipedream.auto.tfvars.json.
variable "pipedream_environment" {
  description = "The Pipedream Connect environment apps acts in: production, or development while only the workspace's own members should connect."
  type        = string
  default     = "production"
  validation {
    condition     = contains(["production", "development"], var.pipedream_environment)
    error_message = "pipedream_environment is production or development."
  }
}

resource "aws_secretsmanager_secret" "external" {
  for_each                = local.external_secrets
  name                    = "${local.name}/${each.value}"
  description             = "${each.key} for the oryxa tasks"
  recovery_window_in_days = 0
}
