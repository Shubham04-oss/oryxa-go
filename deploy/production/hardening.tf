# What the 2026-09-28 launch review found missing (docs/REMAINING-WORK.md,
# section 7), kept in one file so it can be read, reviewed and applied as one
# change. It adds recovery, alarms and rules at the edge around the service,
# and touches neither its code nor its configuration.

variable "first_deploy" {
  description = "True only for the first apply into an empty account (deploy/README.md, First deploy). deploy/scripts/provision-app.sh makes the files and artifacts buckets after that apply, so their versioning and lifecycle rules wait for the next one, and RDS has not yet made the database's log groups, so they are created here rather than adopted."
  type        = bool
  default     = false
}

# Recoverable files.
#
# A deleted or overwritten object in the files, artifacts and knowledge-base
# documents buckets stays recoverable for 30 days. The buckets are versioned,
# so the task's DeleteObject only lays a delete marker over the newest version
# (the task role holds no s3:DeleteObjectVersion), and a lifecycle rule removes
# the versions a delete or an overwrite left behind 30 days later. That adds
# to the expiry some objects already have: a file the 90-day rule expires, or
# an artifact the 30-day rule does, is kept 30 days more after it. Getting an
# object back is in deploy/README.md, "Recovering a deleted file".
locals {
  deleted_object_retention_days = 30

  # The buckets deploy/scripts/provision-app.sh makes, named in main.tf.
  # OpenTofu only configures them, so they must exist: a first deploy's first
  # apply comes before the provisioner, and leaves them out.
  provisioned_buckets = var.first_deploy ? {} : {
    files     = local.files_bucket
    artifacts = local.artifacts_bucket
  }
}

resource "aws_s3_bucket_versioning" "provisioned" {
  for_each = local.provisioned_buckets
  bucket   = each.value

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_versioning" "knowledge_base_documents" {
  bucket = aws_s3_bucket.knowledge_base_documents.id

  versioning_configuration {
    status = "Enabled"
  }
}

# S3 keeps one lifecycle configuration per bucket, and putting one replaces
# it whole. The files and artifacts buckets already have one, which the
# provisioners put again on every run of provision-app.sh
# (files/infra/storage/internal/provision, agents/primary/infra/artifacts),
# so the two below carry each provisioner's own rule as it writes it, beside
# the rule that clears up after versioning. Keep those rules equal to the
# provisioners' constants (lifecycleExpireDays, expireDays and
# abortMultipartDays). A provisioner run puts its rule alone back, which
# leaves deleted versions unexpired until the next `tofu apply`, so apply
# after provision-app.sh until the provisioners write the rule too.
#
# The rule everywhere: versions a delete or overwrite left behind expire
# after 30 days, and a delete marker whose versions have all gone is removed,
# so that it does not pile up in listings. Unfinished multipart uploads need no
# rule of their own: every bucket already drops them after a day, which is
# sooner than the 7 days a looser rule would allow (files' rule covers org/,
# where every key it writes begins). The knowledge-base documents bucket,
# which OpenTofu creates, has the same rule in knowledge_base.tf, beside the
# two it already had.
resource "aws_s3_bucket_lifecycle_configuration" "files" {
  count  = var.first_deploy ? 0 : 1
  bucket = local.files_bucket

  rule {
    id     = "expire-objects"
    status = "Enabled"
    filter {
      prefix = "org/"
    }
    expiration {
      days = 90
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }

  rule {
    id     = "recoverable-deletes"
    status = "Enabled"
    filter {}
    noncurrent_version_expiration {
      noncurrent_days = local.deleted_object_retention_days
    }
    expiration {
      expired_object_delete_marker = true
    }
  }

  depends_on = [aws_s3_bucket_versioning.provisioned]
}

resource "aws_s3_bucket_lifecycle_configuration" "artifacts" {
  count  = var.first_deploy ? 0 : 1
  bucket = local.artifacts_bucket

  rule {
    id     = "expire-artifacts"
    status = "Enabled"
    filter {}
    expiration {
      days = 30
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }

  rule {
    id     = "recoverable-deletes"
    status = "Enabled"
    filter {}
    noncurrent_version_expiration {
      noncurrent_days = local.deleted_object_retention_days
    }
    expiration {
      expired_object_delete_marker = true
    }
  }

  depends_on = [aws_s3_bucket_versioning.provisioned]
}

# Alarms on the application's own errors, and on what dashboard.tf only
# graphs. They mail the same SNS topic as every alarm in observability.tf.
#
# The application's errors: one metric filter counts every log line at level
# ERROR in the app log group (the dashboard's Errors table reads the same
# lines), and the alarm asks for 3 in 15 minutes, against a baseline of about
# one every 9 hours. A quiet log group publishes nothing, which is fine, not a
# fault.
resource "aws_cloudwatch_log_metric_filter" "app_errors" {
  name           = "${local.name}-app-errors"
  log_group_name = aws_cloudwatch_log_group.task["app"].name
  pattern        = "{ $.level = \"ERROR\" }"

  metric_transformation {
    name      = "AppErrors"
    namespace = "Oryxa/Logs"
    value     = "1"
  }
}

resource "aws_cloudwatch_metric_alarm" "app_errors" {
  alarm_name          = "${local.name}-app-errors"
  alarm_description   = "oryxa logged 3 or more errors in 15 minutes; about one every 9 hours is normal. The dashboard's Errors table lists them with their trace ids."
  namespace           = aws_cloudwatch_log_metric_filter.app_errors.metric_transformation[0].namespace
  metric_name         = aws_cloudwatch_log_metric_filter.app_errors.metric_transformation[0].name
  statistic           = "Sum"
  period              = 900
  evaluation_periods  = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = 3
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}

# Mail, from the SES region: the metrics the dashboard's mail graph draws. The
# alarms sit in that region and notify the topic in this one, as AWS's
# guidance for alarms across Regions describes, and the topic's default policy
# admits publishers from this account. After the first apply, set one to
# ALARM to see the mail arrive:
#   aws cloudwatch set-alarm-state --region ap-south-1 --alarm-name <name> \
#     --state-value ALARM --state-reason test
# A complaint is rare and counts against the account's sending reputation, so
# one is enough; a bounce is usually a mistyped address, so it takes a few in
# an hour.
resource "aws_cloudwatch_metric_alarm" "ses" {
  provider = aws.ses
  for_each = {
    complaints = { metric = "Complaint", threshold = 1, description = "An email was reported as spam. Complaints count against the account's sending reputation." }
    bounces    = { metric = "Bounce", threshold = 3, description = "3 or more emails bounced in an hour, which puts SES's production access at risk if it goes on." }
  }
  alarm_name          = "${local.name}-ses-${each.key}"
  alarm_description   = each.value.description
  namespace           = "AWS/SES"
  metric_name         = each.value.metric
  statistic           = "Sum"
  period              = 3600
  evaluation_periods  = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = each.value.threshold
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}

# EventBridge gave up delivering an event to a target: a consumer's queue
# never got it, and nothing else says so. EventBridge publishes
# FailedInvocations only when it is not zero. An alarm cannot use the SEARCH
# the dashboard's graph does, so the first asks a Metrics Insights query for
# the sum of the metric over every rule on every bus in the region. That holds
# the dashboard's series (EventBusName and RuleName) and does not depend on
# which dimensions EventBridge puts on a custom bus's rules: its reference
# lists RuleName alone for this metric, and a dimension set that missed would
# leave the alarm silent. The second watches the one rule OpenTofu makes, the
# SES forwarder on the SES region's default bus, where the metric's only
# dimension is RuleName.
resource "aws_cloudwatch_metric_alarm" "event_deliveries_failed" {
  alarm_name          = "${local.name}-eventbridge-failed-invocations"
  alarm_description   = "EventBridge gave up delivering an event to a target, so a consumer's queue never got it. Look at the rule's target and the queue's policy."
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 1
  threshold           = 0
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions

  metric_query {
    id          = "failed"
    expression  = "SELECT SUM(FailedInvocations) FROM \"AWS/Events\""
    period      = 300
    return_data = true
  }
}

resource "aws_cloudwatch_metric_alarm" "ses_feedback_forwarding_failed" {
  provider            = aws.ses
  alarm_name          = "${local.name}-ses-feedback-forwarding"
  alarm_description   = "SES's bounce and complaint events are not reaching ${var.aws_region}'s bus, so email's suppression list stops learning of them."
  namespace           = "AWS/Events"
  metric_name         = "FailedInvocations"
  dimensions          = { RuleName = aws_cloudwatch_event_rule.ses_forward.name }
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}

# Bedrock embeds, reranks and answers for rag. A throttled call is refused,
# not queued, and the SDK's own retries hide a single one, so this asks for
# throttling in two 5-minute periods running, over every model and dimension
# set, which includes the dashboard's search on ModelId.
resource "aws_cloudwatch_metric_alarm" "bedrock_throttles" {
  alarm_name          = "${local.name}-bedrock-throttles"
  alarm_description   = "Bedrock throttled calls in two 5-minute periods running: rag's embedding, reranking or answering is being refused for capacity or quota."
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 2
  datapoints_to_alarm = 2
  threshold           = 0
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions

  metric_query {
    id          = "throttles"
    expression  = "SELECT SUM(InvocationThrottles) FROM \"AWS/Bedrock\""
    period      = 300
    return_data = true
  }
}

# The database's log groups. RDS makes one for each log it exports to
# CloudWatch (database_log_exports, in database.tf), named for the instance,
# and leaves it without an expiry. Declaring them here gives each 30 days, and
# in a first deploy makes them before the instance, so RDS finds them. In an
# account that has them already, which production has, the import blocks
# adopt them instead of failing on the name: an import is refused for an
# object that does not exist, so a first deploy leaves them out, and one that
# is already in state is left alone, so they can stay after the first apply.
resource "aws_cloudwatch_log_group" "database" {
  for_each          = toset(local.database_log_exports)
  name              = "/aws/rds/instance/${local.name}/${each.key}"
  retention_in_days = 30
}

import {
  for_each = var.first_deploy ? toset([]) : toset(local.database_log_exports)
  to       = aws_cloudwatch_log_group.database[each.key]
  id       = "/aws/rds/instance/${local.name}/${each.key}"
}

# /v1/internal at the edge. The routes under it are for one service to call
# another: auth's agent-token exchange, knowledge's document bodies, and
# apps' operator routes for the deployment's webhooks. Each refuses a caller
# without a signed service token, but nothing needs them reachable from the
# internet, and the tunnel forwards every path on both hosts, so this stops
# them at Cloudflare before they get there.
#
# Nothing legitimate is sent through Cloudflare to them (checked 2026-09-29):
# every service calls its peers on this process's own address (internal/host
# defaults each *_URL to it, and compute.tf sets none), web-next and web call
# only member-facing routes, and Composio, Dodo and Cognito call their own
# webhook and callback paths. The Worker's /internal/realtime/purge is a
# different path, served by the Worker, and is not matched. Apps' operator
# routes exist only where APPS_SERVICE_CALLERS is set, which production does
# not; an operator who turns that on would call them from inside the task
# (ECS Exec), not through here.
#
# Cloudflare's URL normalization, on by default, makes the rule see an
# encoded path (/v1/%69nternal) as the plain one. A zone has one entry-point
# ruleset for this phase, and OpenTofu takes it over whole: see deploy/README.md,
# "Applying the hardening", for what to check first.
resource "cloudflare_ruleset" "custom_rules" {
  zone_id     = data.cloudflare_zone.main.zone_id
  name        = "${local.name} custom rules"
  description = "Managed by OpenTofu (deploy/production/hardening.tf)."
  kind        = "zone"
  phase       = "http_request_firewall_custom"

  rules = [{
    ref         = "block_v1_internal"
    description = "Service-to-service routes are not for the internet"
    action      = "block"
    enabled     = true
    expression  = "(http.host in {\"${local.app_host}\" \"${local.api_host}\"} and starts_with(http.request.uri.path, \"/v1/internal\"))"
  }]
}

# Password sign-in at the edge. auth has no limit of its own on
# /v1/auth/password/ (sign-up, verify, resend, login, forgot and reset).
# Cognito locks one user out after repeated wrong passwords, but nothing slows
# one address that tries many users, or asks for verification and reset mail
# for any address, as fast as the tasks answer. This blocks a client IP that
# asks more than var.signin_requests_per_period times in 10 seconds, for 10
# seconds.
#
# Those numbers are the Free plan's only ones, and the zone's plan is not
# known here, so the rule stays inside them: one rule, counted per client IP
# (ip.src, with the data center Cloudflare requires beside it), a 10-second
# window and a 10-second block, and an expression on the path alone, since
# Free has no method or host to match on (Pro adds the host, Business the
# method). The path is enough: every route under /v1/auth/password/ is a POST
# (auth/internal/httpserver/server.go), so it selects the same requests.
#
# A paid plan allows longer windows, which suit a sign-in limit better. Pro
# counts up to a minute and blocks for up to an hour, Business counts up to
# ten minutes: then a handful of attempts a minute, blocked for an hour, is
# the setting to want (period and mitigation_timeout below, and the variable).
# A blocked request gets Cloudflare's own 429 page.
#
# This is the zone's entry-point ruleset for http_ratelimit, and the Free plan
# allows one rate limiting rule, so it is the only one there can be.
variable "signin_requests_per_period" {
  description = "How many requests to /v1/auth/password/ one client IP may make in 10 seconds, at one Cloudflare data center, before it is blocked for 10 seconds. Ten seconds is the only window and block the Free plan allows; a paid plan allows longer ones (deploy/production/hardening.tf)."
  type        = number
  default     = 10

  validation {
    condition     = var.signin_requests_per_period >= 1
    error_message = "signin_requests_per_period is at least 1."
  }
}

resource "cloudflare_ruleset" "rate_limits" {
  zone_id     = data.cloudflare_zone.main.zone_id
  name        = "${local.name} rate limits"
  description = "Managed by OpenTofu (deploy/production/hardening.tf)."
  kind        = "zone"
  phase       = "http_ratelimit"

  rules = [{
    ref         = "limit_password_sign_in"
    description = "Password sign-up, sign-in and reset attempts, per client IP"
    action      = "block"
    enabled     = true
    expression  = "starts_with(http.request.uri.path, \"/v1/auth/password/\")"
    ratelimit = {
      characteristics     = ["cf.colo.id", "ip.src"]
      period              = 10
      requests_per_period = var.signin_requests_per_period
      mitigation_timeout  = 10
    }
  }]
}
