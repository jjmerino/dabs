# The box the e2e suite runs in: a dabseption box (bubblewrap, HOME off overlayfs,
# a dabs that can boot its own boxes) plus the suite's own fixtures.
#
# `dabs-dabseption` is the image `dabs build dabseption` produces — the docker
# driver tags images `dabs-<recipe>` — so it must be built first (run_e2e.sh does).
FROM dabs-dabseption

# The image the suite's inner boxes boot. The env/workdir recorded for it at the
# bottom of this file describe THIS stage, so both move together.
FROM alpine:3.20 AS e2ebase
WORKDIR /work
RUN apk add --no-cache git curl ca-certificates   # mirrors images/shell: git for worktrees, curl+certs so a fresh box can fetch
RUN echo hello-from-image > /work/marker.txt

# A FAKE `claude`: a real CLI on PATH that only "logs in" — it writes the
# credential Claude Code would (valid JSON, with a non-empty
# claudeAiOauth.accessToken) and exits 0. A recipe that mkmounts a shared login
# dir can then be driven end to end with nothing built in-box. The heredoc keeps
# the JSON quotes intact.
RUN cat > /usr/local/bin/claude <<'SH' \
 && chmod +x /usr/local/bin/claude
#!/bin/sh
mkdir -p /root/.claude
cat > /root/.claude/.credentials.json <<'JSON'
{"claudeAiOauth":{"accessToken":"fake-token"}}
JSON
echo "fake-claude: login ok"
SH

FROM dabs-dabseption

# The proxy engine runs under bun on the box the suite runs in (openssl mints its
# CA); the proxy e2e boots an inner box whose only egress is that engine.
# BUN_INSTALL puts bun straight on PATH (arch-correct, no symlink); the final
# `bun --version` fails the build if the install silently no-op'd.
RUN apt-get update && apt-get install -y --no-install-recommends curl unzip openssl \
 && rm -rf /var/lib/apt/lists/* \
 && BUN_INSTALL=/usr/local curl -fsSL https://bun.sh/install | BUN_INSTALL=/usr/local bash \
 && bun --version

# dabs is baked in, not built at start: this box mounts no source. The forwarder
# is built first (a static linux binary) and dabs is built with `-tags
# withforwarder` so it embeds that binary — under proxy egress dabs materializes
# it and mounts the single-purpose forwarder (never the dabs CLI) into inner boxes.
WORKDIR /work
COPY . /work
RUN CGO_ENABLED=0 go build -o egressforwarder/forwarder/forward.bin ./egressforwarder/cmd/forward \
 && CGO_ENABLED=0 go build -tags withforwarder -o /usr/local/bin/dabs .

# The e2ebase stage as a dabs bwrap image, where dabs looks for one: a flattened
# rootfs plus the env/workdir recorded beside it. `COPY --from` is the export —
# the builder flattens a stage into a plain rootfs, which is what a bwrap image
# is — so the box needs no docker, and the suite installs nothing at startup.
# HOME (/tmp/h, off overlayfs) comes from the base.
COPY --from=e2ebase / /tmp/h/.dabs/images/dabs-e2e/rootfs
RUN printf '%s' '{"env":["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],"workdir":"/work"}' \
      > /tmp/h/.dabs/images/dabs-e2e/image.json

# Pristine copies of the staged images, OUTSIDE the store `dabs prune` reaps.
# There is no builder in the box, so a pruned staged image is otherwise gone for
# the rest of the suite; tests that need one (the bundled box recipes boot
# `shell`) restore it from here.
RUN cp -a /tmp/h/.dabs/images /tmp/h/.dabs-staged-images
