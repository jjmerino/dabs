---
name: review
description: Senior Go code review of the current branch versus main for the dabs codebase — run inside a disposable box by `dabs recipe review`. Diffs, reads, builds, tests, and reports ranked findings.
---

# Review the dabs branch

You are in a disposable **copy** of the repo, so exercise the code freely.

1. Run `git diff main...HEAD` and read the diff and every changed file.
2. Read `AGENTS.md`, section **Working on the codebase**, and check the diff
   respects the architecture:
   - `cli` stays thin,
   - `core/actions` holds policy,
   - `core/sandbox` holds mechanical drivers,
   - the `data.Data` and `sandbox.Driver` seams are kept clean,
   - zero third-party dependencies.
3. Build both platforms and run the tests to verify:
   - `go build ./...`
   - `GOOS=linux go build ./...`
   - `go test ./cli/ ./core/...`

Report **ranked findings** (blocker / major / minor / nit) with `file:line`,
covering Go style, architecture and seams, and test quality. End with an
overall verdict.
