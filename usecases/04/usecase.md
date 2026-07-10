# Recipe 04 — a dashboard that chats with many Claudes

**Use case:** I want a little web UI: a column per agent, each streaming its
output, each with a text box so I can chat with that Claude — and a "spawn"
button to add another. I don't want to build an agent runtime; I want **dabs to
be the backend** and my UI to be a thin client. Every button in the UI should map
to a `dabs` command I could also type.

This is the relay: dabs already knows how to start agents, stream them, and
deliver messages ([02], [03]). A UI needs those same verbs over a socket instead
of a TTY, plus a stable machine format. So the whole recipe is "make the CLI
scriptable, then serve it."

**Ideal flow:**

```bash
brew install dabs
dabs auth claude

# 1. Prove the primitives compose from the shell first (the UI does exactly this).
cd ~/code/myapp
dabs claude -mw --name api    --detach "Own the REST API. Wait for instructions."
dabs claude -mw --name web    --detach "Own the React front end. Wait for instructions."
dabs claude -mw --name schema --detach "Own the DB schema. Wait for instructions."

dabs ls --json
#  [ {"name":"api","status":"idle","branch":"dabs/api",…}, … ]

# Relay a message into one agent and stream every agent at once — these two
# commands ARE the backend the UI needs:
dabs send api "add a DELETE /notes/:id endpoint"
dabs tail --all --json
#  {"agent":"api","ts":…,"text":"Editing routes/notes.ts…"}
#  {"agent":"web","ts":…,"text":"idle"}
#  …

# 2. Now serve the same surface over a local socket for the UI.
dabs serve --port 7070
#  dabs API on http://127.0.0.1:7070
#    GET  /agents                → dabs ls --json
#    POST /agents                → dabs claude … (spawn button)
#    POST /agents/:name/send     → dabs send
#    GET  /agents/:name/stream   → dabs tail (Server-Sent Events)
#    POST /agents/:name/down     → dabs down
```

```html
<!-- The entire UI is a client of that socket. No agent logic of its own. -->
<script>
  const agents = await fetch('http://127.0.0.1:7070/agents').then(r => r.json());
  for (const a of agents) {
    const es = new EventSource(`http://127.0.0.1:7070/agents/${a.name}/stream`);
    es.onmessage = e => appendColumn(a.name, JSON.parse(e.data).text);
  }
  function send(name, msg) {
    fetch(`http://127.0.0.1:7070/agents/${name}/send`,
          {method:'POST', body: JSON.stringify({msg})});
  }
</script>
```

**What this pins down about the CLI:**

- `--json` on every read command and `dabs tail --all` are the contract: a UI is
  just a consumer of structured events, so dabs must emit them.
- `dabs serve` is not a new brain — it's a 1:1 HTTP/SSE shim over `ls`, `claude`,
  `send`, `tail`, `down`. The rule "every UI action equals a CLI command I could
  type" keeps the surface honest and testable from a shell.
- `dabs send` (deliver one message, non-interactively) is the relay atom — the
  same verb whether the caller is a UI, a cron job, or another agent.
- Multiplexing is dabs's job (many named agents, one `tail --all`); rendering is
  the UI's. Clean seam.
