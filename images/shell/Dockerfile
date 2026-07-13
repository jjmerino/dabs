# The box behind `dabs recipe shell`: a minimal, generic dev shell — the
# out-of-the-box example recipe. Just a small base with git; /work is supplied
# by the recipe's mount, not baked in.
FROM alpine:3.20
# git for worktrees; curl + ca-certificates because a fresh machine that
# cannot fetch anything fails the first thing anyone does on a fresh machine
# (found by a user typing `curl` in a box: command not found).
RUN apk add --no-cache git curl ca-certificates
WORKDIR /work
