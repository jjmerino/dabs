---
name: review-dabs
description: Senior Go code review of the dabs branch versus main. Diffs, reads, builds, tests, and reports ranked findings. Used by the review and review-wt recipes.
---

# Review the dabs branch

You are in a throwaway box with the branch checked out — build and test freely,
but do not modify the source.

1. Run `git diff main...HEAD` and read the diff and every changed file.
2. Read `AGENTS.md`, section **Working on the codebase**, and check the diff
   respects the architecture:
   - `cli` stays thin,
   - `core/actions` holds policy,
   - `core/sandbox` holds mechanical drivers,
   - the `data.Data` and `sandbox.Driver` seams are kept clean.
3. Build both platforms and run the tests to verify:
   - `go build ./...`
   - `GOOS=linux go build ./...`
   - `go test ./cli/ ./core/...`
4. Flag any reference to a time-bound project or one-time change **by name** in
   permanent code — comments, identifiers, test names, doc prose. Future
   engineers will not know what it was. If a tui reskin happens, a comment about
   "the reskin" (as if it were the one and only) is a finding: permanent code
   must describe what the thing IS, not the change that introduced it.

Report a **single ranked list of findings** (most severe first) with
`file:line`, covering Go style, architecture and seams, and test quality.
Rank orders priority — it never grants permission to skip. Do **not** label any
finding "optional", "nit", "polish", "minor follow-up", or "nice-to-have": there
are no skippable findings. Every finding you surface must be resolved in exactly
one of two ways — (a) it is FIXED, or (b) a HUMAN explicitly approves leaving it
unfixed. You never unilaterally decide something can be skipped. End with an
overall verdict that presents every finding as needing a fix or an explicit
human waiver.
