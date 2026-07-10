package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The claude image bans an agent's tools from reading the mounted vault
// (/root/.claude) so the OAuth credential cannot be exfiltrated. The ban is
// declared in the image: Claude Code managed settings (deny rules) plus a
// PreToolUse hook (the guard script). These tests assert the shipped artifacts
// — read from imagesFS, i.e. what is actually embedded in the binary — are
// wired correctly, and that the guard script denies exactly the vault and
// nothing else.

func TestClaudeManagedSettingsDenyVault(t *testing.T) {
	data, err := imagesFS.ReadFile("images/claude/managed-settings.json")
	if err != nil {
		t.Fatalf("managed-settings.json not embedded: %v", err)
	}
	var doc struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("managed-settings.json is not valid JSON: %v", err)
	}

	// A deny rule must cover the Read tool over the whole vault subtree.
	wantDeny := "Read(/root/.claude/**)"
	found := false
	for _, d := range doc.Permissions.Deny {
		if d == wantDeny {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing deny rule %q; have %v", wantDeny, doc.Permissions.Deny)
	}

	// A PreToolUse Bash hook must invoke the guard script (the Bash backstop the
	// Read deny cannot cover — e.g. node/python subprocesses).
	var hookCmd string
	for _, p := range doc.Hooks.PreToolUse {
		if p.Matcher != "Bash" {
			continue
		}
		for _, h := range p.Hooks {
			if h.Type == "command" {
				hookCmd = h.Command
			}
		}
	}
	if !strings.Contains(hookCmd, "dabs-guard-claude-vault") {
		t.Fatalf("PreToolUse Bash hook does not call the guard script; got %q", hookCmd)
	}
}

// TestGuardScriptDeniesVaultOnly runs the shipped guard script against realistic
// PreToolUse payloads: vault reads (in several command forms) must be denied
// (exit 2), and unrelated commands must pass (exit 0). This is the exact
// mechanism proven end-to-end in a real box.
func TestGuardScriptDeniesVaultOnly(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh available")
	}
	script, err := imagesFS.ReadFile("images/claude/guard-claude-vault.sh")
	if err != nil {
		t.Fatalf("guard script not embedded: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "guard.sh")
	if err := os.WriteFile(path, script, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		command  string
		wantExit int
	}{
		{"cat cred", "cat /root/.claude/.credentials.json", 2},
		{"grep vault", "grep -r token /root/.claude", 2},
		{"node subprocess", `node -e "require('fs').readFileSync('/root/.claude/.credentials.json')"`, 2},
		{"cred filename alone", "cp .credentials.json /work/x", 2},
		{"config-dir env ref", "cat $CLAUDE_CONFIG_DIR/.credentials.json", 2},
		{"work read allowed", "cat /work/main.go | head -1", 0},
		{"ls work allowed", "ls -la /work", 0},
		{"git status allowed", "git -C /work status", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]any{
				"tool_name":  "Bash",
				"tool_input": map[string]string{"command": c.command},
			})
			cmd := exec.Command("sh", path)
			cmd.Stdin = strings.NewReader(string(payload))
			out, _ := cmd.CombinedOutput()
			got := cmd.ProcessState.ExitCode()
			if got != c.wantExit {
				t.Fatalf("command %q: want exit %d, got %d (output: %s)", c.command, c.wantExit, got, out)
			}
		})
	}
}
