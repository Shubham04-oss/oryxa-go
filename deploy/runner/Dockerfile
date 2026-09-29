# The self-hosted GitHub Actions runner CI and CD run on (README.md here).
#
# GitHub's own runner image, plus what GitHub's hosted Ubuntu images carry and
# the workflows call: gh (the deploy records its release with `gh api`) and the
# AWS CLI (the deploy logs in to ECR and rolls ECS with it). Git, jq, curl,
# unzip, and Docker with buildx come with the base image; Go, Node and OpenTofu
# come from the workflows' setup actions.
FROM ghcr.io/actions/actions-runner:latest

USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl gnupg unzip jq \
 && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
      -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
      > /etc/apt/sources.list.d/github-cli.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends gh \
 && curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-$(uname -m).zip" -o /tmp/awscli.zip \
 && unzip -q /tmp/awscli.zip -d /tmp \
 && /tmp/aws/install \
 && rm -rf /tmp/aws /tmp/awscli.zip /var/lib/apt/lists/*

WORKDIR /home/runner
