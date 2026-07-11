# The base image the e2e inner boxes come from. Built by the suite via
# `dabs build test/e2e` (dabs picks the platform's driver). Named "dabs-e2e"
# (see dabs.json) so the boxes are obvious in `dabs ls`.
FROM alpine:3.20
WORKDIR /work
RUN apk add --no-cache git   # `dabs cast` proves git works IN the box; the suite needs it on PATH
RUN echo hello-from-image > /work/marker.txt

# A FAKE `claude`: a real CLI on PATH that only "logs in" — it writes the
# credential Claude Code would (valid JSON, with a non-empty
# claudeAiOauth.accessToken) and exits 0. Baked into the prebuilt base image so
# `dabs auth claude` (via DABS_AUTH_IMAGE=dabs-e2e) can be exercised in the
# no-docker bwrap runner without building anything in-box. A heredoc keeps the
# JSON quotes intact — `dabs auth` now parses the token, so mangled JSON fails.
RUN cat > /usr/local/bin/claude <<'SH' \
 && chmod +x /usr/local/bin/claude
#!/bin/sh
mkdir -p /root/.claude
cat > /root/.claude/.credentials.json <<'JSON'
{"claudeAiOauth":{"accessToken":"fake-token"}}
JSON
echo "fake-claude: login ok"
SH
