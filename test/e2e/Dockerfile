# The base image the e2e inner boxes come from. Built by the suite via
# `dabs build test/e2e` (dabs picks the platform's driver). Named "dabs-e2e"
# (see dabs.json) so the boxes are obvious in `dabs ls`.
FROM alpine:3.20
WORKDIR /work
RUN echo hello-from-image > /work/marker.txt
