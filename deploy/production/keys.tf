# Auth seals agent grants with envelope encryption; agents, knowledge and rag
# each prove which service is calling with an ES256 signature over its own key
# (internal/serviceauth), which the service it calls checks with the public
# half. Apps seals what it holds for Oryxa's own sign-in and the Slack bot under
# a key of its own, and agents seals the one-time secrets its members share
# under another.
#
# No apply should be able to lose one quietly. A destroyed auth key leaves
# every member's stored agent grant unreadable, to be given again; a destroyed
# signing key leaves its service unable to prove who it is. So none of these
# can be destroyed by an ordinary apply (prevent_destroy), and a key scheduled
# for deletion some other way has 30 days to be cancelled, the longest window
# AWS allows.

resource "aws_kms_key" "auth" {
  description             = "${local.name} auth: envelope encryption of agent grants"
  enable_key_rotation     = true
  deletion_window_in_days = 30

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "auth" {
  name          = "alias/${local.name}-auth"
  target_key_id = aws_kms_key.auth.key_id
}

locals {
  signing_services = ["agents-primary", "knowledge", "rag"]
}

resource "aws_kms_key" "service" {
  for_each                 = toset(local.signing_services)
  description              = "${local.name} ${each.key}: service identity signatures"
  key_usage                = "SIGN_VERIFY"
  customer_master_key_spec = "ECC_NIST_P256"
  deletion_window_in_days  = 30

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "service" {
  for_each      = toset(local.signing_services)
  name          = "alias/${local.name}-${each.key}"
  target_key_id = aws_kms_key.service[each.key].key_id
}

# Every service that publishes events signs them (internal/eventauth). Without
# a KMS key a service signs with a key its process makes at boot, which dies
# with the process, so events in flight across a restart or a rolling deploy
# can't be verified and ORYXA_EVENT_SIGNATURES must stay report. The three
# services above reuse their identity key: an event signature is made over
# bytes that start "oryxa.event.v1", so it can never pass as a service token.
# The rest get a key each. ci.yml checks every publisher is listed here.
locals {
  event_signing_services = ["apps", "auth", "batching", "billing", "files", "integrations", "realtime", "teams"]
  event_signing_keys = merge(
    {
      agents    = aws_kms_alias.service["agents-primary"].name
      knowledge = aws_kms_alias.service["knowledge"].name
      rag       = aws_kms_alias.service["rag"].name
    },
    { for service in local.event_signing_services : service => aws_kms_alias.event[service].name },
  )
}

resource "aws_kms_key" "event" {
  for_each                 = toset(local.event_signing_services)
  description              = "${local.name} ${each.key}: event signatures"
  key_usage                = "SIGN_VERIFY"
  customer_master_key_spec = "ECC_NIST_P256"
  deletion_window_in_days  = 30

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "event" {
  for_each      = toset(local.event_signing_services)
  name          = "alias/${local.name}-${each.key}-events"
  target_key_id = aws_kms_key.event[each.key].key_id
}

locals {
  # Which services auth and knowledge admit, and for what (see
  # AUTH_SERVICE_CALLERS and KNOWLEDGE_SERVICE_CALLERS in .env.example).
  auth_service_callers = jsonencode([
    { service = "agents", key_id = aws_kms_alias.service["agents-primary"].name, purposes = ["agent"] },
    { service = "knowledge", key_id = aws_kms_alias.service["knowledge"].name, purposes = ["agent"] },
  ])
  knowledge_service_callers = jsonencode([
    { service = "rag", key_id = aws_kms_alias.service["rag"].name, purposes = ["documents"] },
  ])
  # Which services apps admits on its internal routes, and for what
  # (APPS_SERVICE_CALLERS, apps/README.md, "The internal API"): agents, for the
  # purpose slack. It reads what Slack sent, and has the bot post back, through
  # those routes, signing each call with its own key above. The purpose is the
  # only one granted: a token for another is refused there, and the deployment's
  # own webhook endpoints (purpose hooks) stay served to no one. compute.tf sets
  # it only with slack_app_ready, since it is the Slack bot's alone.
  apps_service_callers = jsonencode([
    { service = "agents", key_id = aws_kms_alias.service["agents-primary"].name, purposes = ["slack"] },
  ])
}

# Oryxa's own sign-in (docs/design/integrations-plan.md, work package 1F):
# apps/internal/signin seals every OAuth credential, key, password and
# webhook secret it holds under this key, each bound to its own encryption
# context (internal/secrets.Envelope), service "oryxa-apps". It ships
# switched off (APPS_ORYXA_TOOLKITS empty in compute.tf) until a toolkit is
# added at the plan's gate G4, but once real members' credentials are sealed
# under it, losing the key loses them beyond recovery, so it is kept as the
# keys above are: a 30-day deletion window, the longest AWS allows, and no
# ordinary apply can destroy it.
resource "aws_kms_key" "apps_signin" {
  description             = "${local.name} apps: Oryxa's own sign-in, envelope encryption of credentials and webhook secrets"
  enable_key_rotation     = true
  deletion_window_in_days = 30

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "apps_signin" {
  name          = "alias/${local.name}-apps-signin"
  target_key_id = aws_kms_key.apps_signin.key_id
}

# One-time secrets (agents/primary/internal/secretshares). When the answer to a
# Slack request is a credential, its owner types it into Oryxa and the member
# who asked opens it once; it waits in between, for an hour at most, sealed
# under this key with envelope encryption. Each secret gets a data key of its
# own, bound to the request it was shared for: the encryption context is
# service=oryxa-agents, resource_type=secret_share and resource_id=<request>
# (internal/secrets.Envelope), so a sealed value opens for its own request
# alone. The value is never in Slack, an event, the database in the clear or a
# log line.
#
# Nothing sealed under it outlives its hour, so losing it would cost only the
# secrets waiting at that moment, which their owners share again. It is kept as
# the keys above are all the same: a 30-day deletion window, the longest AWS
# allows, and no ordinary apply can destroy it.
resource "aws_kms_key" "agents_secrets" {
  description             = "${local.name} agents: envelope encryption of one-time secrets shared between members"
  enable_key_rotation     = true
  deletion_window_in_days = 30

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "agents_secrets" {
  name          = "alias/${local.name}-agents-secrets"
  target_key_id = aws_kms_key.agents_secrets.key_id
}
