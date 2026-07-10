# Box for `dabs recipe dumb-user`: a fresh machine with Claude Code, Go, and git.
# The recipe copies the dabs source into /work and builds `dabs` from it there,
# so the naive agent exercises the CURRENT tree.
FROM node:22-slim
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL "https://go.dev/dl/go1.23.4.linux-${TARGETARCH}.tar.gz" | tar -C /usr/local -xz
ENV PATH=/usr/local/go/bin:/usr/local/bin:$PATH
RUN npm install -g @anthropic-ai/claude-code
WORKDIR /work
