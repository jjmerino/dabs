# The box the e2e suite runs in — a dabseption box plus what the suite needs.
#
# It does NOT reimplement dabseption (bubblewrap from source, HOME off overlayfs,
# a staged inner image): it DERIVES from it. `dabs-dabseption` is the image
# `dabs build dabseption` produces (the docker driver tags images `dabs-<recipe>`),
# so run_e2e.sh builds that first. One definition of "a box that can run dabs and
# boot its own boxes" — this file only adds what is specific to the suite.
FROM dabs-dabseption

# The image the suite's inner boxes boot, authored HERE so the rootfs and the
# env/workdir recorded for it below cannot drift apart, and so this Dockerfile
# needs nothing staged on the host. dabseption's own staged `shell` image comes
# along from the base and is harmless.
FROM alpine:3.20 AS e2ebase
WORKDIR /work
RUN apk add --no-cache git   # `dabs cast` proves git works IN the box; the suite needs it on PATH
RUN echo hello-from-image > /work/marker.txt

# A FAKE `claude`: a real CLI on PATH that only "logs in" — it writes the
# credential Claude Code would (valid JSON, with a non-empty
# claudeAiOauth.accessToken) and exits 0. It lets `dabs auth claude` (via
# DABS_AUTH_IMAGE=dabs-e2e) be exercised without building anything in-box. A
# heredoc keeps the JSON quotes intact — `dabs auth` parses the token, so
# mangled JSON fails.
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

# The suite runs the dabs built from THIS source, baked in (dabseption's own
# recipe builds dabs at start from a mounted /work; the e2e box has no mount, so
# it builds at image time).
WORKDIR /work
COPY . /work
RUN go build -o /usr/local/bin/dabs .

# Stage the e2ebase stage as a dabs bwrap image, right where dabs looks for it:
# a flattened rootfs plus the env/workdir bwrap records beside it. `COPY --from`
# IS the export — the builder flattens the stage into a plain rootfs, which is
# exactly what a bwrap image is. So nothing runs docker inside the box, and the
# suite installs nothing at startup: the box IS the isolation, so its own ~/.dabs
# is the suite's. HOME (/tmp/h, off overlayfs) is inherited from dabseption.
#
# The env/workdir below describe the e2ebase stage above — same file, so a change
# there is a change here.
COPY --from=e2ebase / /tmp/h/.dabs/images/dabs-e2e/rootfs
RUN printf '%s' '{"env":["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],"workdir":"/work"}' \
      > /tmp/h/.dabs/images/dabs-e2e/image.json
