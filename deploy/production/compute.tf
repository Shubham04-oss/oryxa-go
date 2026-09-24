# One ECS Fargate service runs the oryxa binary, every service in it, beside
# two sidecars: cloudflared, which is the only way in (api. and app./v1 through
# a Cloudflare Tunnel), and the AWS OpenTelemetry collector, which takes the
# process's traces and metrics to X-Ray and CloudWatch.

resource "aws_ecr_repository" "oryxa" {
  name                 = "oryxa"
  image_tag_mutability = "MUTABLE"
  force_delete         = false

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_lifecycle_policy" "oryxa" {
  repository = aws_ecr_repository.oryxa.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the last 30 images"
      selection    = { tagStatus = "any", countType = "imageCountMoreThan", countNumber = 30 }
      action       = { type = "expire" }
    }]
  })
}

resource "aws_ecs_cluster" "main" {
  name = local.name

  # Enhanced Container Insights: per-task and per-container CPU, memory,
  # network and restarts, which the dashboard and alarms read.
  setting {
    name  = "containerInsights"
    value = "enhanced"
  }
}

resource "aws_cloudwatch_log_group" "task" {
  for_each          = toset(["app", "cloudflared", "otel"])
  name              = "/${local.name}/${each.key}"
  retention_in_days = 30
}

# The collector's metrics arrive as CloudWatch embedded metric format logs.
resource "aws_cloudwatch_log_group" "metrics" {
  name              = "/${local.name}/metrics"
  retention_in_days = 14
}

locals {
  app_url = "https://${local.app_host}"
  api_url = "https://${local.api_host}"

  app_environment = merge(
    {
      DEPLOYMENT_ENVIRONMENT = var.environment
      LOG_LEVEL              = "info"
      AWS_REGION             = var.aws_region
      EVENT_BUS_NAME         = aws_cloudwatch_event_bus.main.name
      ORYXA_ADDRESS          = ":8080"
      ORYXA_AUTO_MIGRATE     = "true"

      OTEL_EXPORTER_OTLP_ENDPOINT = "http://localhost:4317"
      OTEL_EXPORTER_OTLP_INSECURE = "true"
      # The log group lets X-Ray show a trace's logs beside it.
      OTEL_RESOURCE_ATTRIBUTES = "deployment.environment=${var.environment},aws.log.group.names=${aws_cloudwatch_log_group.task["app"].name}"

      # Auth
      COGNITO_ISSUER             = var.cognito.user_pool_id == "" ? "" : "https://cognito-idp.${var.aws_region}.amazonaws.com/${var.cognito.user_pool_id}"
      COGNITO_USER_POOL_ID       = var.cognito.user_pool_id
      COGNITO_CLIENT_ID          = var.cognito.client_id
      COGNITO_DOMAIN             = var.cognito.domain
      COGNITO_CALLBACK_URL       = "${local.app_url}/v1/auth/callback"
      COGNITO_AGENT_CLIENT_ID    = var.cognito.agent_client_id
      COGNITO_AGENT_CALLBACK_URL = "${local.app_url}/v1/agent-grant/callback"
      AUTH_RETURN_URLS           = local.app_url
      INVITATION_ACCEPT_URL      = "${local.app_url}/invitations/accept"
      EMAIL_VERIFICATION_URL     = "${local.app_url}/auth/verify-email"
      PASSWORD_RESET_URL         = "${local.app_url}/auth/reset-password"
      AUTH_COOKIE_SECURE         = "true"
      AUTH_KMS_KEY_ID            = aws_kms_alias.auth.name
      AUTH_SERVICE_CALLERS       = local.auth_service_callers

      # Every browser-facing service answers the web app's origin.
      AGENTS_ALLOWED_ORIGINS    = local.app_url
      APPS_ALLOWED_ORIGINS      = local.app_url
      BILLING_ALLOWED_ORIGINS   = local.app_url
      FILES_ALLOWED_ORIGINS     = local.app_url
      KNOWLEDGE_ALLOWED_ORIGINS = local.app_url
      RAG_ALLOWED_ORIGINS       = local.app_url
      TEAMS_ALLOWED_ORIGINS     = local.app_url

      # Apps and billing are reached by third parties at the API host.
      APPS_PUBLIC_BASE_URL = local.api_url
      BILLING_URL          = local.api_url

      FILES_BUCKET_NAME = local.files_bucket

      KNOWLEDGE_SERVICE_KEY_ID  = aws_kms_alias.service["knowledge"].name
      KNOWLEDGE_SERVICE_CALLERS = local.knowledge_service_callers

      RAG_VECTOR_BUCKET_NAME = local.vectors_bucket
      RAG_SERVICE_KEY_ID     = aws_kms_alias.service["rag"].name
      RAG_ANSWER_PROVIDER    = "vertex"
      RAG_ANSWER_MODEL       = var.model
      RAG_RERANK_MODEL_ARN   = "arn:aws:bedrock:${var.aws_region}::foundation-model/${var.rerank_model}"
      # The Bedrock knowledge base rag indexes into and searches
      # (rag_knowledge_base), where new corpora go (rag_default_backend).
      RAG_KNOWLEDGE_BASE_ID             = local.rag_knowledge_base.id
      RAG_KNOWLEDGE_BASE_DATA_SOURCE_ID = local.rag_knowledge_base.data_source_id
      RAG_KNOWLEDGE_BASE_TYPE           = local.rag_knowledge_base.type
      RAG_KNOWLEDGE_BASE_BUCKET         = aws_s3_bucket.knowledge_base_documents.bucket
      RAG_DEFAULT_BACKEND               = var.rag_default_backend
      KNOWLEDGE_ORIGINALS_BUCKET        = aws_s3_bucket.knowledge_base_documents.bucket
      # What connected apps yield is judged worth indexing, or not, before it
      # is published: by Jev, or by the same model on the same Vertex AI
      # project as Krude, which also decides what Jev is unsure of.
      KNOWLEDGE_JUDGE       = var.knowledge_judge
      KNOWLEDGE_JUDGE_MODEL = var.model

      GOOGLE_CLOUD_PROJECT              = var.gcp_project
      GOOGLE_CLOUD_LOCATION             = "global"
      GOOGLE_WORKLOAD_IDENTITY_PROVIDER = local.gcp_workload_identity_audience
      GOOGLE_SERVICE_ACCOUNT            = google_service_account.runtime.email

      AGENTS_PRIMARY_MODEL               = var.model
      AGENTS_PRIMARY_MODEL_LITE          = var.model
      AGENTS_PRIMARY_SERVICE_KEY_ID      = aws_kms_alias.service["agents-primary"].name
      AGENTS_PRIMARY_ARTIFACT_BUCKET     = local.artifacts_bucket
      AGENTS_PRIMARY_MEMORY_AGENT_ENGINE = var.memory_agent_engine
      AGENTS_PRIMARY_MEMORY_LOCATION     = var.gcp_region

      AGENTS_WORKFLOW_QUEUE_URL          = aws_sqs_queue.agent_workflows.url
      AGENTS_WORKFLOW_STATE_MACHINE_ARN  = aws_sfn_state_machine.agent_workflows.arn
      AGENTS_WORKFLOW_SCHEDULER_ROLE_ARN = aws_iam_role.agent_workflow_scheduler.arn
      AGENTS_WORKFLOW_SCHEDULE_DLQ_ARN   = aws_sqs_queue.agent_workflow_schedule_dlq.arn
      AGENTS_WORKFLOW_SCHEDULER_GROUP    = aws_scheduler_schedule_group.agent_workflows.name

      EMAIL_FROM_ADDRESS       = "no-reply@${var.domain}"
      EMAIL_FROM_NAME          = "Oryxa"
      SES_REGION               = var.ses_region
      SES_CONFIGURATION_SET    = local.ses_configuration_set
      EMAIL_DELIVERIES_TABLE   = local.email_deliveries_table
      EMAIL_SUPPRESSIONS_TABLE = local.email_suppressions_table
    },
    { for service, url in local.queue_urls : (service == "email" ? "EMAIL_QUEUE_URL" : "${upper(service)}_EVENTS_QUEUE_URL") => url },
    # Realtime has the web app's Worker purge a member's direct messages and
    # side conversations, at the app's origin; with REALTIME_PURGE_SECRET
    # (secrets.tf), only once realtime_purge turns it on.
    var.realtime_purge ? { REALTIME_WORKER_URL = local.app_url } : {},
  )

  app_secrets = merge(
    { DATABASE_URL = aws_secretsmanager_secret.database_url.arn },
    { for variable, _ in local.external_secrets : variable => aws_secretsmanager_secret.external[variable].arn },
  )

  log_options = { for name, group in aws_cloudwatch_log_group.task : name => {
    "awslogs-group"         = group.name
    "awslogs-region"        = var.aws_region
    "awslogs-stream-prefix" = name
  } }
}

resource "aws_iam_role" "execution" {
  name = "${local.name}-execution"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role_policy" "execution_secrets" {
  role = aws_iam_role.execution.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = "secretsmanager:GetSecretValue"
        Resource = concat(
          [aws_secretsmanager_secret.database_url.arn, aws_secretsmanager_secret.api_tunnel_token.arn],
          [for secret in aws_secretsmanager_secret.external : secret.arn],
        )
      },
      {
        Effect   = "Allow"
        Action   = "ssm:GetParameters"
        Resource = aws_ssm_parameter.otel_config.arn
      },
    ]
  })
}

# What the oryxa process itself may do. Resource names are the ones the
# service provisioners create, fixed in main.tf.
resource "aws_iam_role" "task" {
  name = "${local.name}-task"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = { ArnLike = { "aws:SourceArn" = "arn:aws:ecs:${var.aws_region}:${local.account_id}:*" } }
    }]
  })
}

resource "aws_iam_role_policy" "task" {
  role = aws_iam_role.task.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "PublishEvents"
        Effect   = "Allow"
        Action   = "events:PutEvents"
        Resource = aws_cloudwatch_event_bus.main.arn
      },
      {
        Sid    = "ConsumeQueues"
        Effect = "Allow"
        Action = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:DeleteMessageBatch", "sqs:ChangeMessageVisibility", "sqs:GetQueueAttributes"]
        Resource = [for queue in local.queue_names :
        "arn:aws:sqs:${var.aws_region}:${local.account_id}:${queue}"]
      },
      {
        Sid    = "Buckets"
        Effect = "Allow"
        Action = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket", "s3:GetBucketLocation"]
        Resource = flatten([for bucket in [local.files_bucket, local.artifacts_bucket] :
        ["arn:aws:s3:::${bucket}", "arn:aws:s3:::${bucket}/*"]])
      },
      {
        Sid      = "Vectors"
        Effect   = "Allow"
        Action   = "s3vectors:*"
        Resource = ["arn:aws:s3vectors:${var.aws_region}:${local.account_id}:bucket/${local.vectors_bucket}", "arn:aws:s3vectors:${var.aws_region}:${local.account_id}:bucket/${local.vectors_bucket}/*"]
      },
      {
        # Ingesting or deleting a document is also authorized as
        # StartIngestionJob on the knowledge base (AWS's service authorization
        # reference lists what each operation needs).
        Sid    = "KnowledgeBase"
        Effect = "Allow"
        Action = [
          "bedrock:Retrieve", "bedrock:IngestKnowledgeBaseDocuments", "bedrock:GetKnowledgeBaseDocuments",
          "bedrock:DeleteKnowledgeBaseDocuments", "bedrock:ListKnowledgeBaseDocuments", "bedrock:StartIngestionJob",
        ]
        # Both knowledge bases, so rag_knowledge_base can move between them
        # without an IAM change racing the task that needs it.
        Resource = [aws_bedrockagent_knowledge_base.main.arn, aws_bedrockagent_knowledge_base.managed.arn]
      },
      {
        # Actions with no resource type, which a statement matches only with
        # "*": reranking, and the association check ingesting and deleting a
        # knowledge base's documents also make.
        Sid      = "BedrockUnscoped"
        Effect   = "Allow"
        Action   = ["bedrock:Rerank", "bedrock:AssociateThirdPartyKnowledgeBase"]
        Resource = "*"
      },
      {
        # Originals knowledge stores and text rag stages, for the knowledge
        # base to read.
        Sid      = "KnowledgeBaseDocuments"
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket"]
        Resource = [aws_s3_bucket.knowledge_base_documents.arn, "${aws_s3_bucket.knowledge_base_documents.arn}/*"]
      },
      {
        Sid      = "Bedrock"
        Effect   = "Allow"
        Action   = ["bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream"]
        Resource = ["arn:aws:bedrock:${var.aws_region}::foundation-model/*"]
      },
      {
        # Krude's sandbox and browser are AWS's built-in AgentCore tools.
        Sid    = "AgentCoreTools"
        Effect = "Allow"
        Action = [
          "bedrock-agentcore:StartCodeInterpreterSession", "bedrock-agentcore:InvokeCodeInterpreter",
          "bedrock-agentcore:StopCodeInterpreterSession", "bedrock-agentcore:GetCodeInterpreterSession",
          "bedrock-agentcore:StartBrowserSession", "bedrock-agentcore:StopBrowserSession",
          "bedrock-agentcore:GetBrowserSession", "bedrock-agentcore:UpdateBrowserStream",
          "bedrock-agentcore:ConnectBrowserAutomationStream",
        ]
        Resource = [
          "arn:aws:bedrock-agentcore:${var.aws_region}:aws:code-interpreter/*",
          "arn:aws:bedrock-agentcore:${var.aws_region}:aws:browser/*",
          "arn:aws:bedrock-agentcore:${var.aws_region}:${local.account_id}:code-interpreter/*",
          "arn:aws:bedrock-agentcore:${var.aws_region}:${local.account_id}:browser/*",
        ]
      },
      {
        Sid      = "SignAsService"
        Effect   = "Allow"
        Action   = ["kms:Sign", "kms:GetPublicKey", "kms:DescribeKey"]
        Resource = [for key in aws_kms_key.service : key.arn]
      },
      {
        Sid      = "SealGrants"
        Effect   = "Allow"
        Action   = ["kms:GenerateDataKey", "kms:Decrypt", "kms:DescribeKey"]
        Resource = aws_kms_key.auth.arn
      },
      {
        Sid      = "Cognito"
        Effect   = "Allow"
        Action   = "cognito-idp:*"
        Resource = "arn:aws:cognito-idp:${var.aws_region}:${local.account_id}:userpool/*"
      },
      {
        Sid      = "SendMail"
        Effect   = "Allow"
        Action   = ["ses:SendEmail", "ses:SendRawEmail"]
        Resource = ["arn:aws:ses:${var.ses_region}:${local.account_id}:identity/${var.domain}", "arn:aws:ses:${var.ses_region}:${local.account_id}:configuration-set/${local.ses_configuration_set}"]
      },
      {
        Sid    = "EmailTables"
        Effect = "Allow"
        Action = ["dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:Query"]
        Resource = [for table in [local.email_deliveries_table, local.email_suppressions_table] :
        "arn:aws:dynamodb:${var.aws_region}:${local.account_id}:table/${table}"]
      },
      {
        Sid      = "Telemetry"
        Effect   = "Allow"
        Action   = ["xray:PutTraceSegments", "xray:PutTelemetryRecords", "xray:GetSamplingRules", "xray:GetSamplingTargets", "xray:GetSamplingStatisticSummaries", "cloudwatch:PutMetricData", "logs:CreateLogStream", "logs:PutLogEvents", "logs:DescribeLogStreams", "logs:DescribeLogGroups"]
        Resource = "*"
      },
      {
        Sid      = "EcsExec"
        Effect   = "Allow"
        Action   = ["ssmmessages:CreateControlChannel", "ssmmessages:CreateDataChannel", "ssmmessages:OpenControlChannel", "ssmmessages:OpenDataChannel"]
        Resource = "*"
      },
    ]
  })
}

resource "aws_ecs_task_definition" "oryxa" {
  family                   = local.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.task_cpu
  memory                   = var.task_memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64"
  }

  container_definitions = jsonencode([
    {
      name         = "oryxa"
      image        = "${aws_ecr_repository.oryxa.repository_url}:${var.image_tag}"
      essential    = true
      portMappings = [{ containerPort = 8080, protocol = "tcp" }]
      environment  = [for name, value in local.app_environment : { name = name, value = value } if value != ""]
      secrets      = [for name, arn in local.app_secrets : { name = name, valueFrom = arn }]
      healthCheck = {
        command     = ["CMD", "/oryxa", "healthcheck"]
        interval    = 15
        timeout     = 5
        retries     = 3
        startPeriod = 90
      }
      dependsOn        = [{ containerName = "otel", condition = "START" }]
      stopTimeout      = 45
      logConfiguration = { logDriver = "awslogs", options = local.log_options["app"] }
    },
    {
      # Connects only once oryxa answers its health check, so Cloudflare never
      # routes to a process still starting.
      name             = "cloudflared"
      image            = "cloudflare/cloudflared:latest"
      essential        = true
      command          = ["tunnel", "--no-autoupdate", "--metrics", "127.0.0.1:2000", "run"]
      secrets          = [{ name = "TUNNEL_TOKEN", valueFrom = aws_secretsmanager_secret.api_tunnel_token.arn }]
      dependsOn        = [{ containerName = "oryxa", condition = "HEALTHY" }]
      logConfiguration = { logDriver = "awslogs", options = local.log_options["cloudflared"] }
    },
    {
      name             = "otel"
      image            = "public.ecr.aws/aws-observability/aws-otel-collector:latest"
      essential        = false
      secrets          = [{ name = "AOT_CONFIG_CONTENT", valueFrom = aws_ssm_parameter.otel_config.arn }]
      logConfiguration = { logDriver = "awslogs", options = local.log_options["otel"] }
    },
  ])
}

resource "aws_ecs_service" "oryxa" {
  name                   = "oryxa"
  cluster                = aws_ecs_cluster.main.id
  task_definition        = aws_ecs_task_definition.oryxa.arn
  desired_count          = var.desired_count
  launch_type            = "FARGATE"
  enable_execute_command = true
  propagate_tags         = "SERVICE"
  # An apply that changes the release returns once the new task serves.
  wait_for_steady_state = true

  # A new task starts, and turns healthy, before the old one stops.
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.app.id]
    assign_public_ip = true
  }
}
