# Auth seals agent grants with envelope encryption; agents, knowledge and rag
# each prove which service is calling with an ES256 signature over its own key
# (internal/serviceauth), which the service it calls checks with the public
# half.

resource "aws_kms_key" "auth" {
  description             = "${local.name} auth: envelope encryption of agent grants"
  enable_key_rotation     = true
  deletion_window_in_days = 7
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
  deletion_window_in_days  = 7
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
