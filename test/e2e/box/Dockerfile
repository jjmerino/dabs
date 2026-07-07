# The box the e2e suite runs in: Go (compiles + runs the suite) and bubblewrap
# built from source (0.11.0, non-setuid, overlay-enabled) so dabs' bwrap driver
# can create overlay boxes. No docker inside — the suite's base image is staged
# in prebuilt (DABS_E2E_PREBUILT). Run via the INTERNAL privileged docker driver.
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
COPY .e2e-stage /opt/dabs-e2e-image
