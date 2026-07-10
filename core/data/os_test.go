package data

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// GitState's `ahead` count must survive a detached-HEAD main repo — the config
// where the old baseBranch() grabbed a linked worktree's branch and undercounted
// to 0, so a worktree with committed work read "clean" and could be reaped
// without --force. Uses real git against temp dirs.
func TestGitStateCountsAheadWithDetachedMain(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "commit", "--allow-empty", "-qm", "c0")

	wt := filepath.Join(t.TempDir(), "wt")
	git(repo, "worktree", "add", "-q", "-b", "feat", wt, "HEAD")
	git(wt, "commit", "--allow-empty", "-qm", "c1") // 1 commit ahead, working tree clean

	// Detach the MAIN repo's HEAD — the case that broke baseBranch.
	git(repo, "checkout", "-q", "--detach", "HEAD")

	branch, dirty, ahead, err := OS{}.GitState(wt)
	if err != nil {
		t.Fatalf("GitState: %v", err)
	}
	if branch != "feat" {
		t.Errorf("branch = %q, want feat", branch)
	}
	if dirty {
		t.Errorf("working tree is clean, reported dirty")
	}
	if ahead != 1 {
		t.Fatalf("ahead = %d, want 1 — a committed-but-unmerged worktree would be silently reaped", ahead)
	}
}
