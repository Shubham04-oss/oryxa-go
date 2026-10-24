# Sandbox previews' own registrable domain (#474). A preview serves pages a
# sandbox made; on a host under oryxa.in, their script could set cookies that
# app. and api. trust. The edge Worker serves previews only at
# EDGE_PREVIEW_URL (edge/wrangler.jsonc): preview.oryxa.uk for production,
# staging-preview.oryxa.uk for staging. oryxa.uk is already a zone in the
# same Cloudflare account, so preview_domain stays empty and no zone is made
# here; wrangler makes the custom domains and their DNS records on deploy, as
# it does edge.oryxa.in's. Nothing that signs people in may ever live under
# oryxa.uk: previews can set its cookies. See edge/DEPLOY.md, "Previews'
# origin".
variable "preview_domain" {
  description = "A preview domain to make a zone for. Empty (the default) makes none: oryxa.uk, which previews use, is already a zone."
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
