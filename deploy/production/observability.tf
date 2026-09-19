# Where everything the deployment reports goes:
#   - traces: the oryxa process → the collector sidecar (OTLP) → X-Ray;
#   - metrics: the process's OTel metrics → the collector → CloudWatch
#     (embedded metric format, namespace Oryxa), beside what AWS reports for
#     ECS (Container Insights), RDS (Enhanced Monitoring, Database Insights)
#     and SQS;
#   - logs: every container's JSON logs → CloudWatch Logs, carrying trace ids;
#   - alarms: → SNS → email.
# The dashboard (dashboard.tf) puts the latency of every hop on one page.

# CloudWatch bills every metric series, and every attribute of a data point
# would otherwise be a dimension, so only these series are kept: each metric's
# name matched by a selector, under each set of dimensions listed with it.
# service.name is the process ("oryxa") or, for what the collector scrapes,
# the scrape job.
locals {
  metric_declarations = [
    {
      # The API: the whole service, each route, and each status.
      dimensions            = [["service.name"], ["service.name", "http.route", "http.request.method"], ["service.name", "http.response.status_code"]]
      metric_name_selectors = ["^http\\.server\\.request\\.duration$"]
    },
    {
      # What the process calls over HTTP: other services, Google, Composio.
      dimensions            = [["service.name"], ["service.name", "server.address"]]
      metric_name_selectors = ["^http\\.client\\.request\\.duration$"]
    },
    {
      # The Go runtime and the database pool.
      dimensions            = [["service.name"]]
      metric_name_selectors = ["^go\\.", "^db\\.client\\.connection"]
    },
    {
      # cloudflared, the tunnel sidecar, scraped from its metrics endpoint.
      dimensions            = [["service.name"], ["service.name", "status_code"]]
      metric_name_selectors = ["^cloudflared_tunnel_(total_requests|request_errors|response_by_code|ha_connections|concurrent_requests_per_tunnel)$"]
    },
  ]
}

resource "aws_ssm_parameter" "otel_config" {
  name = "/${local.name}/otel-collector-config"
  type = "String"
  tier = "Advanced" # the config outgrows Standard's 4 KB
  value = yamlencode({
    extensions = { health_check = {} }
    receivers = {
      otlp = { protocols = {
        grpc = { endpoint = "127.0.0.1:4317" }
        http = { endpoint = "127.0.0.1:4318" }
      } }
      # The tunnel's own view: requests Cloudflare handed over, their
      # statuses, and the connections to Cloudflare's edge.
      prometheus = { config = { scrape_configs = [{
        job_name        = "cloudflared"
        scrape_interval = "60s"
        static_configs  = [{ targets = ["127.0.0.1:2000"] }]
      }] } }
    }
    processors = {
      # override = false keeps what the process says about itself, such as
      # the log group X-Ray links a trace's logs from.
      resourcedetection = { detectors = ["env", "ecs"], timeout = "2s", override = false }
      "batch/traces"    = { timeout = "1s", send_batch_size = 50 }
      "batch/metrics"   = { timeout = "60s" }
    }
    exporters = {
      awsxray = { region = var.aws_region }
      awsemf = {
        region                           = var.aws_region
        namespace                        = "Oryxa"
        log_group_name                   = aws_cloudwatch_log_group.metrics.name
        log_stream_name                  = "{TaskId}"
        dimension_rollup_option          = "NoDimensionRollup"
        resource_to_telemetry_conversion = { enabled = true }
        metric_declarations              = local.metric_declarations
      }
    }
    service = {
      extensions = ["health_check"]
      pipelines = {
        traces  = { receivers = ["otlp"], processors = ["resourcedetection", "batch/traces"], exporters = ["awsxray"] }
        metrics = { receivers = ["otlp", "prometheus"], processors = ["resourcedetection", "batch/metrics"], exporters = ["awsemf"] }
      }
    }
  })
}

resource "aws_sns_topic" "alarms" {
  name = "${local.name}-alarms"
}

resource "aws_sns_topic_subscription" "alarms_email" {
  topic_arn = aws_sns_topic.alarms.arn
  protocol  = "email"
  endpoint  = var.alarm_email
}

locals {
  alarm_actions = [aws_sns_topic.alarms.arn]
}

# The service itself.
resource "aws_cloudwatch_metric_alarm" "service_running" {
  alarm_name          = "${local.name}-no-running-task"
  alarm_description   = "No oryxa task is running: the API and every worker are down."
  namespace           = "ECS/ContainerInsights"
  metric_name         = "RunningTaskCount"
  dimensions          = { ClusterName = aws_ecs_cluster.main.name, ServiceName = aws_ecs_service.oryxa.name }
  statistic           = "Minimum"
  period              = 60
  evaluation_periods  = 3
  comparison_operator = "LessThanThreshold"
  threshold           = var.desired_count > 0 ? 1 : 0
  treat_missing_data  = "breaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}

resource "aws_cloudwatch_metric_alarm" "service_pressure" {
  for_each            = { cpu = "CPUUtilization", memory = "MemoryUtilization" }
  alarm_name          = "${local.name}-service-${each.key}"
  alarm_description   = "oryxa's ${each.key} has stayed above 80% for 10 minutes."
  namespace           = "AWS/ECS"
  metric_name         = each.value
  dimensions          = { ClusterName = aws_ecs_cluster.main.name, ServiceName = aws_ecs_service.oryxa.name }
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 2
  comparison_operator = "GreaterThanThreshold"
  threshold           = 80
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}

# The database.
resource "aws_cloudwatch_metric_alarm" "database" {
  for_each = {
    cpu         = { metric = "CPUUtilization", op = "GreaterThanThreshold", threshold = 80, statistic = "Average", description = "Database CPU above 80% for 10 minutes." }
    storage     = { metric = "FreeStorageSpace", op = "LessThanThreshold", threshold = 2147483648, statistic = "Minimum", description = "Under 2 GiB of database storage left." }
    connections = { metric = "DatabaseConnections", op = "GreaterThanThreshold", threshold = 90, statistic = "Maximum", description = "Close to the instance's connection limit." }
    memory      = { metric = "FreeableMemory", op = "LessThanThreshold", threshold = 67108864, statistic = "Minimum", description = "Under 64 MiB of database memory free." }
  }
  alarm_name          = "${local.name}-database-${each.key}"
  alarm_description   = each.value.description
  namespace           = "AWS/RDS"
  metric_name         = each.value.metric
  dimensions          = { DBInstanceIdentifier = aws_db_instance.main.identifier }
  statistic           = each.value.statistic
  period              = 300
  evaluation_periods  = 2
  comparison_operator = each.value.op
  threshold           = each.value.threshold
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}

# Every queue: nothing should sit unread, and nothing should reach a
# dead-letter queue unnoticed.
resource "aws_cloudwatch_metric_alarm" "queue_age" {
  for_each            = local.queue_names
  alarm_name          = "${each.value}-backlog"
  alarm_description   = "${each.key}'s events have waited over 10 minutes: its consumer is stuck or down."
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateAgeOfOldestMessage"
  dimensions          = { QueueName = each.value }
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 2
  comparison_operator = "GreaterThanThreshold"
  threshold           = 600
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}

resource "aws_cloudwatch_metric_alarm" "dead_letters" {
  for_each            = local.queue_names
  alarm_name          = "${each.value}-dead-letters"
  alarm_description   = "An event ${each.key} could not handle reached its dead-letter queue."
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  dimensions          = { QueueName = "${each.value}-dlq" }
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}

# The Hyperdrive connector.
resource "aws_cloudwatch_metric_alarm" "edge_status" {
  alarm_name          = "${local.name}-hyperdrive-connector"
  alarm_description   = "The Hyperdrive connector instance is failing its status checks; it recovers itself."
  namespace           = "AWS/EC2"
  metric_name         = "StatusCheckFailed_System"
  dimensions          = { InstanceId = aws_instance.edge.id }
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 2
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0
  alarm_actions       = concat(local.alarm_actions, ["arn:aws:automate:${var.aws_region}:ec2:recover"])
}
