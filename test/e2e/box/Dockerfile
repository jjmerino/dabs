# The box the e2e suite runs in: Go (compiles + runs the suite) and bubblewrap
# built from source (0.11.0, non-setuid, overlay-enabled) so dabs' bwrap driver
# can create overlay boxes. No docker inside — the suite's base image is staged
# in by this Dockerfile (DABS_E2E_PREBUILT). Run via the INTERNAL privileged
# docker driver.
FROM golang:1.23-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential meson ninja-build pkg-config libcap-dev ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://github.com/containers/bubblewrap/releases/download/v0.11.0/bubblewrap-0.11.0.tar.xz -o /tmp/bw.tar.xz \
    && cd /tmp && tar xf bw.tar.xz && cd bubblewrap-0.11.0 \
    && meson setup _build --prefix=/usr/local -Dtests=false -Dman=disabled \
    && ninja -C _build && ninja -C _build install && ldconfig

ENV HOME=/root DABS_E2E_PREBUILT=/opt/dabs-e2e-image
WORKDIR /work
COPY . /work
RUN go build -o /usr/local/bin/dabs .

# The suite's inner base image, as a dabs bwrap image: a flattened rootfs plus the
# env/workdir bwrap records beside it. `COPY --from=<image>` IS the export — the
# builder flattens dabs-e2e-base (run_e2e.sh builds it from test/e2e, the single
# definition of that image) into a plain rootfs, which is exactly what a bwrap
# image is. Nothing has to run docker inside the box.
#
# The suite does NOT boot from here: TestMain copies this into the fresh isolated
# $HOME it makes per run, which is what keeps concurrent runs from colliding.
COPY --from=dabs-e2e-base / /opt/dabs-e2e-image/rootfs
RUN printf '%s' '{"env":["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],"workdir":"/work"}' \
      > /opt/dabs-e2e-image/image.json
