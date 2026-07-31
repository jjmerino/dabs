# Box for testing dabs WITH dabs — behind the `dabseption` and `dabseptionwt`
# recipes. Go, git, bubblewrap (built from source, non-setuid, overlay-enabled)
# and pasta (the passt project, also from source) so a dabs BUILT from /work can
# actually run: dabs boots its driver fleet at startup and needs bwrap present,
# and a nested box with open egress gets its own network namespace from pasta.
# The box runs under the privileged nested-sandbox target, so the dabs inside
# can boot its OWN boxes.
#
# It bakes NO dabs source: the recipe supplies /work (the cwd for `dabseption`,
# a fresh worktree for `dabseptionwt`) and builds `dabs` from THAT at start, so
# one image serves every branch.

# The image the dabs inside boots, matching images/shell (what the bundled `sh`
# recipe uses — git for worktrees, curl+certs so a fresh box can fetch).
# `COPY --from` below flattens it into a rootfs, so the box needs no docker of
# its own.
FROM alpine:3.20 AS shellimg
RUN apk add --no-cache git curl ca-certificates
WORKDIR /work

FROM golang:1.23-bookworm

# tmux is here so the box's dabs can be driven as a TERMINAL, not just as a
# process.
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential meson ninja-build pkg-config libcap-dev ca-certificates curl git \
      tmux ncurses-term \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://github.com/containers/bubblewrap/releases/download/v0.11.0/bubblewrap-0.11.0.tar.xz -o /tmp/bw.tar.xz \
    && cd /tmp && tar xf bw.tar.xz && cd bubblewrap-0.11.0 \
    && meson setup _build --prefix=/usr/local -Dtests=false -Dman=disabled \
    && ninja -C _build && ninja -C _build install && ldconfig

# pasta, from the passt project: what a nested box with open egress gets its
# network namespace and its outbound connectivity from. Built from source for
# the same reason bubblewrap is — the distro package is older than the flags
# dabs passes (--dns-forward, --no-map-gw, per-protocol port forwarding off).
ARG PASST_VERSION=2025_02_17.a1e48a0
RUN curl -fsSL "https://passt.top/passt/snapshot/passt-${PASST_VERSION}.tar.gz" -o /tmp/passt.tar.gz \
    && cd /tmp && tar xf passt.tar.gz && cd "passt-${PASST_VERSION}" \
    && make && make install prefix=/usr/local \
    && pasta --version | head -1

# NOT /root: /root is docker's overlayfs, and bwrap cannot stack an overlay on
# one — the inner `dabs recipe --no-command` dies with "Can't make overlay mount … Invalid
# argument". The privileged target runs the box with a non-overlay volume at
# /tmp, so dabs's state lives there. Docker seeds that volume from the image's
# own /tmp, which is what carries the staged image below into the box.
ENV HOME=/tmp/h

# The `shell` image as dabs stores one: a flattened rootfs plus the env/workdir
# recorded alongside. With it present, `dabs recipe sh --no-command` and `dabs recipe sh`
# work in the box with no builder. `dabs build` cannot run here — it needs docker.
COPY --from=shellimg / /tmp/h/.dabs/images/shell/rootfs
RUN printf '%s' '{"env":["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],"workdir":"/work"}' \
      > /tmp/h/.dabs/images/shell/image.json

# An unprivileged user, because pasta serves only one: it drops its capabilities
# before mapping the namespace it builds, so a root caller gets a namespace with
# no mapping. A nested box with open egress is therefore driven from here as
# `boxer`, with its own dabs state and its own copy of the staged image under a
# non-overlay /tmp:
#
#   su boxer -c 'HOME=/tmp/boxer dabs recipe sh --no-command'
#
# The image's own user stays root: the build at /work, and the images that layer
# on this one, are root's.
RUN useradd -m -u 1000 boxer \
    && mkdir -p /tmp/boxer/.dabs/images \
    && cp -a /tmp/h/.dabs/images/shell /tmp/boxer/.dabs/images/shell \
    && chown -R boxer:boxer /tmp/boxer

WORKDIR /work
