# The box behind `dabs auth claude` (login) and `dabs claude` (a fresh dev box
# on a git worktree): Claude Code plus the tools it drives — git and ripgrep —
# on a slim node base. The credential the login writes to /root/.claude is
# captured by a live host mount (the vault), so it survives the box and
# refreshes in place; `dabs claude` mounts that same vault for config + logs.
FROM node:22-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git ripgrep \
 && rm -rf /var/lib/apt/lists/*

RUN npm install -g @anthropic-ai/claude-code

WORKDIR /work
