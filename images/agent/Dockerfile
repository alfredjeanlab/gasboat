# Gasboat agent image (multi-stage).
#
# Build (from repo root):
#   docker build -t gasboat-agent -f images/agent/Dockerfile .
#   docker build --target base -t gasboat-agent:base -f images/agent/Dockerfile .  # minimal variant

# ═════════════════════════════════════════════════════════════════
# Go builder — compiles gb (agent orchestration CLI) from source
# ═════════════════════════════════════════════════════════════════

FROM golang:1.25-bookworm AS gb-builder
WORKDIR /build
COPY controller/go.mod controller/go.sum ./controller/
RUN cd controller && go mod download
COPY controller/ ./controller/
RUN cd controller && CGO_ENABLED=0 go build -ldflags="-s -w" -o /gb ./cmd/gb/

# ═════════════════════════════════════════════════════════════════
# base — minimal agent with git, Node.js, Claude Code, coop, kd, gb
# ═════════════════════════════════════════════════════════════════

FROM ubuntu:24.04 AS base

ARG DEBIAN_FRONTEND=noninteractive
ARG NODE_VERSION=24
ARG COOP_VERSION=2026.224.0
ARG BEADS_VERSION=2026.60.2
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

# ── Gasboat CLI (gb) — agent orchestration ──────────────────────
COPY --from=gb-builder /gb /usr/local/bin/gb

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
COPY images/agent/hooks/ /hooks/
RUN chmod +x /hooks/*.sh

# ── Claudeless mock scenarios ────────────────────────────────────
COPY images/agent/scenarios/ /scenarios/

# ── kubectl ──────────────────────────────────────────────────────
RUN KUBECTL_VERSION=$(curl -fsSL https://dl.k8s.io/release/stable.txt) && \
    curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
      -o /usr/local/bin/kubectl && \
    chmod +x /usr/local/bin/kubectl

# ── Entrypoint ───────────────────────────────────────────────────
COPY images/agent/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

USER 1000
WORKDIR /home/agent/workspace

ENTRYPOINT ["/entrypoint.sh"]

# ═════════════════════════════════════════════════════════════════
# Full agent — base + language toolchains, CLIs, and dev tools
# ═════════════════════════════════════════════════════════════════

FROM base AS agent

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
    pkg-config libssl-dev \
    libcurl3t64-gnutls libicu74 libicu-dev \
    postgresql-client shellcheck \
    ffmpeg cmake libsdl2-dev && \
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
ARG CLAUDELESS_VERSION=v0.4.0
RUN case "${TARGETARCH}" in arm64) CL_ARCH=aarch64 ;; *) CL_ARCH=x86_64 ;; esac && \
    curl -fsSL "https://github.com/alfredjeanlab/claudeless/releases/download/${CLAUDELESS_VERSION}/claudeless-linux-${CL_ARCH}.tar.gz" \
      | tar -xz -C /usr/local/bin claudeless && \
    chmod +x /usr/local/bin/claudeless

# ── uv (Python package manager) ───────────────────────────────
RUN curl -LsSf https://astral.sh/uv/install.sh | env INSTALLER_NO_MODIFY_PATH=1 sh && \
    mv /root/.local/bin/uv /usr/local/bin/uv && \
    mv /root/.local/bin/uvx /usr/local/bin/uvx

# ── Bun ────────────────────────────────────────────────────────
ENV BUN_INSTALL=/usr/local/bun
RUN curl -fsSL https://bun.sh/install | BUN_INSTALL=${BUN_INSTALL} bash && \
    ln -sf ${BUN_INSTALL}/bin/bun /usr/local/bin/bun && \
    ln -sf ${BUN_INSTALL}/bin/bunx /usr/local/bin/bunx
ENV PATH="${BUN_INSTALL}/bin:${PATH}"

# ── Task CLI (Taskfile runner) ─────────────────────────────────
RUN GOPATH=/tmp/go /usr/local/go/bin/go install github.com/go-task/task/v3/cmd/task@latest && \
    mv /tmp/go/bin/task /usr/local/bin/task && \
    rm -rf /tmp/go

# ── Helm + plugins ───────────────────────────────────────────────
RUN curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
ENV HELM_PLUGINS=/usr/local/share/helm/plugins
RUN helm plugin install https://github.com/helm-unittest/helm-unittest && \
    helm plugin install https://github.com/chartmuseum/helm-push

# ── Terraform + Terragrunt ───────────────────────────────────────
ARG TERRAFORM_VERSION=1.11.3
ARG TERRAGRUNT_VERSION=0.77.11
RUN curl -fsSL "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip" \
      -o /tmp/terraform.zip && \
    unzip -q /tmp/terraform.zip -d /usr/local/bin && \
    rm /tmp/terraform.zip && \
    curl -fsSL "https://github.com/gruntwork-io/terragrunt/releases/download/v${TERRAGRUNT_VERSION}/terragrunt_linux_${TARGETARCH}" \
      -o /usr/local/bin/terragrunt && \
    chmod +x /usr/local/bin/terragrunt

# ── golangci-lint ────────────────────────────────────────────────
ARG GOLANGCI_LINT_VERSION=1.64.8
RUN curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
      | sh -s -- -b /usr/local/bin "v${GOLANGCI_LINT_VERSION}"

# ── quench (code quality checker) ────────────────────────────────
RUN cargo install quench --git https://github.com/alfredjeanlab/quench && \
    mv ${CARGO_HOME}/bin/quench /usr/local/bin/quench

# ── yq (YAML processor) ─────────────────────────────────────────
RUN YQ_VERSION=$(curl -fsSL "https://api.github.com/repos/mikefarah/yq/releases/latest" | jq -r .tag_name) && \
    curl -fsSL "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${TARGETARCH}" \
      -o /usr/local/bin/yq && \
    chmod +x /usr/local/bin/yq

# ── stern (K8s log streaming) ────────────────────────────────────
ARG STERN_VERSION=1.30.0
RUN curl -fsSL "https://github.com/stern/stern/releases/download/v${STERN_VERSION}/stern_${STERN_VERSION}_linux_${TARGETARCH}.tar.gz" \
      | tar -xz -C /usr/local/bin stern

# ── hadolint (Dockerfile linter) ─────────────────────────────────
RUN case "${TARGETARCH}" in arm64) HL_ARCH=arm64 ;; *) HL_ARCH=x86_64 ;; esac && \
    curl -fsSL "https://github.com/hadolint/hadolint/releases/latest/download/hadolint-Linux-${HL_ARCH}" \
      -o /usr/local/bin/hadolint && \
    chmod +x /usr/local/bin/hadolint

# ── RTK (token optimization proxy) ──────────────────────────────
ARG RTK_VERSION=0.22.2
RUN case "${TARGETARCH}" in arm64) RTK_TRIPLE=aarch64-unknown-linux-gnu ;; *) RTK_TRIPLE=x86_64-unknown-linux-gnu ;; esac && \
    curl -fsSL "https://github.com/rtk-ai/rtk/releases/download/v${RTK_VERSION}/rtk-${RTK_TRIPLE}.tar.gz" \
      | tar -xz -C /usr/local/bin rtk

# ── whisper-cli (audio transcription for JIRA video attachments) ──
ARG WHISPER_CPP_VERSION=1.8.3
RUN curl -fsSL "https://github.com/ggml-org/whisper.cpp/archive/refs/tags/v${WHISPER_CPP_VERSION}.tar.gz" \
      | tar -xz -C /tmp && \
    cmake -S /tmp/whisper.cpp-${WHISPER_CPP_VERSION} -B /tmp/whisper-build \
      -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF && \
    cmake --build /tmp/whisper-build --config Release -j$(nproc) --target whisper-cli && \
    cp /tmp/whisper-build/bin/whisper-cli /usr/local/bin/whisper-cli && \
    rm -rf /tmp/whisper.cpp-${WHISPER_CPP_VERSION} /tmp/whisper-build

# ── Playwright + Chromium (headless browser automation) ──────────
ARG PLAYWRIGHT_VERSION=1.50.1
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
RUN npm install -g playwright@${PLAYWRIGHT_VERSION} && \
    npx playwright install --with-deps chromium && \
    rm -rf /var/lib/apt/lists/* /tmp/* && \
    npm cache clean --force && \
    chmod -R 777 ${PLAYWRIGHT_BROWSERS_PATH}

USER 1000
