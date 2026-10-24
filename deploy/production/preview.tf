# Sandbox previews' own registrable domain (#474). A preview serves pages a
# sandbox made; on a host under oryxa.in, their script could set cookies that
# app. and api. trust. The edge Worker serves previews only at
# EDGE_PREVIEW_URL (edge/wrangler.jsonc): oryxa-preview.net for production,
# staging.oryxa-preview.net for staging. Wrangler makes those custom domains
# and their DNS records on deploy, as it does edge.oryxa.in's, so this file
# holds only the zone they need. It is made once the domain is registered:
# see edge/DEPLOY.md, "Previews' origin".
variable "preview_domain" {
  description = "Sandbox previews' registrable domain, once registered (oryxa-preview.net). Empty makes no zone (deploy/production/preview.tf)."
  type        = string
  default     = ""

  validation {
    condition     = var.preview_domain == "" || !endswith(var.preview_domain, var.domain)
    error_message = "preview_domain must not be under the main domain: previews could set its cookies."
  }
}

resource "cloudflare_zone" "preview" {
  count   = var.preview_domain == "" ? 0 : 1
  account = { id = var.cloudflare_account_id }
  name    = var.preview_domain
  type    = "full"
}

output "preview_name_servers" {
  description = "Where the preview domain's registrar should point it, unless it is registered with Cloudflare."
  value       = one(cloudflare_zone.preview[*].name_servers)
}
