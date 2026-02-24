# Gasboat agent images (multi-stage).
#
# Targets:
#   slim     — minimal: git, Node.js, Claude Code, coop, kd, hooks
#   omnibus  — slim + Go, Rust, uv, bun, kubectl, Helm, Task, AWS/Docker/GitHub/GitLab CLIs
#
# Build:
#   docker build --target slim    -t gasboat-agent:slim    images/agent/
#   docker build --target omnibus -t gasboat-agent:omnibus  images/agent/

FROM ubuntu:24.04 AS slim

ARG DEBIAN_FRONTEND=noninteractive
ARG NODE_VERSION=24
ARG COOP_VERSION=2026.221.0
ARG BEADS_VERSION=v0.3.1
ARG TARGETARCH

# ── System packages (minimal) ─────────────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl git git-lfs jq xz-utils ca-certificates screen \
    openssh-client sudo \
    python3 python3-pip python3-venv && \
    ln -sf /usr/bin/python3 /usr/bin/python && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# ── Node.js ──────────────────────────────────────────────────────
RUN case "${TARGETARCH}" in arm64) NODE_ARCH=arm64 ;; *) NODE_ARCH=x64 ;; esac && \
    NODE_FULL=$(curl -fsSL "https://nodejs.org/dist/index.json" \
      | jq -r "[.[] | select(.version | startswith(\"v${NODE_VERSION}.\"))][0].version") && \
    [ "${NODE_FULL}" != "null" ] && [ -n "${NODE_FULL}" ] && \
    curl -fsSL "https://nodejs.org/dist/${NODE_FULL}/node-${NODE_FULL}-linux-${NODE_ARCH}.tar.xz" \
      | tar -xJ --strip-components=1 -C /usr/local

# ── Claude Code ──────────────────────────────────────────────────
RUN npm install -g @anthropic-ai/claude-code

# ── Coop (terminal session manager) ─────────────────────────────
RUN set -e && \
    if [ -z "${COOP_VERSION}" ]; then \
      COOP_VERSION=$(curl -fsSL "https://api.github.com/repos/groblegark/coop/releases/latest" | jq -r .tag_name); \
    fi && \
    case "${TARGETARCH}" in arm64) COOP_ARCH=arm64 ;; *) COOP_ARCH=amd64 ;; esac && \
    curl -fsSL "https://github.com/groblegark/coop/releases/download/${COOP_VERSION}/coop-linux-${COOP_ARCH}.tar.gz" \
      | tar -xz -C /usr/local/bin coop

# ── Beads CLI (kd) ──────────────────────────────────────────────
RUN set -e && \
    if [ -z "${BEADS_VERSION}" ]; then \
      BEADS_VERSION=$(curl -fsSL "https://api.github.com/repos/groblegark/kbeads/releases/latest" | jq -r .tag_name); \
    fi && \
    case "${TARGETARCH}" in arm64) BD_ARCH=aarch64 ;; *) BD_ARCH=x86_64 ;; esac && \
    curl -fsSL "https://github.com/groblegark/kbeads/releases/download/${BEADS_VERSION}/kd-linux-${BD_ARCH}.tar.gz" \
      | tar -xz -C /usr/local/bin kd

# ── Agent user ───────────────────────────────────────────────────
RUN sed -i 's/^ubuntu:/agent:/' /etc/passwd && \
    sed -i 's|/home/ubuntu|/home/agent|' /etc/passwd && \
    sed -i 's|/bin/sh|/bin/bash|' /etc/passwd && \
    sed -i 's/^ubuntu:/agent:/' /etc/group && \
    mkdir -p /home/agent/workspace && \
    chown -R 1000:1000 /home/agent && \
    echo 'agent ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/agent && \
    chmod 0440 /etc/sudoers.d/agent

# ── Hook scripts ─────────────────────────────────────────────────
COPY hooks/ /hooks/
RUN chmod +x /hooks/*.sh

# ── Entrypoint ───────────────────────────────────────────────────
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

USER 1000
WORKDIR /home/agent/workspace

ENTRYPOINT ["/entrypoint.sh"]

# ═════════════════════════════════════════════════════════════════
# omnibus — full dev-environment with language toolchains and CLIs
# ═════════════════════════════════════════════════════════════════

FROM slim AS omnibus

USER root
ARG DEBIAN_FRONTEND=noninteractive
ARG GO_VERSION=1.25
ARG DOCKER_VERSION=27.5.1
ARG TARGETARCH

# ── Additional system packages ───────────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends \
    make unzip xz-utils \
    python3 python3-pip python3-venv \
    gcc g++ libc6-dev binutils \
    libcurl3t64-gnutls libicu74 libicu-dev && \
    ln -sf /usr/bin/python3 /usr/bin/python && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# ── Go + gopls ───────────────────────────────────────────────────
RUN GO_LATEST=$(curl -fsSL "https://go.dev/dl/?mode=json" \
      | jq -r "[.[] | select(.version | startswith(\"go${GO_VERSION}\"))][0].version") && \
    [ "${GO_LATEST}" != "null" ] && [ -n "${GO_LATEST}" ] && \
    curl -fsSL "https://go.dev/dl/${GO_LATEST}.linux-${TARGETARCH}.tar.gz" | tar -C /usr/local -xz && \
    GOPATH=/tmp/go /usr/local/go/bin/go install golang.org/x/tools/gopls@latest && \
    mv /tmp/go/bin/gopls /usr/local/go/bin/gopls && \
    rm -rf /tmp/go
ENV PATH="/usr/local/go/bin:${PATH}"

# ── Rust toolchain (compiler + cargo + rust-analyzer) ───────────
ENV RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo
ENV PATH="${CARGO_HOME}/bin:${PATH}"
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \
      | sh -s -- -y --default-toolchain stable --profile default \
          --component rust-analyzer && \
    chmod -R a+rw ${RUSTUP_HOME} ${CARGO_HOME}

# ── kubectl ──────────────────────────────────────────────────────
RUN KUBECTL_VERSION=$(curl -fsSL https://dl.k8s.io/release/stable.txt) && \
    curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
      -o /usr/local/bin/kubectl && \
    chmod +x /usr/local/bin/kubectl

# ── GitHub CLI ───────────────────────────────────────────────────
RUN GH_VERSION=$(curl -fsSL "https://api.github.com/repos/cli/cli/releases/latest" \
      | jq -r .tag_name | sed "s/^v//") && \
    curl -fsSL "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz" \
      | tar -xz --strip-components=2 -C /usr/local/bin "gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh"

# ── GitLab CLI ──────────────────────────────────────────────────
RUN GLAB_VERSION=$(curl -fsSL "https://gitlab.com/api/v4/projects/gitlab-org%2Fcli/releases" \
      | jq -r '.[0].tag_name | ltrimstr("v")') && \
    curl -fsSL "https://gitlab.com/gitlab-org/cli/-/releases/v${GLAB_VERSION}/downloads/glab_${GLAB_VERSION}_linux_${TARGETARCH}.tar.gz" \
      | tar -xz --strip-components=1 -C /usr/local/bin bin/glab

# ── Docker CLI (client only) ─────────────────────────────────────
RUN case "${TARGETARCH}" in arm64) DOCKER_ARCH=aarch64 ;; *) DOCKER_ARCH=x86_64 ;; esac && \
    curl -fsSL "https://download.docker.com/linux/static/stable/${DOCKER_ARCH}/docker-${DOCKER_VERSION}.tgz" \
      | tar -xz --strip-components=1 -C /usr/local/bin docker/docker

# ── AWS CLI ──────────────────────────────────────────────────────
RUN case "${TARGETARCH}" in arm64) AWS_ARCH=aarch64 ;; *) AWS_ARCH=x86_64 ;; esac && \
    curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-${AWS_ARCH}.zip" -o /tmp/awscli.zip && \
    unzip -q /tmp/awscli.zip -d /tmp && \
    /tmp/aws/install --install-dir /opt/aws-cli --bin-dir /usr/local/bin && \
    AWSVER=$(ls /opt/aws-cli/v2/ | grep -v current | head -1) && \
    ln -sfn "/opt/aws-cli/v2/${AWSVER}" /opt/aws-cli/v2/current && \
    rm -rf /tmp/awscli.zip /tmp/aws

# ── Claudeless (Claude CLI simulator for E2E testing) ────────────
RUN CLAUDELESS_VERSION=$(curl -fsSL "https://api.github.com/repos/alfredjeanlab/claudeless/releases/latest" | jq -r .tag_name) && \
    case "${TARGETARCH}" in arm64) CL_ARCH=aarch64 ;; *) CL_ARCH=x86_64 ;; esac && \
    curl -fsSL "https://github.com/alfredjeanlab/claudeless/releases/download/${CLAUDELESS_VERSION}/claudeless-linux-${CL_ARCH}.tar.gz" \
      | tar -xz -C /usr/local/bin claudeless && \
    chmod +x /usr/local/bin claudeless

# ── uv (Python package manager) ───────────────────────────────
RUN curl -LsSf https://astral.sh/uv/install.sh | env INSTALLER_NO_MODIFY_PATH=1 sh && \
    mv /root/.local/bin/uv /usr/local/bin/uv && \
    mv /root/.local/bin/uvx /usr/local/bin/uvx

# ── Bun ────────────────────────────────────────────────────────
ENV BUN_INSTALL=/usr/local/bun
RUN curl -fsSL https://bun.sh/install | bash && \
    ln -sf ${BUN_INSTALL}/bin/bun /usr/local/bin/bun && \
    ln -sf ${BUN_INSTALL}/bin/bunx /usr/local/bin/bunx
ENV PATH="${BUN_INSTALL}/bin:${PATH}"

# ── Task CLI (Taskfile runner) ─────────────────────────────────
RUN GOPATH=/tmp/go /usr/local/go/bin/go install github.com/go-task/task/v3/cmd/task@latest && \
    mv /tmp/go/bin/task /usr/local/bin/task && \
    rm -rf /tmp/go

# ── Helm ───────────────────────────────────────────────────────
RUN curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash

USER 1000
