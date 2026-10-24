variable "environment" {
  description = "Names every resource, so a staging copy of this configuration can share the account."
  type        = string
  default     = "production"
}

variable "aws_region" {
  type    = string
  default = "us-west-2"
}

variable "ses_region" {
  description = "Where SES production access and the verified domain are. Mail is sent from here."
  type        = string
  default     = "ap-south-1"
}

variable "domain" {
  description = "The Cloudflare zone the deployment's hostnames live in."
  type        = string
  default     = "oryxa.in"
}

variable "cloudflare_account_id" {
  type    = string
  default = "f50cbb237cd6f0ab48cc3679f727d132"
}

variable "gcp_project" {
  type    = string
  default = "internalbeta"
}

variable "gcp_region" {
  description = "Where the Memory Bank agent engine lives: Oregon, beside us-west-2."
  type        = string
  default     = "us-west1"
}

variable "jev_base_url" {
  description = "Where Jev's System One requests go. Empty is TypeSafe. For Cloudflare's Clef, its Workers AI run endpoint, e.g. https://api.cloudflare.com/client/v4/accounts/<account>/ai/run/@cf/cloudflare/clef-flash, with a Cloudflare API token (Workers AI) stored as TYPESAFE_API_KEY."
  type        = string
  default     = ""
  validation {
    condition     = var.jev_base_url == "" || startswith(var.jev_base_url, "https://")
    error_message = "jev_base_url must be HTTPS: every request carries the API key."
  }
}

variable "knowledge_judge" {
  description = "What judges whether a connected app's document is worth indexing: jev (TypeSafe; put TYPESAFE_API_KEY first), gemini (var.model), or off."
  type        = string
  default     = "gemini"
  validation {
    condition     = contains(["jev", "gemini", "off"], var.knowledge_judge)
    error_message = "knowledge_judge must be jev, gemini or off."
  }
}

variable "model" {
  description = "The Gemini model Krude and rag's answers use."
  type        = string
  default     = "gemini-3.5-flash-lite"
}

variable "rag_default_backend" {
  description = "Where rag puts a new corpus: bedrock-kb for the knowledge base. Empty keeps rag's own default."
  type        = string
  default     = "bedrock-kb"
}

variable "rag_knowledge_base" {
  description = "The Bedrock knowledge base rag uses: managed (Bedrock keeps the index and searches it by keyword and meaning) or vector (the S3 Vectors one, until it is removed). Moving between them re-indexes every bedrock-kb document; see rag/README.md."
  type        = string
  default     = "managed"

  validation {
    condition     = contains(["managed", "vector"], var.rag_knowledge_base)
    error_message = "rag_knowledge_base is managed or vector."
  }
}

variable "rerank_model" {
  description = "The Bedrock model rag reranks with, in-region. Amazon Rerank needs no Marketplace subscription."
  type        = string
  default     = "amazon.rerank-v1:0"
}

variable "image_tag" {
  description = "The oryxa image tag in ECR to run. deploy/scripts/release.sh pushes one and applies it."
  type        = string
  default     = "bootstrap"
}

variable "desired_count" {
  description = "How many oryxa tasks run. Zero until the first image is pushed."
  type        = number
  default     = 0
}

variable "task_cpu" {
  type    = number
  default = 1024
}

variable "task_memory" {
  type    = number
  default = 2048
}

variable "database_instance_class" {
  description = "Cheap until launch, when the database moves to Aurora Serverless v2. A db.t4g.small (2 GiB) is headroom over the db.t4g.micro (1 GiB) it replaced, not a repair: measured over 7 days, the micro had 86 to 140 MiB free, 44 to 82 MiB parked in swap but only about 0.5 KB/s of swap traffic, and CPU at 5%, so it was not swapping constantly."
  type        = string
  default     = "db.t4g.small"
}

variable "alarm_email" {
  description = "Where alarms are sent. The subscription has to be confirmed from the email SNS sends."
  type        = string
  default     = "shubham@oryxa.in"
}

variable "cognito" {
  description = "What auth's Cognito provisioner answered (deploy/scripts/provision-app.sh writes it to app.auto.tfvars.json)."
  type = object({
    user_pool_id    = string
    client_id       = string
    domain          = string
    agent_client_id = string
    cli_client_id   = optional(string, "")
  })
  default = {
    user_pool_id    = ""
    client_id       = ""
    domain          = ""
    agent_client_id = ""
    cli_client_id   = ""
  }
}

variable "realtime_purge" {
  description = "Whether realtime has the web app's Worker purge a member's direct messages and side conversations when they leave an organization or it is deleted. Set in realtime.auto.tfvars.json, so every checkout applies the same. Turn it on only once the Worker at app.oryxa.in serves /internal/realtime/purge and REALTIME_PURGE_SECRET is stored on both sides; deploy/README.md gives the order."
  type        = bool
  default     = false
}

variable "memory_agent_engine" {
  description = "The Vertex agent engine id holding Memory Bank (deploy/scripts/memory-bank.sh). Empty leaves memory off."
  type        = string
  default     = ""
}

variable "restore_snapshot_identifier" {
  description = "An RDS snapshot to create the database from, for a restore only (e.g. oryxa-production-final). Null creates an empty database."
  type        = string
  default     = null
}
