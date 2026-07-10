#!/bin/sh
# dabs-guard-claude-vault: a Claude Code PreToolUse hook (matcher: Bash).
#
# Deny any Bash command that references the mounted Claude vault
# (/root/.claude), so the agent's shell cannot read .credentials.json and
# exfiltrate the OAuth token. Claude Code authenticates by reading the
# credential INTERNALLY at startup — that path never invokes the Bash tool, so
# it does not pass through this hook and is unaffected.
#
# The hook receives the tool call as JSON on stdin (including
# tool_input.command). We deny if the vault path (or a credential filename)
# appears anywhere in it. Exit code 2 blocks the tool call and feeds stderr
# back to the model. Erring broad is intentional: the agent has no legitimate
# reason to touch /root/.claude from a shell.
input=$(cat)
case "$input" in
  *"/root/.claude"*|*".credentials.json"*|*'$CLAUDE_CONFIG_DIR'*|*'${CLAUDE_CONFIG_DIR}'*)
    echo "denied by dabs sandbox policy: reading the Claude vault (/root/.claude) is not permitted; the OAuth credential must not leave the box" >&2
    exit 2
    ;;
esac
exit 0
