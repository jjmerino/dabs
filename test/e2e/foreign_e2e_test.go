//go:build e2e

// End-to-end tests for foreign worktrees in `ls`: checkouts cut by git
// directly (not dabs) render as display-only `(unmanaged)` rows under their
// project, and no verb resolves them.
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLsRendersForeignWorktrees cuts a worktree with plain `git worktree add`
// — not dabs — and asserts `ls` shows it under the project as `(unmanaged)`:
// KIND worktree, WHERE its path, STATE the same git judgment dabs's own rows
// get, space cells empty. The row is display-only: rm, cd and exec refuse to
// resolve it.
func TestLsRendersForeignWorktrees(t *testing.T) {
	bundledOnly(t)
	repo := filepath.Join(home, "e2e-foreign")
	gitRepo(t, repo)

	// One dabs-owned worktree (it must NOT repeat as unmanaged) …
	if out, code := runIn(repo, "dabs recipe wt --name owned-wt"); code != 0 {
		t.Fatalf("wt --name owned-wt failed (%d): %s", code, out)
	}
	// … and one cut by git itself, dirtied so its STATE has something to say.
	foreign := filepath.Join(home, "e2e-foreign-co")
	git := exec.Command("git", "worktree", "add", "-b", "foreign-branch", foreign)
	git.Dir = repo
	if b, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, b)
	}
	defer func() {
		rm := exec.Command("git", "worktree", "remove", "--force", foreign)
		rm.Dir = repo
		_ = rm.Run()
	}()
	if err := os.WriteFile(filepath.Join(foreign, "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second git-cut worktree, clean and fully merged (its branch sits at the
	// base's HEAD): finished work, which must NOT earn a row.
	merged := filepath.Join(home, "e2e-foreign-merged")
	gm := exec.Command("git", "worktree", "add", "-b", "merged-branch", merged)
	gm.Dir = repo
	if b, err := gm.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add (merged): %v: %s", err, b)
	}
	defer func() {
		rm := exec.Command("git", "worktree", "remove", "--force", merged)
		rm.Dir = repo
		_ = rm.Run()
	}()

	ls, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, ls)
	}
	if got := strings.Count(ls, "(unmanaged)"); got != 1 {
		t.Fatalf("want exactly one (unmanaged) row (owned-wt must not repeat; the merged one is finished work), got %d:\n%s", got, ls)
	}
	if strings.Contains(ls, "e2e-foreign-merged") {
		t.Fatalf("a clean, merged foreign worktree must not render:\n%s", ls)
	}
	for _, line := range strings.Split(ls, "\n") {
		if !strings.Contains(line, "(unmanaged)") {
			continue
		}
		if !strings.Contains(line, "worktree") {
			t.Errorf("KIND is not worktree:\n%s", line)
		}
		if !strings.Contains(line, "e2e-foreign-co") {
			t.Errorf("WHERE is not the checkout's path:\n%s", line)
		}
		if !strings.Contains(line, "has work") {
			t.Errorf("a dirty foreign worktree must read `has work`:\n%s", line)
		}
	}

	// Display-only: nothing resolves the marker or the foreign path.
	for _, cmd := range []string{
		"dabs cd (unmanaged)",
		"dabs rm (unmanaged) -y",
		"dabs exec (unmanaged) -- true",
		"dabs cd " + foreign,
	} {
		if out, code := run(cmd); code == 0 {
			t.Errorf("%s resolved a foreign worktree; it must refuse: %s", cmd, out)
		} else if !strings.Contains(out, "no node") && !strings.Contains(out, "no instance") && !strings.Contains(out, "not found") && !strings.Contains(out, "no box") {
			t.Errorf("%s should refuse with a clean no-node answer, got: %s", cmd, out)
		}
	}

	if out, code := run("dabs rm owned-wt -y"); code != 0 {
		t.Fatalf("rm owned-wt -y failed (%d): %s", code, out)
	}
}
