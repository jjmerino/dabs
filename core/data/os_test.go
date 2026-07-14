package data

import (
	"os"
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

// GitLanded answers the content question with real git: a branch whose work
// reached the base via a SQUASH merge (commits ahead for ever, bytes landed)
// is landed; a branch with work the base lacks is not — even after the base
// moves on with unrelated commits. `git diff base...HEAD` cannot answer this
// (the squash commit is no ancestor of the branch, so the merge-base never
// moves); merge-tree against the base's tree does.
func TestGitLandedSeesThroughSquashMerge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	write := func(dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(repo, "init", "-q", "-b", "main")
	write(repo, "base.txt", "v1\n")
	git(repo, "add", "-A")
	git(repo, "commit", "-qm", "c0")

	wt := filepath.Join(t.TempDir(), "wt")
	git(repo, "worktree", "add", "-q", "-b", "feat", wt, "HEAD")
	write(wt, "feature.txt", "done\n")
	git(wt, "add", "-A")
	git(wt, "commit", "-qm", "feature")

	// Unmerged work: not landed.
	if landed, err := (OS{}).GitLanded(wt); err != nil || landed {
		t.Fatalf("unmerged branch read landed=%v err=%v, want false", landed, err)
	}

	// The human lands it as a SQUASH: content in main, commits not.
	git(repo, "merge", "--squash", "feat")
	git(repo, "commit", "-qm", "feature (squashed)")
	if landed, err := (OS{}).GitLanded(wt); err != nil || !landed {
		t.Fatalf("squash-merged branch read landed=%v err=%v, want true", landed, err)
	}

	// The base moves on with UNRELATED work: the branch is still landed.
	write(repo, "other.txt", "later\n")
	git(repo, "add", "-A")
	git(repo, "commit", "-qm", "unrelated")
	if landed, err := (OS{}).GitLanded(wt); err != nil || !landed {
		t.Fatalf("landed branch behind a moved base read landed=%v err=%v, want true", landed, err)
	}

	// New work on the branch AFTER landing: not landed again.
	write(wt, "more.txt", "wip\n")
	git(wt, "add", "-A")
	git(wt, "commit", "-qm", "more")
	if landed, err := (OS{}).GitLanded(wt); err != nil || landed {
		t.Fatalf("branch with fresh commits read landed=%v err=%v, want false", landed, err)
	}
}
