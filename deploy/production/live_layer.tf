# The live layer's run records (docs/design/live-layer, F4): the edge Worker
# (edge/) writes every agent and live run here, and agents' artifacts can
# move here too (AGENTS_PRIMARY_ARTIFACT_ENDPOINT). Staging has its own
# bucket so its runs never mix with production's. The edge Worker itself,
# its routes, vars and secrets are deployed with wrangler from edge/; see
# edge/DEPLOY.md.
resource "cloudflare_r2_bucket" "runs" {
  account_id = var.cloudflare_account_id
  name       = "oryxa-runs"
}

resource "cloudflare_r2_bucket" "runs_staging" {
  account_id = var.cloudflare_account_id
  name       = "oryxa-runs-staging"
}

# Sandbox workspace backups (edge/src/sandbox, A17) are replaced at every idle
# stop and only restore that one sandbox, so they expire; everything else in
# the bucket (runs/, calls/, artifacts/) is kept. R2 rules match fixed prefixes.
resource "cloudflare_r2_bucket_lifecycle" "runs" {
  for_each = {
    production = cloudflare_r2_bucket.runs.name
    staging    = cloudflare_r2_bucket.runs_staging.name
  }
  account_id  = var.cloudflare_account_id
  bucket_name = each.value
  rules = [{
    id         = "expire-sandbox-backups"
    enabled    = true
    conditions = { prefix = "backups/sandbox/" }
    delete_objects_transition = {
      condition = { type = "Age", max_age = 30 * 24 * 60 * 60 }
    }
  }]
}
