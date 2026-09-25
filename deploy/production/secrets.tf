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
      # this, and the Worker holds the same value. Its container exists
      # whatever realtime_purge says, so it can be filled before realtime is
      # on, and an apply with the switch off never deletes what was stored.
      REALTIME_PURGE_SECRET = "realtime-purge-secret"
    },
    # Only when Jev judges: the tasks cannot start while a secret they name
    # holds no value, so it exists only once there is one to put in it.
    var.knowledge_judge == "jev" ? { TYPESAFE_API_KEY = "typesafe-api-key" } : {},
  )

  # The secrets the task names. Realtime's only once realtime_purge is on, for
  # the same reason: the container is there sooner, and may still be empty.
  task_secrets = { for variable, slug in local.external_secrets : variable => slug if variable != "REALTIME_PURGE_SECRET" || var.realtime_purge }
}

resource "aws_secretsmanager_secret" "external" {
  for_each                = local.external_secrets
  name                    = "${local.name}/${each.value}"
  description             = "${each.key} for the oryxa tasks"
  recovery_window_in_days = 0
}
