#!/bin/sh
# dabs-guard-claude-vault: a Claude Code PreToolUse hook (matcher: Bash).
#
# Deny any Bash command that references the mounted Claude vault
# (/root/.claude), so the agent's shell cannot read .credentials.json and
# exfiltrate the OAuth token. Claude Code authenticates by reading the
# credential INTERNALLY at startup — that path never invokes the Bash tool, so
# it does not pass through this hook and is unaffected.
#
# The hook receives the WHOLE tool call as JSON on stdin. We must inspect ONLY
# tool_input.command — NOT the raw payload, which also carries transcript_path
# and cwd (both under /root/.claude, the config dir) and would otherwise match
# on EVERY command and block all Bash. Extract the command with node (always
# present in this image); if it can't be parsed, fail CLOSED (deny) — a security
# control must never fail open. Exit 2 blocks the call and feeds stderr back to
# the model. Erring broad on the command is intentional: the agent has no
# legitimate reason to touch /root/.claude from a shell.
input=$(cat)

cmd=$(printf '%s' "$input" | node -e '
  let s = "";
  process.stdin.on("data", d => s += d).on("end", () => {
    try {
      const o = JSON.parse(s);
      const c = o && o.tool_input && o.tool_input.command;
      process.stdout.write(typeof c === "string" ? c : "");
    } catch (e) { process.exit(3); }
  });
') || {
  echo "denied by dabs sandbox policy: could not parse the Bash command to vet it against the vault policy" >&2
  exit 2
}

case "$cmd" in
  *"/root/.claude"*|*".credentials.json"*|*'$CLAUDE_CONFIG_DIR'*|*'${CLAUDE_CONFIG_DIR}'*)
    echo "denied by dabs sandbox policy: reading the Claude vault (/root/.claude) is not permitted; the OAuth credential must not leave the box" >&2
    exit 2
    ;;
esac
exit 0
