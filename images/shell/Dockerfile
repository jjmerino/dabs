# The box behind `dabs recipe shell`: a minimal, generic dev shell — the
# out-of-the-box example recipe. Just a small base with git; /work is supplied
# by the recipe's mount, not baked in.
FROM alpine:3.20
RUN apk add --no-cache git
WORKDIR /work
