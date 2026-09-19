# Vertex AI serves Gemini and Memory Bank. The oryxa tasks reach it without a
# key: Workload Identity Federation trusts their AWS task role, and they act as
# one service account (internal/gcpauth). Nothing in AWS holds a GCP secret.

data "google_project" "current" {}

resource "google_project_service" "vertex" {
  for_each           = toset(["aiplatform.googleapis.com", "iam.googleapis.com", "iamcredentials.googleapis.com", "sts.googleapis.com"])
  service            = each.value
  disable_on_destroy = false
}

resource "google_iam_workload_identity_pool" "aws" {
  workload_identity_pool_id = "oryxa-aws"
  display_name              = "Oryxa on AWS"
  description               = "The oryxa tasks in AWS account ${local.account_id}"
}

resource "google_iam_workload_identity_pool_provider" "aws" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.aws.workload_identity_pool_id
  workload_identity_pool_provider_id = "aws-${local.account_id}"
  display_name                       = "AWS ${local.account_id}"

  aws {
    account_id = local.account_id
  }

  # The role a caller assumed, without its session name, so a grant can name
  # the role rather than one task.
  attribute_mapping = {
    "google.subject"     = "assertion.arn"
    "attribute.aws_role" = "assertion.arn.contains('assumed-role') ? assertion.arn.extract('{account_arn}assumed-role/') + 'assumed-role/' + assertion.arn.extract('assumed-role/{role_name}/') : assertion.arn"
  }
  # Only the oryxa task role of this account is admitted at all.
  attribute_condition = "attribute.aws_role == 'arn:aws:sts::${local.account_id}:assumed-role/${aws_iam_role.task.name}'"
}

resource "google_service_account" "runtime" {
  account_id   = "${local.name}-runtime"
  display_name = "oryxa ${var.environment} (AWS tasks)"
}

resource "google_project_iam_member" "runtime" {
  for_each = toset(["roles/aiplatform.user"])
  project  = data.google_project.current.project_id
  role     = each.value
  member   = "serviceAccount:${google_service_account.runtime.email}"
}

resource "google_service_account_iam_member" "runtime_federation" {
  service_account_id = google_service_account.runtime.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.aws.name}/attribute.aws_role/arn:aws:sts::${local.account_id}:assumed-role/${aws_iam_role.task.name}"
}

locals {
  gcp_workload_identity_audience = "//iam.googleapis.com/${google_iam_workload_identity_pool_provider.aws.name}"
}
