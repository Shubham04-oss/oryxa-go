# Retrieval's managed engine: a Bedrock knowledge base that parses (PDF and
# Office included), chunks, embeds and retrieves. Rag pushes each document
# into it with the organization and corpus it belongs to, and filters every
# retrieval by them; rag, not the knowledge base, decides which corpora a
# caller may search.
#
# There are two while rag moves from one to the other; rag_knowledge_base
# says which one rag uses.
#
# - managed: Bedrock's MANAGED kind. Bedrock keeps the index itself, searches
#   it by keyword and by meaning together, and its parser, embedding model and
#   reranker come with the retrieval price. Its custom connector takes one
#   pushed document at a time with its tenant's metadata, which is how rag
#   indexes.
# - main: the VECTOR kind over an S3 Vectors index, which Bedrock searches by
#   meaning only. It stays until the managed one has run a while, so going
#   back is a variable, and is then removed.

locals {
  knowledge_base_embedding_model = "arn:aws:bedrock:${var.aws_region}::foundation-model/amazon.titan-embed-text-v2:0"
  knowledge_base_documents       = "${local.name}-kb-documents-${local.account_id}"

  # The knowledge base rag uses, in the words rag is configured with.
  rag_knowledge_base = var.rag_knowledge_base == "managed" ? {
    id             = aws_bedrockagent_knowledge_base.managed.id
    data_source_id = aws_bedrockagent_data_source.managed.data_source_id
    type           = "MANAGED"
    } : {
    id             = aws_bedrockagent_knowledge_base.main.id
    data_source_id = aws_bedrockagent_data_source.main.data_source_id
    type           = "VECTOR"
  }
}

resource "aws_s3vectors_vector_bucket" "knowledge_base" {
  vector_bucket_name = "${local.name}-kb-${local.account_id}"
}

resource "aws_s3vectors_index" "knowledge_base" {
  vector_bucket_name = aws_s3vectors_vector_bucket.knowledge_base.vector_bucket_name
  index_name         = "documents"
  data_type          = "float32"
  dimension          = 1024
  distance_metric    = "cosine"

  metadata_configuration {
    # Bedrock keeps each chunk's text and its own bookkeeping in these. Neither
    # is filtered on, and a vector's filterable metadata is capped at 2 KB.
    non_filterable_metadata_keys = ["AMAZON_BEDROCK_TEXT", "AMAZON_BEDROCK_METADATA"]
  }
}

# What the knowledge base reads by location rather than by value: originals
# knowledge does not read itself (PDF, Office), which Bedrock parses, and text
# too large to hand it inline, which rag stages here until it is indexed.
resource "aws_s3_bucket" "knowledge_base_documents" {
  bucket = local.knowledge_base_documents
}

resource "aws_s3_bucket_public_access_block" "knowledge_base_documents" {
  bucket                  = aws_s3_bucket.knowledge_base_documents.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "knowledge_base_documents" {
  bucket = aws_s3_bucket.knowledge_base_documents.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "knowledge_base_documents" {
  bucket = aws_s3_bucket.knowledge_base_documents.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "knowledge_base_documents" {
  bucket = aws_s3_bucket.knowledge_base_documents.id

  rule {
    id     = "abandoned-uploads"
    status = "Enabled"
    filter {}
    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }

  # Staged text is needed only until the knowledge base has read it; rag
  # deletes it then, and this catches what a failure left behind.
  rule {
    id     = "staged-text"
    status = "Enabled"
    filter {
      prefix = "rag-staging/"
    }
    expiration {
      days = 7
    }
  }
}

resource "aws_iam_role" "knowledge_base" {
  name = "${local.name}-knowledge-base"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "bedrock.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = {
        StringEquals = { "aws:SourceAccount" = local.account_id }
        ArnLike      = { "aws:SourceArn" = "arn:aws:bedrock:${var.aws_region}:${local.account_id}:knowledge-base/*" }
      }
    }]
  })
}

resource "aws_iam_role_policy" "knowledge_base" {
  role = aws_iam_role.knowledge_base.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "Embed"
        Effect   = "Allow"
        Action   = "bedrock:InvokeModel"
        Resource = [local.knowledge_base_embedding_model, "arn:aws:bedrock:${var.aws_region}::foundation-model/${var.rerank_model}"]
      },
      {
        # Reranking inside a retrieval runs as this role.
        Sid      = "Rerank"
        Effect   = "Allow"
        Action   = "bedrock:Rerank"
        Resource = "*"
      },
      {
        Sid      = "Vectors"
        Effect   = "Allow"
        Action   = ["s3vectors:PutVectors", "s3vectors:GetVectors", "s3vectors:DeleteVectors", "s3vectors:QueryVectors", "s3vectors:ListVectors", "s3vectors:GetIndex"]
        Resource = aws_s3vectors_index.knowledge_base.index_arn
      },
      {
        Sid      = "ReadDocuments"
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:ListBucket"]
        Resource = [aws_s3_bucket.knowledge_base_documents.arn, "${aws_s3_bucket.knowledge_base_documents.arn}/*"]
      },
    ]
  })
}

resource "aws_bedrockagent_knowledge_base" "main" {
  name        = local.name
  description = "What rag indexes: every organization's corpora, told apart by metadata"
  role_arn    = aws_iam_role.knowledge_base.arn

  knowledge_base_configuration {
    type = "VECTOR"
    vector_knowledge_base_configuration {
      embedding_model_arn = local.knowledge_base_embedding_model
      embedding_model_configuration {
        bedrock_embedding_model_configuration {
          dimensions          = 1024
          embedding_data_type = "FLOAT32"
        }
      }
    }
  }

  storage_configuration {
    type = "S3_VECTORS"
    s3_vectors_configuration {
      index_arn = aws_s3vectors_index.knowledge_base.index_arn
    }
  }

  depends_on = [aws_iam_role_policy.knowledge_base]
}

# Rag's documents, pushed one at a time. Hierarchical chunking retrieves the
# small chunk that matched and answers with the larger one around it.
resource "aws_bedrockagent_data_source" "main" {
  knowledge_base_id    = aws_bedrockagent_knowledge_base.main.id
  name                 = "rag"
  data_deletion_policy = "DELETE"

  data_source_configuration {
    type = "CUSTOM"
  }

  vector_ingestion_configuration {
    chunking_configuration {
      chunking_strategy = "HIERARCHICAL"
      hierarchical_chunking_configuration {
        level_configuration {
          max_tokens = 1500
        }
        level_configuration {
          max_tokens = 300
        }
        overlap_tokens = 60
      }
    }
  }
}

# The managed knowledge base. It reads only what rag points it at by
# location: originals knowledge stores (PDF, Office) and text rag stages. Its
# embedding model and reranker are Bedrock's own, so it is granted no model.
resource "aws_iam_role" "managed_knowledge_base" {
  name = "${local.name}-managed-knowledge-base"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "bedrock.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = {
        StringEquals = { "aws:SourceAccount" = local.account_id }
        ArnLike      = { "aws:SourceArn" = "arn:aws:bedrock:${var.aws_region}:${local.account_id}:knowledge-base/*" }
      }
    }]
  })
}

resource "aws_iam_role_policy" "managed_knowledge_base" {
  role = aws_iam_role.managed_knowledge_base.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid      = "ReadDocuments"
      Effect   = "Allow"
      Action   = ["s3:GetObject", "s3:ListBucket"]
      Resource = [aws_s3_bucket.knowledge_base_documents.arn, "${aws_s3_bucket.knowledge_base_documents.arn}/*"]
    }]
  })
}

resource "aws_bedrockagent_knowledge_base" "managed" {
  name        = "${local.name}-managed"
  description = "What rag indexes: every organization's corpora, told apart by metadata"
  role_arn    = aws_iam_role.managed_knowledge_base.arn

  knowledge_base_configuration {
    type = "MANAGED"
    managed_knowledge_base_configuration {
      embedding_model_type = "MANAGED"
    }
  }

  depends_on = [aws_iam_role_policy.managed_knowledge_base]
}

# Rag's documents, pushed one at a time; there is nothing to crawl. The
# connector's own access lists stay off: they filter by an end user's email,
# which Bedrock itself says is not a security boundary, and rag already scopes
# every search by organization and corpus. The file size limit and reading
# the pictures inside a document are Bedrock's defaults, written down because
# the provider fails an apply whose result differs from what it asked for.
resource "aws_bedrockagent_data_source" "managed" {
  knowledge_base_id    = aws_bedrockagent_knowledge_base.managed.id
  name                 = "rag"
  data_deletion_policy = "DELETE"

  data_source_configuration {
    type = "MANAGED_KNOWLEDGE_BASE_CONNECTOR"
    managed_knowledge_base_connector_configuration {
      connector_parameters = jsonencode({
        type                = "CUSTOM"
        version             = "1"
        aclEnabled          = false
        filterConfiguration = { maxFileSizeInMegaBytes = "500" }
      })

      media_extraction_configuration {
        image_extraction_configuration {
          image_extraction_status = "ENABLED"
        }
      }
    }
  }
}
