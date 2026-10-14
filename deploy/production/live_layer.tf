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
