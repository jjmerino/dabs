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
RUN apk add --no-cache git   # `dabs cast` proves git works IN the box; the suite needs it on PATH
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

# dabs is baked in, not built at start: this box mounts no source.
WORKDIR /work
COPY . /work
RUN go build -o /usr/local/bin/dabs .

# The e2ebase stage as a dabs bwrap image, where dabs looks for one: a flattened
# rootfs plus the env/workdir recorded beside it. `COPY --from` is the export —
# the builder flattens a stage into a plain rootfs, which is what a bwrap image
# is — so the box needs no docker, and the suite installs nothing at startup.
# HOME (/tmp/h, off overlayfs) comes from the base.
COPY --from=e2ebase / /tmp/h/.dabs/images/dabs-e2e/rootfs
RUN printf '%s' '{"env":["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],"workdir":"/work"}' \
      > /tmp/h/.dabs/images/dabs-e2e/image.json
