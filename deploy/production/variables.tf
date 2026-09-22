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
  description = "Cheap until launch, when the database moves to Aurora Serverless v2."
  type        = string
  default     = "db.t4g.micro"
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
  })
  default = {
    user_pool_id    = ""
    client_id       = ""
    domain          = ""
    agent_client_id = ""
  }
}

variable "memory_agent_engine" {
  description = "The Vertex agent engine id holding Memory Bank (deploy/scripts/memory-bank.sh). Empty leaves memory off."
  type        = string
  default     = ""
}
