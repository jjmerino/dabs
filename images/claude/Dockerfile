# The box behind `dabs auth claude` (login) and `dabs claude` (a fresh dev box
# on a git worktree): Claude Code plus the tools it drives — git and ripgrep —
# on a slim node base. dabs is a Go project, so the box also ships the Go
# toolchain, letting an agent build, vet, and test dabs in-box. The credential
# the login writes to /root/.claude is captured by a live host mount (the
# vault), so it survives the box and refreshes in place; `dabs claude` mounts
# that same vault for config + logs.
FROM node:22-slim
ARG TARGETARCH

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git ripgrep curl \
 && rm -rf /var/lib/apt/lists/*

# Go toolchain — dabs is a Go project, so its dev box builds and tests dabs.
RUN curl -fsSL "https://go.dev/dl/go1.23.4.linux-${TARGETARCH}.tar.gz" | tar -C /usr/local -xz
ENV PATH=/usr/local/go/bin:$PATH

RUN npm install -g @anthropic-ai/claude-code

# --- Ban the agent's tools from READING the mounted vault -------------------
# The vault at /root/.claude holds .credentials.json (the OAuth token). Claude
# Code needs it INTERNALLY to authenticate — it reads it directly at startup,
# which does NOT pass through the tool-permission layer. But an agent (or any
# command it runs via its Bash/Read tools) must not be able to read that
# directory and exfiltrate the token.
#
# Enforced via Claude Code's MANAGED settings — /etc/claude-code/managed-
# settings.json is the highest-precedence settings source and CANNOT be
# overridden by user/project/local settings or by the agent editing
# /root/.claude/settings.json. Two layers:
#   1. permissions.deny Read(...) — blocks the Read/Glob/Grep tools on the path.
#   2. a PreToolUse hook — blocks any Bash command that references the vault
#      (cat/less/head/grep/cp/python -c open(...)/xxd/…), which a Bash-only
#      deny rule cannot cover reliably given the variety of read commands.
# Both are declarative and baked into the image; nothing is hidden in Go.
RUN mkdir -p /etc/claude-code
COPY managed-settings.json /etc/claude-code/managed-settings.json
COPY guard-claude-vault.sh /usr/local/bin/dabs-guard-claude-vault
RUN chmod 0755 /usr/local/bin/dabs-guard-claude-vault \
 && chmod 0644 /etc/claude-code/managed-settings.json

WORKDIR /work
