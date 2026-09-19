# Hyperdrive reaches the database through a Cloudflare Tunnel whose connector
# runs on this one small instance, inside the VPC. It accepts nothing inbound;
# Systems Manager is the administrative path. Only Hyperdrive's service token
# passes Cloudflare Access in front of the tunnel's hostname.

data "aws_ssm_parameter" "al2023_arm64" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

resource "aws_iam_role" "edge" {
  name = "${local.name}-edge"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "edge" {
  for_each = toset([
    "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
    "arn:aws:iam::aws:policy/CloudWatchAgentServerPolicy",
  ])
  role       = aws_iam_role.edge.name
  policy_arn = each.value
}

resource "aws_iam_role_policy" "edge_token" {
  role = aws_iam_role.edge.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "secretsmanager:GetSecretValue"
      Resource = aws_secretsmanager_secret.db_tunnel_token.arn
    }]
  })
}

resource "aws_iam_instance_profile" "edge" {
  name = "${local.name}-edge"
  role = aws_iam_role.edge.name
}

resource "aws_instance" "edge" {
  ami                         = data.aws_ssm_parameter.al2023_arm64.value
  instance_type               = "t4g.nano"
  subnet_id                   = aws_subnet.public[0].id
  vpc_security_group_ids      = [aws_security_group.edge.id]
  associate_public_ip_address = true
  iam_instance_profile        = aws_iam_instance_profile.edge.name

  metadata_options {
    http_tokens = "required"
  }

  root_block_device {
    volume_size = 8
    volume_type = "gp3"
    encrypted   = true
  }

  # cloudflared from Cloudflare's own repository, run as a service with the
  # tunnel's token, which it reads from Secrets Manager at boot.
  user_data_replace_on_change = true
  user_data                   = <<-EOT
    #!/bin/bash
    set -euo pipefail
    curl -fsSL https://pkg.cloudflare.com/cloudflared-ascii.repo -o /etc/yum.repos.d/cloudflared.repo
    dnf install -y cloudflared
    TOKEN=$(aws secretsmanager get-secret-value --region ${var.aws_region} \
      --secret-id ${aws_secretsmanager_secret.db_tunnel_token.arn} --query SecretString --output text)
    cloudflared service install "$TOKEN"
    systemctl enable --now cloudflared
  EOT

  tags = { Name = "${local.name}-hyperdrive-connector" }

  depends_on = [aws_secretsmanager_secret_version.db_tunnel_token]
}
