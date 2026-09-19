# One page for the whole deployment, top to bottom in the order a request
# travels: Cloudflare's tunnel into the task, the API, the process, the
# database, the event pipeline to the services that consume it, the models,
# and mail. Every graph is a minute or five apart; X-Ray has the single
# request, and the error log at the bottom links to it by trace id.

locals {
  dashboard_region = var.aws_region
  # Search terms match the words of a name: every queue is named
  # <name>-<service>-events, and its dead-letter queue adds -dlq.
  queue_search = "${replace(local.name, "-", " ")} events"

  # CloudWatch search expressions match every dimension set a metric has, so
  # a graph does not have to know them all.
  oryxa_search = "Namespace=\"Oryxa\""

  dashboard_widgets = [
    {
      type = "text", x = 0, y = 0, width = 24, height = 2
      properties = {
        markdown = join("\n", [
          "## ${local.name}",
          "app.${var.domain} and api.${var.domain} → Cloudflare Tunnel → oryxa on Fargate (${var.aws_region}) → PostgreSQL, EventBridge → SQS → consumers; Gemini and Memory Bank on Vertex AI (${var.gcp_project}); mail through SES (${var.ses_region}). Traces: X-Ray. Logs: `/${local.name}/app`.",
        ])
      }
    },

    # The edge: what Cloudflare hands the tasks.
    {
      type = "metric", x = 0, y = 2, width = 8, height = 6
      properties = {
        title  = "Tunnel requests and errors (per minute)"
        region = local.dashboard_region
        stat   = "Sum"
        period = 60
        metrics = [
          [{ expression = "SEARCH('${local.oryxa_search} MetricName=\"cloudflared_tunnel_total_requests\"', 'Sum', 60)", id = "requests", label = "requests" }],
          [{ expression = "SEARCH('${local.oryxa_search} MetricName=\"cloudflared_tunnel_request_errors\"', 'Sum', 60)", id = "errors", label = "errors" }],
        ]
      }
    },
    {
      type = "metric", x = 8, y = 2, width = 8, height = 6
      properties = {
        title  = "Tunnel responses by status"
        region = local.dashboard_region
        period = 60
        metrics = [
          [{ expression = "SEARCH('${local.oryxa_search} MetricName=\"cloudflared_tunnel_response_by_code\"', 'Sum', 60)", id = "codes" }],
        ]
      }
    },
    {
      type = "metric", x = 16, y = 2, width = 8, height = 6
      properties = {
        title  = "Tunnel connections and requests in flight"
        region = local.dashboard_region
        period = 60
        metrics = [
          [{ expression = "SEARCH('${local.oryxa_search} MetricName=\"cloudflared_tunnel_ha_connections\"', 'Minimum', 60)", id = "connections", label = "connections to Cloudflare" }],
          [{ expression = "SEARCH('${local.oryxa_search} MetricName=\"cloudflared_tunnel_concurrent_requests_per_tunnel\"', 'Maximum', 60)", id = "inflight", label = "in flight" }],
        ]
      }
    },

    # The API, as the process measures it.
    {
      type = "metric", x = 0, y = 8, width = 8, height = 6
      properties = {
        title  = "API latency (seconds)"
        region = local.dashboard_region
        period = 60
        metrics = [
          ["Oryxa", "http.server.request.duration", "service.name", "oryxa", { stat = "p50", label = "p50" }],
          ["...", { stat = "p90", label = "p90" }],
          ["...", { stat = "p99", label = "p99" }],
        ]
      }
    },
    {
      type = "metric", x = 8, y = 8, width = 8, height = 6
      properties = {
        title  = "API responses by status (per minute)"
        region = local.dashboard_region
        period = 60
        metrics = [
          [{ expression = "SEARCH('{Oryxa,service.name,http.response.status_code} MetricName=\"http.server.request.duration\"', 'SampleCount', 60)", id = "status" }],
        ]
      }
    },
    {
      type = "metric", x = 16, y = 8, width = 8, height = 6
      properties = {
        title  = "Slowest routes, p99 over 5 minutes (seconds)"
        region = local.dashboard_region
        period = 300
        metrics = [
          [{ expression = "SORT(SEARCH('{Oryxa,service.name,http.route,http.request.method} MetricName=\"http.server.request.duration\"', 'p99', 300), MAX, DESC, 10)", id = "routes" }],
        ]
      }
    },

    # The process and its task.
    {
      type = "metric", x = 0, y = 14, width = 8, height = 6
      properties = {
        title  = "Tasks running"
        region = local.dashboard_region
        period = 60
        metrics = [
          ["ECS/ContainerInsights", "RunningTaskCount", "ClusterName", aws_ecs_cluster.main.name, "ServiceName", aws_ecs_service.oryxa.name, { stat = "Minimum", label = "running" }],
          [".", "DesiredTaskCount", ".", ".", ".", ".", { stat = "Maximum", label = "desired" }],
        ]
      }
    },
    {
      type = "metric", x = 8, y = 14, width = 8, height = 6
      properties = {
        title  = "CPU and memory (% of the task)"
        region = local.dashboard_region
        period = 60
        yAxis  = { left = { min = 0, max = 100 } }
        metrics = [
          ["AWS/ECS", "CPUUtilization", "ClusterName", aws_ecs_cluster.main.name, "ServiceName", aws_ecs_service.oryxa.name, { stat = "Maximum", label = "CPU" }],
          [".", "MemoryUtilization", ".", ".", ".", ".", { stat = "Maximum", label = "memory" }],
        ]
      }
    },
    {
      type = "metric", x = 16, y = 14, width = 8, height = 6
      properties = {
        title  = "Go runtime"
        region = local.dashboard_region
        period = 60
        metrics = [
          [{ expression = "SEARCH('${local.oryxa_search} MetricName=\"go.goroutine.count\"', 'Maximum', 60)", id = "goroutines", label = "goroutines" }],
          [{ expression = "SEARCH('${local.oryxa_search} MetricName=\"go.memory.used\"', 'Maximum', 60)", id = "memory", label = "memory used", yAxis = "right" }],
        ]
      }
    },

    # The database.
    {
      type = "metric", x = 0, y = 20, width = 8, height = 6
      properties = {
        title  = "Database CPU (%) and connections"
        region = local.dashboard_region
        period = 60
        metrics = [
          ["AWS/RDS", "CPUUtilization", "DBInstanceIdentifier", aws_db_instance.main.identifier, { stat = "Maximum", label = "CPU" }],
          [".", "DatabaseConnections", ".", ".", { stat = "Maximum", label = "connections", yAxis = "right" }],
        ]
      }
    },
    {
      type = "metric", x = 8, y = 20, width = 8, height = 6
      properties = {
        title  = "Database I/O latency (seconds)"
        region = local.dashboard_region
        period = 60
        metrics = [
          ["AWS/RDS", "ReadLatency", "DBInstanceIdentifier", aws_db_instance.main.identifier, { stat = "Average", label = "read" }],
          [".", "WriteLatency", ".", ".", { stat = "Average", label = "write" }],
          [".", "DiskQueueDepth", ".", ".", { stat = "Maximum", label = "queue depth", yAxis = "right" }],
        ]
      }
    },
    {
      type = "metric", x = 16, y = 20, width = 8, height = 6
      properties = {
        title  = "Database memory and storage free (bytes)"
        region = local.dashboard_region
        period = 300
        metrics = [
          ["AWS/RDS", "FreeableMemory", "DBInstanceIdentifier", aws_db_instance.main.identifier, { stat = "Minimum", label = "memory" }],
          [".", "FreeStorageSpace", ".", ".", { stat = "Minimum", label = "storage", yAxis = "right" }],
        ]
      }
    },

    # The event pipeline: what waits, what fails, what is dead.
    {
      type = "metric", x = 0, y = 26, width = 8, height = 6
      properties = {
        title  = "Oldest unread event per queue (seconds)"
        region = local.dashboard_region
        period = 60
        metrics = [
          [{ expression = "SEARCH('{AWS/SQS,QueueName} ${local.queue_search} NOT dlq MetricName=\"ApproximateAgeOfOldestMessage\"', 'Maximum', 60)", id = "age" }],
        ]
      }
    },
    {
      type = "metric", x = 8, y = 26, width = 8, height = 6
      properties = {
        title  = "Events received per queue (per minute)"
        region = local.dashboard_region
        period = 60
        metrics = [
          [{ expression = "SEARCH('{AWS/SQS,QueueName} ${local.queue_search} NOT dlq MetricName=\"NumberOfMessagesReceived\"', 'Sum', 60)", id = "received" }],
        ]
      }
    },
    {
      type = "metric", x = 16, y = 26, width = 8, height = 6
      properties = {
        title  = "Dead letters, and deliveries EventBridge failed"
        region = local.dashboard_region
        period = 300
        metrics = [
          [{ expression = "SEARCH('{AWS/SQS,QueueName} ${local.queue_search} dlq MetricName=\"ApproximateNumberOfMessagesVisible\"', 'Maximum', 300)", id = "dead" }],
          [{ expression = "SEARCH('{AWS/Events,EventBusName,RuleName} MetricName=\"FailedInvocations\"', 'Sum', 300)", id = "failed" }],
        ]
      }
    },

    # Bedrock, which embeds and reranks for rag.
    {
      type = "metric", x = 0, y = 32, width = 12, height = 6
      properties = {
        title  = "Bedrock latency by model (milliseconds)"
        region = local.dashboard_region
        period = 300
        metrics = [
          [{ expression = "SEARCH('{AWS/Bedrock,ModelId} MetricName=\"InvocationLatency\"', 'p90', 300)", id = "latency" }],
        ]
      }
    },
    {
      type = "metric", x = 12, y = 32, width = 12, height = 6
      properties = {
        title  = "Bedrock calls, throttles and errors"
        region = local.dashboard_region
        period = 300
        metrics = [
          [{ expression = "SEARCH('{AWS/Bedrock,ModelId} MetricName=\"Invocations\"', 'Sum', 300)", id = "calls" }],
          [{ expression = "SEARCH('{AWS/Bedrock,ModelId} MetricName=\"InvocationThrottles\"', 'Sum', 300)", id = "throttles" }],
          [{ expression = "SEARCH('{AWS/Bedrock,ModelId} MetricName=\"InvocationServerErrors\"', 'Sum', 300)", id = "errors" }],
        ]
      }
    },

    # Mail, from the SES region.
    {
      type = "metric", x = 0, y = 38, width = 12, height = 6
      properties = {
        title  = "Mail (${var.ses_region})"
        region = var.ses_region
        stat   = "Sum"
        period = 300
        metrics = [
          ["AWS/SES", "Send", { label = "sent" }],
          [".", "Delivery", { label = "delivered" }],
          [".", "Bounce", { label = "bounced" }],
          [".", "Complaint", { label = "complaints" }],
        ]
      }
    },
    {
      type = "metric", x = 12, y = 38, width = 12, height = 6
      properties = {
        title  = "Hyperdrive connector"
        region = local.dashboard_region
        period = 300
        metrics = [
          ["AWS/EC2", "CPUUtilization", "InstanceId", aws_instance.edge.id, { stat = "Maximum", label = "CPU %" }],
          [".", "StatusCheckFailed", ".", ".", { stat = "Maximum", label = "status check failed", yAxis = "right" }],
        ]
      }
    },

    # What went wrong, newest first, with the trace to open in X-Ray.
    {
      type = "log", x = 0, y = 44, width = 24, height = 8
      properties = {
        title  = "Errors"
        region = local.dashboard_region
        view   = "table"
        query  = "SOURCE '/${local.name}/app' | fields @timestamp, component, msg, error, http.route, trace_id | filter level = \"ERROR\" | sort @timestamp desc | limit 100"
      }
    },
  ]
}

resource "aws_cloudwatch_dashboard" "main" {
  dashboard_name = local.name
  dashboard_body = jsonencode({ widgets = local.dashboard_widgets })
}
