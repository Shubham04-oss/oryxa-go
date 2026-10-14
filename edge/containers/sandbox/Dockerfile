# The room sandbox (A1): what an agent's sandbox_* tools run in. Python,
# Node/TypeScript and common CLI tools, plus sandbox-shim, the helper that
# @cloudflare/sandbox's Files and DirectoryBackup run. It must be the same
# release as the package (edge/package.json, exactly); version.node.test.ts
# fails when they drift. Cloudflare runs linux/amd64.
FROM docker.io/cloudflare/sandbox:1.0.1 AS shim

FROM docker.io/library/python:3.13-slim-bookworm

ENV DEBIAN_FRONTEND=noninteractive \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    NODE_MAJOR=22

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash ca-certificates curl wget git jq unzip zip xz-utils file less procps \
      build-essential sqlite3 ripgrep fd-find tree \
 && curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && npm install --global tsx@4 typescript@5 \
 && pip install numpy pandas matplotlib requests beautifulsoup4 openpyxl pypdf pillow \
 && apt-get clean && rm -rf /var/lib/apt/lists/* /root/.npm

COPY --from=shim /usr/local/bin/sandbox-shim /usr/local/bin/sandbox-shim

# Agents work as an unprivileged user in /workspace.
RUN useradd --create-home --home-dir /home/agent --uid 1000 agent \
 && mkdir -p /workspace && chown agent:agent /workspace
WORKDIR /workspace
ENV HOME=/home/agent MPLBACKEND=Agg

# The code kernels keep exec/code's state between calls (A12): Python on
# 8899, JavaScript and TypeScript on 8898, both run as the agent user by
# start.sh. Previews can't use those two ports.
COPY kernel.py kernel.mjs start.sh /opt/oryxa/
ENV ORYXA_WORKSPACE=/workspace ORYXA_TYPESCRIPT=/usr/lib/node_modules/typescript/lib/typescript.js
CMD ["/opt/oryxa/start.sh"]
