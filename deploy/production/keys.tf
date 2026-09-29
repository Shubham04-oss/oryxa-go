# Auth seals agent grants with envelope encryption; agents, knowledge and rag
# each prove which service is calling with an ES256 signature over its own key
# (internal/serviceauth), which the service it calls checks with the public
# half.
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
