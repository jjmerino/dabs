# The box the e2e suite runs in: Go (compiles + runs the suite) and bubblewrap
# built from source (0.11.0, non-setuid, overlay-enabled) so dabs' bwrap driver
# can create overlay boxes. No docker inside — the suite's base image is staged
# in by this Dockerfile, below. Run via the INTERNAL privileged docker driver.
FROM golang:1.23-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential meson ninja-build pkg-config libcap-dev ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://github.com/containers/bubblewrap/releases/download/v0.11.0/bubblewrap-0.11.0.tar.xz -o /tmp/bw.tar.xz \
    && cd /tmp && tar xf bw.tar.xz && cd bubblewrap-0.11.0 \
    && meson setup _build --prefix=/usr/local -Dtests=false -Dman=disabled \
    && ninja -C _build && ninja -C _build install && ldconfig

# NOT /root: /root is docker's overlayfs, and bwrap cannot stack an overlay on
# one — the inner `dabs up` would die with "Can't make overlay mount … Invalid
# argument". The privileged target runs the box with a non-overlay volume at
# /tmp, so dabs's state lives there. Docker seeds that volume from the image's
# own /tmp, which is what carries the staged image below into the box.
ENV HOME=/tmp/h
WORKDIR /work
COPY . /work
RUN go build -o /usr/local/bin/dabs .

# The inner base image, staged as a dabs bwrap image right where dabs looks for
# it — a flattened rootfs plus the env/workdir bwrap records beside it.
# `COPY --from=<image>` IS the export: the builder flattens dabs-e2e-base
# (run_e2e.sh builds it from test/e2e, the single definition of that image) into
# a plain rootfs, which is exactly what a bwrap image is. Nothing has to run
# docker inside the box, and the suite has nothing to install at startup — the
# box IS the isolation, so its own ~/.dabs is the suite's.
COPY --from=dabs-e2e-base / /tmp/h/.dabs/images/dabs-e2e/rootfs
RUN printf '%s' '{"env":["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],"workdir":"/work"}' \
      > /tmp/h/.dabs/images/dabs-e2e/image.json
