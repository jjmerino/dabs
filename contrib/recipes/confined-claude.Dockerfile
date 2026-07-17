# The confined-claude image: the bundled `claude` box PLUS the linux forwarder
# that proxy egress needs. On the apple driver dabs cannot mount a host binary
# into the micro-VM, so the image must carry the forwarder at /run/dabs/forward.
#
# We build the forwarder from THIS checkout — not `go install …@latest` — because
# the forwarder command postdates the latest release tag, so `@latest` would
# resolve to a version that has no cmd/forward and fail. Building from source
# also guarantees the in-box forwarder matches this branch exactly.
#
# The base is the already-built bundled claude image (tag dabs-claude): run the
# `claude` recipe once so it exists, then `dabs build <this dabs.yaml>`.
FROM golang:1.23-alpine AS fwd
WORKDIR /src
COPY . /src
RUN CGO_ENABLED=0 go build -o /forward ./egressforwarder/cmd/forward

FROM dabs-claude
COPY --from=fwd /forward /run/dabs/forward
