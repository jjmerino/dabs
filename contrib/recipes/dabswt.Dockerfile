# Box for `dabs cast dabswt <worktree>`: Go, git, and bubblewrap (built from
# source, non-setuid, overlay-enabled) so a dabs BUILT from the worktree can
# actually run — dabs boots its driver fleet at startup and needs bwrap present,
# and the box runs under the privileged nested-sandbox target so that dabs can
# boot its own boxes inside. Mirrors test/e2e/box's bwrap build.
#
# It bakes NO dabs source: the recipe mounts a worktree at /work and builds
# `dabs` from THAT at start, so one image serves every branch.
FROM golang:1.23-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential meson ninja-build pkg-config libcap-dev ca-certificates curl git \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://github.com/containers/bubblewrap/releases/download/v0.11.0/bubblewrap-0.11.0.tar.xz -o /tmp/bw.tar.xz \
    && cd /tmp && tar xf bw.tar.xz && cd bubblewrap-0.11.0 \
    && meson setup _build --prefix=/usr/local -Dtests=false -Dman=disabled \
    && ninja -C _build && ninja -C _build install && ldconfig

ENV HOME=/root
WORKDIR /work
