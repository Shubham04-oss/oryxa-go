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
      COGNITO_CLI_CLIENT_ID      = var.cognito.cli_client_id
      COGNITO_CLI_CALLBACK_URL   = "http://127.0.0.1:8765/callback"
      COGNITO_CLI_CALLBACK_URLS  = "http://127.0.0.1:8765/callback,http://127.0.0.1:8766/callback,http://127.0.0.1:8767/callback,http://127.0.0.1:8768/callback"
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

      # Composio reaches apps at the API host. BILLING_URL is where Dodo's
      # checkout and portal, and billing's emails, send a member back: the
      # app's billing page, which both web-next and the old web/ app serve.
      APPS_PUBLIC_BASE_URL = local.api_url
      BILLING_URL          = "${local.app_url}/settings/billing"

      # Oryxa's own sign-in (docs/design/integrations-plan.md, phase 1):
      # apps' vault key, and the one OAuth callback registered with every
      # app. The callback has to be on the host a member's browser sends the
      # connect request to, because that is where the state cookie is set
      # (apps/README.md, "Oryxa's own sign-in") — the web app's own origin,
      # not the API host Composio's callback uses. APPS_ORYXA_TOOLKITS is
      # named explicitly, empty, rather than left out: ECS's own filter below
      # drops a blank value the same as leaving it unset, but switching a
      # toolkit on later (gate G4) is then a one-line change here.
      APPS_SIGNIN_KMS_KEY_ID   = aws_kms_alias.apps_signin.name
      APPS_SIGNIN_REDIRECT_URI = "${local.app_url}/v1/apps/oauth/callback"
      APPS_ORYXA_TOOLKITS      = ""

      # Pipedream Connect's environment. Production accounts live apart from
      # development's; apps reads it only once pipedream_ready puts its three
      # secrets (secrets.tf) on the task.
      PIPEDREAM_ENVIRONMENT = var.pipedream_environment

      # The Slack consent loop (docs/design/slack-implementation.md;
      # deploy/README.md, "The Slack bot"), all of it waiting for
      # slack_app_ready. Apps refuses to start with the surface on and no valid
      # APPS_SLACK_APP, and needs a caller allowed the purpose slack
      # (keys.tf), so the surface and the caller come with the secret and no
      # sooner. Agents reads its switch as its own: on, it asks an owner for
      # consent in Slack and needs AGENTS_PRIMARY_SLACK_TICKET_KEY, which
      # secrets.tf names to the task under the same gate. The surface and the
      # switch say off in words, so the task definition says which it is; the
      # callers are simply not set, since an empty value is dropped below.
      APPS_SLACK_SURFACE           = var.slack_app_ready ? "true" : "false"
      APPS_SERVICE_CALLERS         = var.slack_app_ready ? local.apps_service_callers : ""
      AGENTS_PRIMARY_SLACK_CONSENT = var.slack_app_ready ? "on" : "off"
      # The key one-time secrets are sealed under (keys.tf), whatever the
      # switch says: a secret is only ever shared for a request approved with
      # access, which nothing makes until consent is on.
      AGENTS_PRIMARY_SECRETS_KMS_KEY_ID = aws_kms_alias.agents_secrets.name

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

      AGENTS_PRIMARY_MODEL          = var.model
      AGENTS_PRIMARY_MODEL_LITE     = var.model
      AGENTS_PRIMARY_SERVICE_KEY_ID = aws_kms_alias.service["agents-primary"].name
      # Admits members to the edge's rooms (DMs, side conversations,
      # teamspaces); the edge trusts the key above.
      AGENTS_PRIMARY_EDGE_URL = "https://edge.oryxa.in"
      # These organizations' sandbox and browser run on the edge (Cloudflare
      # Sandbox and Browser Run); everyone else's stay on AgentCore. Once this
      # holds, AGENTS_PRIMARY_LIVE_BACKEND=cloudflare moves everyone.
      AGENTS_PRIMARY_LIVE_CLOUDFLARE_ORGS = "org__80adSFLC_LAUD90"
      # The voice agent in a call hands work to Krude (POST /v1/edge/dispatch),
      # signed with the edge's EDGE_SIGNING_KEY; this is its public half (base64
      # DER SPKI). List both keys while one rotates.
      AGENTS_PRIMARY_EDGE_TOKEN_PUBLIC_KEYS = jsonencode(["MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE3WEgTxZ3pvI62Dh1jEcG3Fhsy2P9lzOpvlTAqp0kJsFJ6pKmWFg+Zaq0C3mH1c2AUv1e/dqX4YCGHTo070w0vQ=="])
      AGENTS_PRIMARY_ARTIFACT_BUCKET        = local.artifacts_bucket
      AGENTS_PRIMARY_MEMORY_AGENT_ENGINE    = var.memory_agent_engine
      AGENTS_PRIMARY_MEMORY_LOCATION        = var.gcp_region
      # Every answer one member's agent gives another is read by this
      # guardrail before they read it (answer_guard.tf).
      AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID      = aws_bedrock_guardrail.agent_answers.guardrail_id
      AGENTS_PRIMARY_ANSWER_GUARDRAIL_VERSION = aws_bedrock_guardrail_version.agent_answers.version

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
    # Each publisher's event signing key (keys.tf), as internal/host reads it.
    { for service, key in local.event_signing_keys : "${upper(service)}_EVENT_SIGNING_KEY_ID" => key },
    # Where the image puts the integrations service's connectors (Dockerfile);
    # nothing reads it while INTEGRATIONS_ENABLED is off.
    { INTEGRATIONS_CONNECTORS_DIR = "/connectors" },
    # The guardrail that grounds every answer to a question asked in Slack
    # (answer_guard.tf). Both variables come together or the service refuses to
    # start, and this is the one place that names either. They are set whatever
    # the Slack switch says: nothing reads them while it is off.
    local.consent_guardrail_environment,
    # Realtime has the web app's Worker purge a member's direct messages and
    # side conversations, at the app's origin; with REALTIME_PURGE_SECRET
    # (secrets.tf), only once realtime_purge turns it on.
    var.realtime_purge ? { REALTIME_WORKER_URL = local.app_url } : {},
    # Where SpiceDB answers, once it runs (spicedb.tf); nothing otherwise.
    local.spicedb_environment,
  )

  app_secrets = merge(
    { DATABASE_URL = aws_secretsmanager_secret.database_url.arn },
    { for variable, _ in local.task_secrets : variable => aws_secretsmanager_secret.external[variable].arn },
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
          aws_secretsmanager_secret.spicedb_datastore[*].arn,
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
        # The answer guard reads answers with its own guardrail, and the consent
        # guard, which reads answers to questions asked in Slack, with its own:
        # each these two and no other. Without the second, every such answer is
        # held for its owner as unchecked.
        Sid      = "AnswerGuard"
        Effect   = "Allow"
        Action   = "bedrock:ApplyGuardrail"
        Resource = [aws_bedrock_guardrail.agent_answers.guardrail_arn, local.consent_guardrail_arn]
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
        Resource = concat([for key in aws_kms_key.service : key.arn], [for key in aws_kms_key.event : key.arn])
      },
      {
        Sid      = "SealGrants"
        Effect   = "Allow"
        Action   = ["kms:GenerateDataKey", "kms:Decrypt", "kms:DescribeKey"]
        Resource = aws_kms_key.auth.arn
      },
      {
        # Oryxa's own sign-in. One task role can use this key for every
        # service in the binary (deploy/README.md's "one task role" risk),
        # but the encryption context confines what it seals to apps' own
        # rows: a ciphertext bound to another service, resource type or id
        # fails to open even under this same grant (internal/secrets.Envelope,
        # apps/internal/signin.SealService).
        Sid      = "SealAppsSignin"
        Effect   = "Allow"
        Action   = ["kms:GenerateDataKey", "kms:Decrypt"]
        Resource = aws_kms_key.apps_signin.arn
        Condition = {
          StringEquals = { "kms:EncryptionContext:service" = "oryxa-apps" }
        }
      },
      {
        # One-time secrets shared between members. As for the key above, the
        # encryption context confines what this grant seals to agents' own
        # rows: a ciphertext bound to another service, resource type or
        # request fails to open even under this same grant
        # (agents/primary/internal/secretshares.SealService, keys.tf).
        Sid      = "SealSecretShares"
        Effect   = "Allow"
        Action   = ["kms:GenerateDataKey", "kms:Decrypt"]
        Resource = aws_kms_key.agents_secrets.arn
        Condition = {
          StringEquals = { "kms:EncryptionContext:service" = "oryxa-agents" }
        }
      },
      {
        # The eight admin calls that password sign-up, email verification and
        # password reset sign with the task role (auth/internal/passwordflow),
        # and nothing else auth asks Cognito. Sign-in (InitiateAuth) and
        # session refresh (GetTokensFromRefreshToken) are its other two calls;
        # Cognito authorizes those by app client and never reads IAM for them,
        # so they need no statement. DescribeUserPoolClient, which answers
        # with a client's secret, is left out on purpose: only
        # deploy/scripts/provision-app.sh reads it, as the operator.
        Sid    = "Cognito"
        Effect = "Allow"
        Action = [
          "cognito-idp:AdminCreateUser", "cognito-idp:AdminDeleteUser", "cognito-idp:AdminSetUserPassword",
          "cognito-idp:AdminDisableUser", "cognito-idp:AdminEnableUser", "cognito-idp:AdminGetUser",
          "cognito-idp:AdminUpdateUserAttributes", "cognito-idp:AdminUserGlobalSignOut",
        ]
        # The one pool auth's provisioner made, which app.auto.tfvars.json
        # names. Until the provisioner has run there is no pool to name, and
        # any pool matches.
        Resource = "arn:aws:cognito-idp:${var.aws_region}:${local.account_id}:userpool/${var.cognito.user_pool_id == "" ? "*" : var.cognito.user_pool_id}"
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
      dependsOn = [{ containerName = "otel", condition = "START" }]
      # The drain (ORYXA_DRAIN_TIMEOUT 20s), then requests and workers, can take
      # about 100s when something hangs; 120 is Fargate's most.
      stopTimeout      = 120
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
      # A release and its digest, not "latest", so that a task that starts
      # tomorrow runs what this one does. The digest is the multi-architecture
      # index, which has linux/arm64 for Fargate; `latest` was v0.50.0 on
      # 2026-09-29. To move to another release, read the digest of its tag:
      #   token=$(curl -s https://public.ecr.aws/token/ | jq -r .token)
      #   curl -sI -H "Authorization: Bearer $token" -H "Accept: application/vnd.oci.image.index.v1+json" \
      #     https://public.ecr.aws/v2/aws-observability/aws-otel-collector/manifests/<tag> | grep -i docker-content-digest
      name             = "otel"
      image            = "public.ecr.aws/aws-observability/aws-otel-collector:v0.50.0@sha256:7968fb60db6a2390a47ba6a2df029745638486e285c9b2487da1b722d0855a3e"
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
