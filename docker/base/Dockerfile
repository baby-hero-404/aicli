# syntax=docker/dockerfile:1.7
#
# aicli base image: the foundation every runtime image builds on.
# Contains the agent CLIs (Claude Code, Codex, Antigravity), common build tools and a
# non-root "agent" user whose home is /home/agent (sandbox.HomeDir).
#
# Build:  make image-base   (or: docker build -t aicli/base:latest docker/base)

# Official Node.js image, pinned by digest for reproducible builds (the CLIs need Node 22+).
# Refresh the tag and digest periodically to pick up security fixes.
ARG NODE_IMAGE=node:24-bookworm-slim@sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5
FROM ${NODE_IMAGE}

# Agent CLI versions are pinned: a surprise upgrade must never change behavior silently.
ARG CLAUDE_CODE_VERSION=2.1.312
ARG CODEX_VERSION=0.165.0
# UID/GID of the agent user. Match the host user that owns bind-mounted workspaces to avoid
# permission errors (the Makefile passes the invoking user's IDs).
ARG USER_UID=1000
ARG USER_GID=1000
# Antigravity CLI (agy). Its official installer (antigravity.google/cli/install.sh) only reads a
# release manifest, downloads one tarball and checks its SHA-512, so the build does those steps
# itself with the values pinned here instead of running the script. To upgrade, read
# https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_<amd64|arm64>.json
# and copy version, url and sha512.
ARG AGY_VERSION=1.4.10
ARG AGY_URL_AMD64=https://storage.googleapis.com/antigravity-public/antigravity-cli/1.4.10-5501573669978112/linux-x64/cli_linux_x64.tar.gz
ARG AGY_SHA512_AMD64=3a14c21697e86349ce8402e0b1889cbd1d3a5f4e2cb4dc43aae1c48c404240c2ccff9f775eec277e1f759234a33ec026645c63ed22ab6f6ebede561eda7c38b6
ARG AGY_URL_ARM64=https://storage.googleapis.com/antigravity-public/antigravity-cli/1.4.10-5501573669978112/linux-arm/cli_linux_arm64.tar.gz
ARG AGY_SHA512_ARM64=0182c23882b174c6f2968ad88de43262692ec363a938031319131551dab41cc6aca573d28d264095db357e49881a211114afe9f974386ed37208760f401ba98c
# Set by BuildKit to the platform being built (amd64, arm64).
ARG TARGETARCH

LABEL org.opencontainers.image.title="aicli-base" \
      org.opencontainers.image.description="Base sandbox image for AI coding CLIs" \
      org.opencontainers.image.source="https://github.com/baby-hero-404/aicli"

ENV DEBIAN_FRONTEND=noninteractive

RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    rm -f /etc/apt/apt.conf.d/docker-clean \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        git \
        make \
        patch \
        tini \
    && rm -rf /var/lib/apt/lists/*

RUN --mount=type=cache,target=/root/.npm \
    npm install -g --no-audit --no-fund \
        "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
        "@openai/codex@${CODEX_VERSION}"

# The official node image already owns UID/GID 1000 as user "node"; replace it with "agent".
RUN if getent passwd node >/dev/null; then userdel -r node; fi \
    && if getent group node >/dev/null; then groupdel node; fi \
    && groupadd --gid "${USER_GID}" agent \
    && useradd --uid "${USER_UID}" --gid "${USER_GID}" --create-home --shell /bin/bash agent

# agy updates itself in the background by default; a sandbox must keep the pinned version.
ENV AGY_CLI_DISABLE_AUTO_UPDATE=1

RUN set -eu; \
    case "${TARGETARCH:-amd64}" in \
        amd64) url="${AGY_URL_AMD64}"; sum="${AGY_SHA512_AMD64}" ;; \
        arm64) url="${AGY_URL_ARM64}"; sum="${AGY_SHA512_ARM64}" ;; \
        *) echo "agy: unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    tmp="$(mktemp -d)"; \
    curl -fsSL -o "$tmp/agy.tgz" "$url"; \
    echo "$sum  $tmp/agy.tgz" | sha512sum -c -; \
    tar -xzf "$tmp/agy.tgz" -C "$tmp" antigravity; \
    install -m 0755 "$tmp/antigravity" /usr/local/bin/agy; \
    test "$(HOME="$tmp" agy --version)" = "${AGY_VERSION}"; \
    rm -rf "$tmp"

WORKDIR /workspace
USER agent
ENV HOME=/home/agent

# tini reaps zombie processes and forwards signals; long agent runs spawn many children.
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["bash"]
