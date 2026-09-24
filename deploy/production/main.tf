data "aws_caller_identity" "current" {}

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  name       = "oryxa-${var.environment}"
  account_id = data.aws_caller_identity.current.account_id

  tags = {
    Application = "oryxa"
    Environment = var.environment
    ManagedBy   = "opentofu"
  }

  app_host = "app.${var.domain}"
  api_host = "api.${var.domain}"
  # Hyperdrive reaches the database through this hostname, which Cloudflare
  # Access admits only Hyperdrive's service token to.
  db_host = "hyperdrive-db.${var.domain}"

  # Each service's event queue. The service provisioners create them (so their
  # rule patterns stay generated from the code that consumes them); the names
  # are fixed here so the task's configuration and IAM can refer to them.
  queue_services = ["agents", "apps", "auth", "batching", "billing", "files", "knowledge", "rag", "realtime", "teams"]
  queue_names = merge(
    { for service in local.queue_services : service => "${local.name}-${service}-events" },
    { email = "${local.name}-email-events" },
  )
  queue_urls = { for service, queue in local.queue_names : service => "https://sqs.${var.aws_region}.amazonaws.com/${local.account_id}/${queue}" }

  # Buckets the service provisioners create, named here for the same reason.
  files_bucket     = "${local.name}-files-${local.account_id}"
  artifacts_bucket = "${local.name}-artifacts-${local.account_id}"
  vectors_bucket   = "${local.name}-vectors-${local.account_id}"

  email_deliveries_table   = "${local.name}-email-deliveries"
  email_suppressions_table = "${local.name}-email-suppressions"
  ses_configuration_set    = "${local.name}-email"
}
