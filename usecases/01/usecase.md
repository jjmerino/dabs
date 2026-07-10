# Recipe 01 — 30 dumb users against my product

**Use case:** I built a CLI (say `notekeeper`) and I want to know where real,
impatient, not-very-careful users get stuck. I want to unleash 30 cheap agents
(Haiku) on a *fresh machine* each, tell each one "you're a new user, achieve X",
and read back where they floundered. No agent should touch my laptop, and every
run must be identical and reproducible — this is a fleet, not a chat.

Why this is the `dabs.json` route, copy strategy: I'm not iterating on code with
an agent, so **no mount** (I don't want 30 agents writing to my host) and **no
worktree** (there's no branch to work on). Each box gets a disposable *copy* of a
fixed test rig. The rig is a reusable artifact, so it's a `dabs.json`, not a pile
of shell flags.

**Ideal flow:**

```bash
brew install dabs

# One-time: log the driving model in. The credential lands in a host vault that
# every box mounts read-only, so I never paste a key into 30 boxes.
dabs auth claude

# The test rig: a fresh machine with my product installed and a "probe" harness
# that runs ONE Haiku through ONE journey and writes a verdict to /out/report.json.
mkdir probe && cd probe
cat > dabs.json <<'JSON'
{
  "name": "notekeeper-probe",
  "harness": "claude",
  "model": "claude-haiku-4-5",
  "task": "You are a brand-new user. Install and use notekeeper to create a note,
           tag it, and find it again. Narrate every confusion. When done (or stuck
           for good), write your verdict to /out/report.json.",
  "out": "/out",
  "env": { "NOTEKEEPER_VERSION": "1.4.0" }
}
JSON
# Dockerfile: the fresh machine. `.` is COPIED in at build — the box owns it.
cat > Dockerfile <<'DOCKER'
FROM debian:stable-slim
RUN apt-get update && apt-get install -y notekeeper=1.4.0
DOCKER

# Fan out: 30 identical fresh boxes, each running the probe once, in parallel.
# --collect drains each box's /out into ./results/<instance>/ as it finishes.
dabs up . --replicas 30 --collect ./results

# Watch the swarm; each line is one box reaching a milestone or dying.
dabs tail --all

# When the fleet drains, I have 30 verdicts side by side.
jq -s 'group_by(.stuck_on) | map({stuck_on: .[0].stuck_on, count: length})' \
   results/*/report.json
#  → [ {"stuck_on":"tagging syntax","count":19}, {"stuck_on":"none","count":8}, … ]

dabs down --all
```

**What this pins down about the CLI:**

- `dabs up --replicas N` is the fleet primitive: N pristine boxes from one
  manifest, run to completion, no names to juggle.
- `--collect <dir>` is how a headless fleet returns data — every box has an
  `out` dir; dabs drains it. No mount needed for *output* because collection
  happens at teardown.
- `harness`/`model`/`task` in `dabs.json` mean "run this agent unattended" — the
  declarative counterpart to typing `dabs claude "<prompt>"`.
- Copy (the default) is what makes 30 boxes *safe and identical*: none of them
  can see or corrupt my host or each other.
