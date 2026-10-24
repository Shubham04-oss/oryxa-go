# Bedrock AgentCore Memory, to keep what Krude remembers in place of Vertex AI
# Memory Bank (docs/design/agent-runtime.md, section 7; agents/primary
# README, "AgentCore Memory"). Nothing here exists until
# agentcore_memory_enabled is true, and only then is the oryxa task told to use
# it, as agentcore_memory_backend says: by default as a shadow that Memory Bank
# still answers beside. False is no change at all.
#
# Each member's memory is one actor, <organization>/<member>/<agent|primary>,
# and the semantic strategy extracts its long-term records into /m/<actor>/,
# which is what a search reads and what forgetting a member or an organization
# lists by prefix (adkmemory.agentCoreActor). Raw events, which hold the
# conversation itself, expire after the shortest time AgentCore allows.
variable "agentcore_memory_enabled" {
  description = "Whether the AgentCore memory, its strategy, its KMS key and the task's grant on them exist, and the oryxa task is given AGENTS_PRIMARY_MEMORY_BACKEND and AGENTS_PRIMARY_MEMORY_ID. False, the default, is no change at all."
  type        = bool
  default     = false
}

variable "agentcore_memory_backend" {
  description = "AGENTS_PRIMARY_MEMORY_BACKEND once agentcore_memory_enabled is on: vertex+agentcore (Memory Bank answers, AgentCore is written beside it), agentcore+vertex (the reverse), or agentcore (AgentCore alone). Both pairs need memory_agent_engine."
  type        = string
  default     = "vertex+agentcore"
  validation {
    condition     = contains(["vertex+agentcore", "agentcore+vertex", "agentcore"], var.agentcore_memory_backend)
    error_message = "agentcore_memory_backend is vertex+agentcore, agentcore+vertex or agentcore."
  }
}

locals {
  agentcore_memory = var.agentcore_memory_enabled ? 1 : 0
  # What the oryxa task reads (agents/primary/internal/serviceconfig).
  agentcore_memory_environment = var.agentcore_memory_enabled ? {
    AGENTS_PRIMARY_MEMORY_BACKEND = var.agentcore_memory_backend
    AGENTS_PRIMARY_MEMORY_ID      = aws_bedrockagentcore_memory.krude[0].id
  } : {}
}

# What members told Krude is theirs, so it is under a key of this deployment's
# own, which no apply can destroy quietly (keys.tf says why).
resource "aws_kms_key" "agentcore_memory" {
  count                   = local.agentcore_memory
  description             = "${local.name} agents: AgentCore Memory, what Krude remembers"
  enable_key_rotation     = true
  deletion_window_in_days = 30

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "agentcore_memory" {
  count         = local.agentcore_memory
  name          = "alias/${local.name}-agentcore-memory"
  target_key_id = aws_kms_key.agentcore_memory[0].key_id
}

resource "aws_bedrockagentcore_memory" "krude" {
  count = local.agentcore_memory
  # Letters, digits and underscores only.
  name               = "${replace(local.name, "-", "_")}_krude"
  description        = "What Krude remembers about members across their threads"
  encryption_key_arn = aws_kms_key.agentcore_memory[0].arn
  # Days. Seven is AgentCore's minimum: the events are the conversation
  # itself, and the lasting facts are the strategy's records, not these.
  event_expiry_duration = 7
}

# One semantic strategy, whose records are what a search reads. The template
# must stay as adkmemory.agentCoreNamespaceTemplate has it: searches read
# exactly /m/<actor>/, and forgetting lists by /m/<organization>/.
resource "aws_bedrockagentcore_memory_strategy" "semantic" {
  count               = local.agentcore_memory
  name                = "krude_semantic"
  memory_id           = aws_bedrockagentcore_memory.krude[0].id
  type                = "SEMANTIC"
  description         = "Lasting facts about a member, from what they said"
  namespace_templates = ["/m/{actorId}/"]
}

# The oryxa task's use of the memory: writing a run's turns, searching an
# actor's records, and forgetting a member's or an organization's records and
# events. A policy of its own rather than a statement in the task policy
# (compute.tf), so that off, nothing in that policy changes.
resource "aws_iam_role_policy" "task_agentcore_memory" {
  count = local.agentcore_memory
  name  = "agentcore-memory"
  role  = aws_iam_role.task.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "AgentCoreMemory"
        Effect = "Allow"
        Action = [
          "bedrock-agentcore:CreateEvent", "bedrock-agentcore:ListEvents", "bedrock-agentcore:DeleteEvent",
          "bedrock-agentcore:ListActors", "bedrock-agentcore:ListSessions",
          "bedrock-agentcore:RetrieveMemoryRecords", "bedrock-agentcore:ListMemoryRecords",
          "bedrock-agentcore:BatchDeleteMemoryRecords",
        ]
        Resource = aws_bedrockagentcore_memory.krude[0].arn
      },
      {
        # The memory is under the key above; AgentCore uses it on the
        # caller's behalf, and only AgentCore may.
        Sid      = "AgentCoreMemoryKey"
        Effect   = "Allow"
        Action   = ["kms:Decrypt", "kms:GenerateDataKey", "kms:DescribeKey"]
        Resource = aws_kms_key.agentcore_memory[0].arn
        Condition = {
          StringEquals = { "kms:ViaService" = "bedrock-agentcore.${var.aws_region}.amazonaws.com" }
        }
      },
    ]
  })
}

output "agentcore_memory_id" {
  description = "The AgentCore memory's id (AGENTS_PRIMARY_MEMORY_ID), or empty while agentcore_memory_enabled is off."
  value       = var.agentcore_memory_enabled ? aws_bedrockagentcore_memory.krude[0].id : ""
}
