output "name" {
  description = "What every resource's name starts with."
  value       = local.name
}

output "gcp_project" {
  value = var.gcp_project
}

output "gcp_region" {
  value = var.gcp_region
}

output "model" {
  value = var.model
}

output "ecr_repository" {
  value = aws_ecr_repository.oryxa.repository_url
}

output "cluster" {
  value = aws_ecs_cluster.main.name
}

output "service" {
  value = aws_ecs_service.oryxa.name
}

output "event_bus" {
  value = aws_cloudwatch_event_bus.main.name
}

output "database_address" {
  value = aws_db_instance.main.address
}

output "knowledge_base_id" {
  description = "The knowledge base rag uses."
  value       = local.rag_knowledge_base.id
}

output "knowledge_base_data_source_id" {
  value = local.rag_knowledge_base.data_source_id
}

output "hyperdrive_id" {
  description = "For the web app's wrangler.jsonc hyperdrive binding."
  value       = cloudflare_hyperdrive_config.main.id
}

output "gcp_service_account" {
  value = google_service_account.runtime.email
}

output "gcp_workload_identity_audience" {
  value = local.gcp_workload_identity_audience
}

# What deploy/scripts/provision-app.sh hands the service provisioners.
output "provisioner_environment" {
  value = {
    AWS_REGION                     = var.aws_region
    EVENT_BUS_NAME                 = aws_cloudwatch_event_bus.main.name
    SES_REGION                     = var.ses_region
    SES_CONFIGURATION_SET          = local.ses_configuration_set
    EMAIL_FROM_ADDRESS             = "no-reply@${var.domain}"
    EMAIL_DELIVERIES_TABLE         = local.email_deliveries_table
    EMAIL_SUPPRESSIONS_TABLE       = local.email_suppressions_table
    EMAIL_QUEUE_NAME               = local.queue_names["email"]
    FILES_BUCKET_NAME              = local.files_bucket
    AGENTS_PRIMARY_ARTIFACT_BUCKET = local.artifacts_bucket
    RAG_VECTOR_BUCKET_NAME         = local.vectors_bucket
    APP_URL                        = "https://${local.app_host}"
    API_URL                        = "https://${local.api_host}"
    QUEUE_NAMES                    = jsonencode(local.queue_names)
    COGNITO_POOL_NAME              = "${local.name}-users"
    COGNITO_APP_CLIENT_NAME        = "${local.name}-web"
    COGNITO_DOMAIN_PREFIX          = local.name
  }
}
