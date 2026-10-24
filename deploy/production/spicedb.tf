# SpiceDB, the permissions engine, in shadow (docs/design/permissions, §6 step
# 4): it answers beside internal/access, which still decides. Nothing here
# exists until spicedb_enabled is true, and no task runs until
# spicedb_desired_count is above zero too, which is also when the oryxa task is
# told where SpiceDB is. deploy/README.md, "SpiceDB", gives the order.
#
# Two tasks on the oryxa cluster, in its subnets, found by a private DNS name
# (Cloud Map) rather than a load balancer: nothing in this configuration has a
# load balancer, an internal NLB would cost more than both tasks, and Service
# Connect would put a proxy into the oryxa task. gRPC is plain inside the VPC,
# open only to the oryxa tasks' security group, and every call carries the
# preshared key.

variable "spicedb_enabled" {
  description = "Whether SpiceDB's resources exist: its database login, secrets, task definitions, DNS name and service (at spicedb_desired_count tasks), and track_commit_timestamp on the database. False is no change at all."
  type        = bool
  default     = false
}

variable "spicedb_desired_count" {
  description = "How many SpiceDB tasks run once spicedb_enabled is on. Zero until its database is migrated; above zero, the oryxa task is also given SPICEDB_ENDPOINT and SPICEDB_PRESHARED_KEY."
  type        = number
  default     = 0
}

locals {
  spicedb       = var.spicedb_enabled ? 1 : 0
  spicedb_on    = var.spicedb_enabled && var.spicedb_desired_count > 0
  spicedb_image = "authzed/spicedb:v1.56.2"
  # The name the tasks register under, in the namespace below.
  spicedb_host = "spicedb.${local.name}.internal"

  # What the oryxa task reads (internal/access/spicedb.FromEnv). Insecure is
  # plain gRPC, which only the VPC and the security group below ever carry.
  spicedb_environment = local.spicedb_on ? {
    SPICEDB_ENDPOINT = "${local.spicedb_host}:50051"
    SPICEDB_INSECURE = "true"
  } : {}

  spicedb_datastore = [
    { name = "SPICEDB_DATASTORE_ENGINE", value = "postgres" },
  ]
  spicedb_datastore_secrets = var.spicedb_enabled ? [
    { name = "SPICEDB_DATASTORE_CONN_URI", valueFrom = aws_secretsmanager_secret.spicedb_datastore[0].arn },
  ] : []

  spicedb_logs = var.spicedb_enabled ? { for prefix in ["spicedb", "migrate", "schema"] : prefix => {
    "awslogs-group"         = aws_cloudwatch_log_group.spicedb[0].name
    "awslogs-region"        = var.aws_region
    "awslogs-stream-prefix" = prefix
  } } : {}
}

# Its own login and database on the RDS instance. The password is in the state,
# as the oryxa login's is (database.tf); the one-off task below creates both.
resource "random_password" "spicedb" {
  count   = local.spicedb
  length  = 40
  special = false # it travels inside a connection URL
}

resource "aws_secretsmanager_secret" "spicedb_datastore" {
  count                   = local.spicedb
  name                    = "${local.name}/spicedb-datastore-uri"
  description             = "SpiceDB's datastore connection URI"
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "spicedb_datastore" {
  count         = local.spicedb
  secret_id     = aws_secretsmanager_secret.spicedb_datastore[0].id
  secret_string = "postgres://spicedb:${random_password.spicedb[0].result}@${aws_db_instance.main.address}:5432/spicedb?sslmode=require"
}

resource "aws_cloudwatch_log_group" "spicedb" {
  count             = local.spicedb
  name              = "/${local.name}/spicedb"
  retention_in_days = 30
}

resource "aws_security_group" "spicedb" {
  count       = local.spicedb
  name        = "${local.name}-spicedb"
  description = "SpiceDB: gRPC from the oryxa tasks only"
  vpc_id      = aws_vpc.main.id

  # The database, and Docker Hub for the image (a public address, no NAT).
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_vpc_security_group_ingress_rule" "spicedb" {
  count                        = local.spicedb
  security_group_id            = aws_security_group.spicedb[0].id
  referenced_security_group_id = aws_security_group.app.id
  ip_protocol                  = "tcp"
  from_port                    = 50051
  to_port                      = 50051
  description                  = "SpiceDB gRPC from app"
}

resource "aws_vpc_security_group_ingress_rule" "database_spicedb" {
  count                        = local.spicedb
  security_group_id            = aws_security_group.database.id
  referenced_security_group_id = aws_security_group.spicedb[0].id
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
  description                  = "PostgreSQL from spicedb"
}

resource "aws_service_discovery_private_dns_namespace" "main" {
  count = local.spicedb
  name  = "${local.name}.internal"
  vpc   = aws_vpc.main.id
}

# One A record per healthy task; ECS removes a task's record when its health
# check fails or it stops.
resource "aws_service_discovery_service" "spicedb" {
  count = local.spicedb
  name  = "spicedb"

  dns_config {
    namespace_id   = aws_service_discovery_private_dns_namespace.main[0].id
    routing_policy = "MULTIVALUE"
    dns_records {
      type = "A"
      ttl  = 10
    }
  }

  # Empty: ECS reports each task's health to Cloud Map.
  health_check_custom_config {}
}

# The smallest Fargate size: in shadow it answers a check per guarded request,
# from caches sized to the task's memory. The pools are kept small, since the
# database is the oryxa tasks' too.
resource "aws_ecs_task_definition" "spicedb" {
  count                    = local.spicedb
  family                   = "${local.name}-spicedb"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.execution.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64"
  }

  container_definitions = jsonencode([{
    name         = "spicedb"
    image        = local.spicedb_image
    essential    = true
    command      = ["serve"]
    portMappings = [{ containerPort = 50051, protocol = "tcp" }]
    environment = concat(local.spicedb_datastore, [
      { name = "SPICEDB_DATASTORE_CONN_POOL_READ_MIN_OPEN", value = "2" },
      { name = "SPICEDB_DATASTORE_CONN_POOL_READ_MAX_OPEN", value = "8" },
      { name = "SPICEDB_DATASTORE_CONN_POOL_WRITE_MIN_OPEN", value = "1" },
      { name = "SPICEDB_DATASTORE_CONN_POOL_WRITE_MAX_OPEN", value = "4" },
      { name = "SPICEDB_LOG_FORMAT", value = "json" },
    ])
    secrets = concat(local.spicedb_datastore_secrets, [
      { name = "SPICEDB_GRPC_PRESHARED_KEY", valueFrom = aws_secretsmanager_secret.external["SPICEDB_PRESHARED_KEY"].arn },
    ])
    # The gRPC health service, which serve answers without the key; the
    # release image carries the probe.
    healthCheck = {
      command     = ["CMD", "grpc_health_probe", "-addr=localhost:50051"]
      interval    = 15
      timeout     = 5
      retries     = 3
      startPeriod = 30
    }
    logConfiguration = { logDriver = "awslogs", options = local.spicedb_logs["spicedb"] }
  }])
}

resource "aws_ecs_service" "spicedb" {
  count           = local.spicedb
  name            = "spicedb"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.spicedb[0].arn
  desired_count   = var.spicedb_desired_count
  launch_type     = "FARGATE"
  propagate_tags  = "SERVICE"

  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.spicedb[0].id]
    assign_public_ip = true
  }

  service_registries {
    registry_arn = aws_service_discovery_service.spicedb[0].arn
  }
}

# One-off, run by hand with the oryxa service's network (deploy/README.md,
# "SpiceDB"): creates the spicedb login and database if they are missing, as
# the oryxa login, then migrates SpiceDB's schema to head. Safe to run again.
resource "aws_ecs_task_definition" "spicedb_migrate" {
  count                    = local.spicedb
  family                   = "${local.name}-spicedb-migrate"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.execution.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64"
  }

  container_definitions = jsonencode([
    {
      name       = "database"
      image      = "public.ecr.aws/docker/library/postgres:17-alpine"
      essential  = false
      entryPoint = ["sh", "-c"]
      # The password comes out of the datastore URI, so it is never in the task
      # definition. CREATE DATABASE cannot run in a transaction, so each
      # statement commits on its own.
      command = [<<-EOT
        set -eu
        pw=$(printf '%s' "$SPICEDB_DATASTORE_CONN_URI" | sed -E 's#^postgres://spicedb:([^@]+)@.*#\1#')
        psql "$DATABASE_URL" --no-psqlrc -v ON_ERROR_STOP=1 -v pw="$pw" <<'SQL'
        SELECT format('CREATE ROLE spicedb LOGIN PASSWORD %L', :'pw') WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'spicedb') \gexec
        ALTER ROLE spicedb PASSWORD :'pw';
        GRANT spicedb TO CURRENT_USER;
        SELECT 'CREATE DATABASE spicedb OWNER spicedb' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'spicedb') \gexec
        SQL
        EOT
      ]
      secrets = concat(local.spicedb_datastore_secrets, [
        { name = "DATABASE_URL", valueFrom = aws_secretsmanager_secret.database_url.arn },
      ])
      logConfiguration = { logDriver = "awslogs", options = local.spicedb_logs["migrate"] }
    },
    {
      name             = "migrate"
      image            = local.spicedb_image
      essential        = true
      command          = ["datastore", "migrate", "head"]
      environment      = local.spicedb_datastore
      secrets          = local.spicedb_datastore_secrets
      dependsOn        = [{ containerName = "database", condition = "SUCCESS" }]
      logConfiguration = { logDriver = "awslogs", options = local.spicedb_logs["migrate"] }
    },
  ])
}

# One-off too, once the service serves and whenever schema.zed changes: writes
# internal/access/spicedb/schema.zed, as the compose profile's spicedb-schema
# does. zed's image has no shell, so a first container puts the file on a
# volume they share.
resource "aws_ecs_task_definition" "spicedb_schema" {
  count                    = local.spicedb
  family                   = "${local.name}-spicedb-schema"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.execution.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64"
  }

  volume {
    name = "schema"
  }

  container_definitions = jsonencode([
    {
      name        = "file"
      image       = "public.ecr.aws/docker/library/postgres:17-alpine"
      essential   = false
      entryPoint  = ["sh", "-c"]
      command     = ["printf '%s' \"$SCHEMA\" > /schema/schema.zed"]
      environment = [{ name = "SCHEMA", value = file("${path.module}/../../internal/access/spicedb/schema.zed") }]
      mountPoints = [{ sourceVolume = "schema", containerPath = "/schema" }]
    },
    {
      name             = "zed"
      image            = "authzed/zed:v1.2.1"
      essential        = true
      command          = ["schema", "write", "/schema/schema.zed", "--endpoint=${local.spicedb_host}:50051", "--insecure"]
      secrets          = [{ name = "ZED_TOKEN", valueFrom = aws_secretsmanager_secret.external["SPICEDB_PRESHARED_KEY"].arn }]
      mountPoints      = [{ sourceVolume = "schema", containerPath = "/schema", readOnly = true }]
      dependsOn        = [{ containerName = "file", condition = "SUCCESS" }]
      logConfiguration = { logDriver = "awslogs", options = local.spicedb_logs["schema"] }
    },
  ])
}

# Fewer tasks running than asked for, for three minutes: ECS replaces a task
# whose health check fails, so an unhealthy one shows here too.
resource "aws_cloudwatch_metric_alarm" "spicedb_running" {
  count               = local.spicedb_on ? 1 : 0
  alarm_name          = "${local.name}-spicedb-running"
  alarm_description   = "Fewer SpiceDB tasks are running than asked for. In shadow nothing is denied for it, but its disagreements stop being logged."
  namespace           = "ECS/ContainerInsights"
  metric_name         = "RunningTaskCount"
  dimensions          = { ClusterName = aws_ecs_cluster.main.name, ServiceName = aws_ecs_service.spicedb[0].name }
  statistic           = "Minimum"
  period              = 60
  evaluation_periods  = 3
  comparison_operator = "LessThanThreshold"
  threshold           = var.spicedb_desired_count
  treat_missing_data  = "breaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}

# The projector's events (internal/access/spicedb/projector, the oryxa task's
# "spicedb" service): a queue, its dead-letter queue and the rule feeding it,
# as each service's provisioner makes its own (deploy/scripts/provision-app.sh),
# with the same settings. They exist with spicedb_enabled, so events wait for
# the projector; local.queue_names (main.tf) names the queue, which gives the
# oryxa task SPICEDB_EVENTS_QUEUE_URL, the right to consume it and its backlog
# and dead-letter alarms, only once SpiceDB runs. Production refuses to start
# with SPICEDB_ENDPOINT set and no queue (ORYXA_REQUIRE_EVENT_QUEUES), so both
# come together.
locals {
  spicedb_queue = "${local.name}-spicedb-events"

  # projector.Subscriptions(), by source; TestRuleMatchesSubscriptions fails
  # when they drift. An event it maps but this leaves out is never projected,
  # only reconciled at night.
  spicedb_subscriptions = {
    "oryxa.agents" = ["agents.agent.created.v1", "agents.agent.deleted.v1", "agents.agent.updated.v1", "agents.interaction.requested.v1"]
    "oryxa.apps"   = ["apps.connection.connected.v1", "apps.connection.initiated.v1", "apps.connection.removed.v1"]
    "oryxa.auth"   = ["auth.member.joined.v1", "auth.member.removed.v1", "auth.member.role-changed.v1", "auth.organization.deleted.v1"]
    "oryxa.files"  = ["files.object.created.v1", "files.object.deleted.v1"]
    "oryxa.rag"    = ["rag.corpus.created.v1", "rag.corpus.deleted.v1"]
    "oryxa.teams"  = ["teams.member.added.v1", "teams.member.removed.v1", "teams.member.role-changed.v1", "teams.team.created.v1", "teams.team.deleted.v1"]
  }
}

resource "aws_sqs_queue" "spicedb_dlq" {
  count                     = local.spicedb
  name                      = "${local.spicedb_queue}-dlq"
  message_retention_seconds = 1209600
  sqs_managed_sse_enabled   = true
}

resource "aws_sqs_queue" "spicedb" {
  count                      = local.spicedb
  name                       = local.spicedb_queue
  visibility_timeout_seconds = 60
  sqs_managed_sse_enabled    = true
  redrive_policy             = jsonencode({ deadLetterTargetArn = aws_sqs_queue.spicedb_dlq[0].arn, maxReceiveCount = 5 })
}

resource "aws_cloudwatch_event_rule" "spicedb" {
  count          = local.spicedb
  name           = "spicedb-events"
  description    = "Events that move a SpiceDB relationship, for the projector"
  event_bus_name = aws_cloudwatch_event_bus.main.name
  event_pattern = jsonencode({ "$or" = [for source in sort(keys(local.spicedb_subscriptions)) : {
    source        = [source]
    "detail-type" = sort(local.spicedb_subscriptions[source])
  }] })
}

resource "aws_cloudwatch_event_target" "spicedb" {
  count          = local.spicedb
  rule           = aws_cloudwatch_event_rule.spicedb[0].name
  event_bus_name = aws_cloudwatch_event_bus.main.name
  target_id      = "spicedb-queue"
  arn            = aws_sqs_queue.spicedb[0].arn
}

# The rule, and nothing else, may send to the queue.
resource "aws_sqs_queue_policy" "spicedb" {
  count     = local.spicedb
  queue_url = aws_sqs_queue.spicedb[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "AllowSpiceDBRule"
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.spicedb[0].arn
      Condition = {
        ArnEquals    = { "aws:SourceArn" = aws_cloudwatch_event_rule.spicedb[0].arn }
        StringEquals = { "aws:SourceAccount" = local.account_id }
      }
    }]
  })
}

# Every relationship the nightly reconcile has to fix is one the projector
# missed: it logs each at WARN, and this counts them, as app_errors
# (hardening.tf) counts errors. A failed reconcile logs at ERROR, which
# app_errors already alarms on.
resource "aws_cloudwatch_log_metric_filter" "spicedb_reconcile_fixes" {
  count          = local.spicedb_on ? 1 : 0
  name           = "${local.name}-spicedb-reconcile-fixes"
  log_group_name = aws_cloudwatch_log_group.task["app"].name
  pattern        = "{ $.level = \"WARN\" && $.msg = \"spicedb reconcile: *\" }"

  metric_transformation {
    name      = "SpiceDBReconcileFixes"
    namespace = "Oryxa/Logs"
    value     = "1"
  }
}

resource "aws_cloudwatch_metric_alarm" "spicedb_reconcile_fixes" {
  count               = local.spicedb_on ? 1 : 0
  alarm_name          = "${local.name}-spicedb-reconcile-fixes"
  alarm_description   = "The nightly SpiceDB reconcile fixed relationships the projector missed. Its WARN lines in the app log group name each one."
  namespace           = aws_cloudwatch_log_metric_filter.spicedb_reconcile_fixes[0].metric_transformation[0].namespace
  metric_name         = aws_cloudwatch_log_metric_filter.spicedb_reconcile_fixes[0].metric_transformation[0].name
  statistic           = "Sum"
  period              = 3600
  evaluation_periods  = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = 1
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}
