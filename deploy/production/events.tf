# The deployment's own bus. Every service publishes here through its outbox;
# each service's provisioner (deploy/scripts/provision-app.sh) makes its queue
# and the rule feeding it, from the same subscription list its consumer is
# tested against.

resource "aws_cloudwatch_event_bus" "main" {
  name = local.name
}

# Keep what crossed the bus for a week, so an event a consumer lost can be
# replayed into its queue.
resource "aws_cloudwatch_event_archive" "main" {
  name             = local.name
  event_source_arn = aws_cloudwatch_event_bus.main.arn
  retention_days   = 7
}

# SES publishes its delivery events (bounces, complaints, deliveries) only to
# the default bus of the region it sends from. Email's feedback rule listens
# on this region's default bus, so they are forwarded here.
data "aws_cloudwatch_event_bus" "default" {
  name = "default"
}

resource "aws_iam_role" "ses_forward" {
  name = "${local.name}-ses-forward"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "ses_forward" {
  role = aws_iam_role.ses_forward.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "events:PutEvents"
      Resource = data.aws_cloudwatch_event_bus.default.arn
    }]
  })
}

resource "aws_cloudwatch_event_rule" "ses_forward" {
  provider       = aws.ses
  name           = "${local.name}-ses-feedback"
  description    = "SES events for ${local.ses_configuration_set}, forwarded to ${var.aws_region}"
  event_bus_name = "default"
  event_pattern = jsonencode({
    source = ["aws.ses"]
    detail = { mail = { tags = { "ses:configuration-set" = [local.ses_configuration_set] } } }
  })
}

resource "aws_cloudwatch_event_target" "ses_forward" {
  provider       = aws.ses
  rule           = aws_cloudwatch_event_rule.ses_forward.name
  event_bus_name = "default"
  arn            = data.aws_cloudwatch_event_bus.default.arn
  role_arn       = aws_iam_role.ses_forward.arn
}
