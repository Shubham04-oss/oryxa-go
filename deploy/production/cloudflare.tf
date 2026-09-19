# The edge. app. serves the web app from Workers, except /v1, which Cloudflare
# sends through a tunnel to the oryxa tasks: the web app calls its API on its
# own origin, so its cookies stay first-party. api. goes straight to the tasks,
# for Composio's webhooks and billing's. A second tunnel, from the Hyperdrive
# connector instance, carries Hyperdrive to the database.

data "cloudflare_zone" "main" {
  filter = { name = var.domain }
}

variable "web_worker_name" {
  description = "The web app's Worker, once `pnpm deploy` has uploaded it; its route is made only then."
  type        = string
  default     = ""
}

# API tunnel: its connectors are the cloudflared sidecars in the oryxa tasks.
resource "cloudflare_zero_trust_tunnel_cloudflared" "api" {
  account_id = var.cloudflare_account_id
  name       = "${local.name}-api"
  config_src = "cloudflare"
}

resource "cloudflare_zero_trust_tunnel_cloudflared_config" "api" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.api.id
  config = {
    ingress = [
      { hostname = local.app_host, path = "^/v1/", service = "http://localhost:8080" },
      { hostname = local.api_host, service = "http://localhost:8080" },
      { service = "http_status:404" },
    ]
  }
}

data "cloudflare_zero_trust_tunnel_cloudflared_token" "api" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.api.id
}

resource "aws_secretsmanager_secret" "api_tunnel_token" {
  name                    = "${local.name}/cloudflared-api-token"
  description             = "Runs the API tunnel's connector in the oryxa tasks"
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "api_tunnel_token" {
  secret_id     = aws_secretsmanager_secret.api_tunnel_token.id
  secret_string = data.cloudflare_zero_trust_tunnel_cloudflared_token.api.token
}

resource "cloudflare_dns_record" "tunnel" {
  for_each = toset([local.app_host, local.api_host])
  zone_id  = data.cloudflare_zone.main.zone_id
  name     = each.value
  type     = "CNAME"
  content  = "${cloudflare_zero_trust_tunnel_cloudflared.api.id}.cfargotunnel.com"
  proxied  = true
  ttl      = 1
  comment  = "${local.name}: oryxa through the API tunnel"
}

# app.*/v1/* is sent to the origin, the tunnel, rather than to the Worker.
resource "cloudflare_workers_route" "app_api" {
  zone_id = data.cloudflare_zone.main.zone_id
  pattern = "${local.app_host}/v1/*"
}

resource "cloudflare_workers_route" "app" {
  count   = var.web_worker_name == "" ? 0 : 1
  zone_id = data.cloudflare_zone.main.zone_id
  pattern = "${local.app_host}/*"
  script  = var.web_worker_name
}

# Database tunnel, for Hyperdrive.
resource "cloudflare_zero_trust_tunnel_cloudflared" "db" {
  account_id = var.cloudflare_account_id
  name       = "${local.name}-db"
  config_src = "cloudflare"
}

resource "cloudflare_zero_trust_tunnel_cloudflared_config" "db" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.db.id
  config = {
    ingress = [
      { hostname = local.db_host, service = "tcp://${aws_db_instance.main.address}:5432" },
      { service = "http_status:404" },
    ]
  }
}

data "cloudflare_zero_trust_tunnel_cloudflared_token" "db" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.db.id
}

resource "aws_secretsmanager_secret" "db_tunnel_token" {
  name                    = "${local.name}/cloudflared-db-token"
  description             = "Runs the database tunnel's connector on the Hyperdrive instance"
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "db_tunnel_token" {
  secret_id     = aws_secretsmanager_secret.db_tunnel_token.id
  secret_string = data.cloudflare_zero_trust_tunnel_cloudflared_token.db.token
}

resource "cloudflare_dns_record" "db" {
  zone_id = data.cloudflare_zone.main.zone_id
  name    = local.db_host
  type    = "CNAME"
  content = "${cloudflare_zero_trust_tunnel_cloudflared.db.id}.cfargotunnel.com"
  proxied = true
  ttl     = 1
  comment = "${local.name}: the database, for Hyperdrive only"
}

resource "cloudflare_zero_trust_access_service_token" "hyperdrive" {
  account_id = var.cloudflare_account_id
  name       = "${local.name}-hyperdrive"
  duration   = "8760h"
}

resource "cloudflare_zero_trust_access_policy" "hyperdrive" {
  account_id = var.cloudflare_account_id
  name       = "${local.name}-hyperdrive"
  decision   = "non_identity"
  include    = [{ service_token = { token_id = cloudflare_zero_trust_access_service_token.hyperdrive.id } }]
}

resource "cloudflare_zero_trust_access_application" "hyperdrive" {
  account_id       = var.cloudflare_account_id
  name             = "${local.name}-hyperdrive-db"
  domain           = local.db_host
  type             = "self_hosted"
  session_duration = "24h"
  policies         = [{ id = cloudflare_zero_trust_access_policy.hyperdrive.id, precedence = 1 }]
}

resource "cloudflare_hyperdrive_config" "main" {
  account_id = var.cloudflare_account_id
  name       = local.name
  origin = {
    host                 = local.db_host
    database             = "oryxa"
    user                 = "oryxa"
    password             = random_password.database.result
    scheme               = "postgres"
    access_client_id     = cloudflare_zero_trust_access_service_token.hyperdrive.client_id
    access_client_secret = cloudflare_zero_trust_access_service_token.hyperdrive.client_secret
  }

  depends_on = [cloudflare_zero_trust_access_application.hyperdrive, cloudflare_zero_trust_tunnel_cloudflared_config.db]
}
