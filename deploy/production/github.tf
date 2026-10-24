# Releases from GitHub Actions (.github/workflows/deploy.yml). Its job assumes
# this role through GitHub's OIDC, and only while it runs in the repository's
# production environment (which only main may deploy to, on GitHub's own
# machines, never the self-hosted runners), so no AWS key is stored anywhere.
# The role can push the image and roll the service onto a task definition
# carrying it; everything else stays with `tofu apply`.

variable "github_oidc_subject_prefix" {
  description = "How GitHub's OIDC tokens for the releasing repository begin their sub claim. The repository uses immutable subjects, which name its owner and itself with their ids, so a renamed or recreated repository of the same name cannot take its place; `gh api repos/OWNER/REPO/actions/oidc/customization/sub` reports the prefix."
  type        = string
  default     = "repo:Shubham04-oss@218535561/oryxa-go@1368173110"

  # The trust below compares with StringEquals, so a wildcard would only lock
  # deploys out; refusing one keeps anyone from "fixing" that with StringLike,
  # which would let any branch, pull request or environment assume the role.
  validation {
    condition     = can(regex("^repo:[^:*?]+$", var.github_oidc_subject_prefix))
    error_message = "github_oidc_subject_prefix is repo:OWNER/REPO (immutable form allowed), with no wildcard and nothing after it."
  }
}

resource "aws_iam_openid_connect_provider" "github" {
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
}

resource "aws_iam_role" "github_deploy" {
  name = "${local.name}-github-deploy"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = aws_iam_openid_connect_provider.github.arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com"
          "token.actions.githubusercontent.com:sub" = "${var.github_oidc_subject_prefix}:environment:production"
        }
      }
    }]
  })
}

resource "aws_iam_role_policy" "github_deploy" {
  role = aws_iam_role.github_deploy.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "SignInToECR"
        Effect   = "Allow"
        Action   = "ecr:GetAuthorizationToken"
        Resource = "*"
      },
      {
        Sid    = "PushTheImage"
        Effect = "Allow"
        Action = [
          "ecr:BatchCheckLayerAvailability",
          "ecr:BatchGetImage",
          "ecr:CompleteLayerUpload",
          "ecr:GetDownloadUrlForLayer",
          "ecr:InitiateLayerUpload",
          "ecr:PutImage",
          "ecr:UploadLayerPart",
        ]
        Resource = aws_ecr_repository.oryxa.arn
      },
      {
        # Neither takes a resource narrower than this.
        Sid      = "TaskDefinitions"
        Effect   = "Allow"
        Action   = ["ecs:DescribeTaskDefinition", "ecs:RegisterTaskDefinition"]
        Resource = "*"
      },
      {
        # A registered revision keeps the tags OpenTofu gave the family.
        Sid      = "TagTaskDefinitions"
        Effect   = "Allow"
        Action   = "ecs:TagResource"
        Resource = "arn:aws:ecs:${var.aws_region}:${local.account_id}:task-definition/${aws_ecs_task_definition.oryxa.family}:*"
      },
      {
        Sid      = "RollTheService"
        Effect   = "Allow"
        Action   = ["ecs:DescribeServices", "ecs:UpdateService"]
        Resource = aws_ecs_service.oryxa.id
      },
      {
        Sid      = "PassTheTaskRoles"
        Effect   = "Allow"
        Action   = "iam:PassRole"
        Resource = [aws_iam_role.execution.arn, aws_iam_role.task.arn]
        Condition = {
          StringEquals = { "iam:PassedToService" = "ecs-tasks.amazonaws.com" }
        }
      },
    ]
  })
}

output "github_deploy_role_arn" {
  description = "The role .github/workflows/deploy.yml assumes."
  value       = aws_iam_role.github_deploy.arn
}
