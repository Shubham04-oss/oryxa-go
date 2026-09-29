# What the 2026-09-28 launch review found missing (docs/REMAINING-WORK.md,
# section 7), kept in one file so it can be read, reviewed and applied as one
# change. Nothing here changes the running service's behavior.

variable "first_deploy" {
  description = "True only for the first apply into an empty account (deploy/README.md, First deploy). deploy/scripts/provision-app.sh makes the files and artifacts buckets after that apply, so their versioning and lifecycle rules wait for the next one."
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
