# Durable single-user multi-agent workflows. A Standard Step Functions
# execution sends an identifier-only callback task to SQS and waits for the
# agents service to return the result with its task token. Assignment text,
# tool output, credentials, and provider payloads stay in PostgreSQL.

resource "aws_sqs_queue" "agent_workflow_dlq" {
  name                      = "${local.name}-agent-workflows-dlq"
  message_retention_seconds = 1209600
  sqs_managed_sse_enabled   = true
}

resource "aws_sqs_queue" "agent_workflows" {
  name                       = "${local.name}-agent-workflows"
  visibility_timeout_seconds = 900
  message_retention_seconds  = 345600
  receive_wait_time_seconds  = 20
  sqs_managed_sse_enabled    = true
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.agent_workflow_dlq.arn
    maxReceiveCount     = 5
  })
}

resource "aws_sqs_queue" "agent_workflow_schedule_dlq" {
  name                      = "${local.name}-agent-workflow-schedules-dlq"
  message_retention_seconds = 1209600
  sqs_managed_sse_enabled   = true
}

resource "aws_cloudwatch_log_group" "agent_workflows" {
  name              = "/${local.name}/agent-workflows"
  retention_in_days = 30
}

resource "aws_iam_role" "agent_workflows" {
  name = "${local.name}-agent-workflows"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "states.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = { StringEquals = { "aws:SourceAccount" = local.account_id } }
    }]
  })
}

resource "aws_iam_role_policy" "agent_workflows" {
  role = aws_iam_role.agent_workflows.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "DispatchCallbackTask"
        Effect   = "Allow"
        Action   = "sqs:SendMessage"
        Resource = aws_sqs_queue.agent_workflows.arn
      },
      {
        # Step Functions logging APIs do not support resource-level IAM.
        Sid    = "ExecutionLogging"
        Effect = "Allow"
        Action = [
          "logs:CreateLogDelivery", "logs:GetLogDelivery", "logs:UpdateLogDelivery",
          "logs:DeleteLogDelivery", "logs:ListLogDeliveries", "logs:PutResourcePolicy",
          "logs:DescribeResourcePolicies", "logs:DescribeLogGroups",
        ]
        Resource = "*"
      },
    ]
  })
}

resource "aws_sfn_state_machine" "agent_workflows" {
  name     = "${local.name}-agent-workflows"
  role_arn = aws_iam_role.agent_workflows.arn
  type     = "STANDARD"

  logging_configuration {
    log_destination        = "${aws_cloudwatch_log_group.agent_workflows.arn}:*"
    include_execution_data = false
    level                  = "ALL"
  }

  definition = jsonencode({
    Comment = "Dispatch one durable Oryxa agent-team workflow and wait for its callback"
    StartAt = "Has API execution id"
    States = {
      "Has API execution id" = {
        Type = "Choice"
        Choices = [{
          Variable  = "$.execution_id"
          IsPresent = true
          Next      = "Dispatch API execution"
        }]
        Default = "Dispatch scheduled execution"
      }
      "Dispatch API execution" = {
        Type           = "Task"
        Resource       = "arn:aws:states:::sqs:sendMessage.waitForTaskToken"
        TimeoutSeconds = 86400
        Parameters = {
          QueueUrl = aws_sqs_queue.agent_workflows.url
          MessageBody = {
            "task_token.$"                   = "$$.Task.Token"
            "step_functions_execution_arn.$" = "$$.Execution.Id"
            "execution_id.$"                 = "$.execution_id"
            "workflow_id.$"                  = "$.workflow_id"
            "organization_id.$"              = "$.organization_id"
            "owner_user_id.$"                = "$.owner_user_id"
            "started_by.$"                   = "$.started_by"
          }
        }
        End = true
      }
      "Dispatch scheduled execution" = {
        Type           = "Task"
        Resource       = "arn:aws:states:::sqs:sendMessage.waitForTaskToken"
        TimeoutSeconds = 86400
        Parameters = {
          QueueUrl = aws_sqs_queue.agent_workflows.url
          MessageBody = {
            "task_token.$"                   = "$$.Task.Token"
            "step_functions_execution_arn.$" = "$$.Execution.Id"
            "workflow_id.$"                  = "$.workflow_id"
            "organization_id.$"              = "$.organization_id"
            "owner_user_id.$"                = "$.owner_user_id"
            "started_by.$"                   = "$.started_by"
          }
        }
        End = true
      }
    }
  })
}

resource "aws_scheduler_schedule_group" "agent_workflows" {
  name = "${local.name}-agent-workflows"
}

resource "aws_iam_role" "agent_workflow_scheduler" {
  name = "${local.name}-agent-workflow-scheduler"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "scheduler.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = {
        StringEquals = { "aws:SourceAccount" = local.account_id }
        ArnLike      = { "aws:SourceArn" = "arn:aws:scheduler:${var.aws_region}:${local.account_id}:schedule/${aws_scheduler_schedule_group.agent_workflows.name}/*" }
      }
    }]
  })
}

resource "aws_iam_role_policy" "agent_workflow_scheduler" {
  role = aws_iam_role.agent_workflow_scheduler.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = "states:StartExecution"
        Resource = aws_sfn_state_machine.agent_workflows.arn
      },
      {
        Effect   = "Allow"
        Action   = "sqs:SendMessage"
        Resource = aws_sqs_queue.agent_workflow_schedule_dlq.arn
      },
    ]
  })
}

# The ECS task starts manual executions, manages member-owned schedules, and
# returns task-token callbacks. Schedule targets themselves use the narrower
# role above.
resource "aws_iam_role_policy" "task_agent_workflows" {
  role = aws_iam_role.task.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["states:StartExecution", "states:SendTaskSuccess", "states:SendTaskFailure", "states:SendTaskHeartbeat"]
        Resource = "*"
      },
      {
        Effect   = "Allow"
        Action   = ["scheduler:CreateSchedule", "scheduler:UpdateSchedule", "scheduler:DeleteSchedule", "scheduler:GetSchedule"]
        Resource = "arn:aws:scheduler:${var.aws_region}:${local.account_id}:schedule/${aws_scheduler_schedule_group.agent_workflows.name}/*"
      },
      {
        Effect    = "Allow"
        Action    = "iam:PassRole"
        Resource  = aws_iam_role.agent_workflow_scheduler.arn
        Condition = { StringEquals = { "iam:PassedToService" = "scheduler.amazonaws.com" } }
      },
      {
        Effect   = "Allow"
        Action   = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility", "sqs:GetQueueAttributes"]
        Resource = aws_sqs_queue.agent_workflows.arn
      },
    ]
  })
}

resource "aws_cloudwatch_metric_alarm" "agent_workflow_failures" {
  alarm_name          = "${local.name}-agent-workflow-failures"
  alarm_description   = "A durable agent workflow execution failed"
  namespace           = "AWS/States"
  metric_name         = "ExecutionsFailed"
  statistic           = "Sum"
  period              = 60
  evaluation_periods  = 1
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  dimensions          = { StateMachineArn = aws_sfn_state_machine.agent_workflows.arn }
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}
resource "aws_cloudwatch_metric_alarm" "agent_workflow_dead_letters" {
  for_each            = { callback = aws_sqs_queue.agent_workflow_dlq.name, schedule = aws_sqs_queue.agent_workflow_schedule_dlq.name }
  alarm_name          = "${local.name}-agent-workflow-${each.key}-dead-letters"
  alarm_description   = "Agent workflow ${each.key} dead-letter queue is not empty"
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 1
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  dimensions          = { QueueName = each.value }
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}
