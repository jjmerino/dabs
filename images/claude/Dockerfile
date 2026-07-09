# The box `dabs auth claude` logs into. Minimal: just Claude Code on a slim
# node base. The credential the login writes to /root/.claude is captured by a
# live host mount (the vault), so it survives the box and refreshes in place.
FROM node:22-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*

RUN npm install -g @anthropic-ai/claude-code

WORKDIR /work
