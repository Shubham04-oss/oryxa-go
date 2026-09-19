# Production: AWS us-west-2 for everything that runs, SES in ap-south-1 where
# its production access lives, Cloudflare at the edge, and Vertex AI in GCP for
# Gemini and Memory Bank. See ../README.md for how the pieces fit and the order
# they are applied in.

terraform {
  required_version = ">= 1.10"

  backend "s3" {
    bucket       = "oryxa-tofu-state-226236025533"
    key          = "production/terraform.tfstate"
    region       = "us-west-2"
    encrypt      = true
    use_lockfile = true
  }

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.0"
    }
    google = {
      source  = "hashicorp/google"
      version = ">= 6.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = local.tags
  }
}

# SES production access and the verified oryxa.in identity live here, so the
# rule forwarding SES's delivery events to this deployment's bus does too.
provider "aws" {
  alias  = "ses"
  region = var.ses_region

  default_tags {
    tags = local.tags
  }
}

# CLOUDFLARE_API_TOKEN in the environment; see ../README.md for its scopes.
provider "cloudflare" {}

provider "google" {
  project = var.gcp_project
  region  = var.gcp_region
}
