package data

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// OS is the real Data layer: straight passthrough to the os package and the
// git binary. It holds no state.
type OS struct{}

func (OS) HomeDir() (string, error)                          { return os.UserHomeDir() }
func (OS) ReadFile(path string) ([]byte, error)              { return os.ReadFile(path) }
func (OS) WriteFile(p string, b []byte, m fs.FileMode) error { return os.WriteFile(p, b, m) }
func (OS) AppendFile(p string, b []byte, m fs.FileMode) error {
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, m)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(b)
	return err
}
func (OS) Stat(path string) (fs.FileInfo, error)         { return os.Stat(path) }
func (OS) MkdirAll(path string, m fs.FileMode) error     { return os.MkdirAll(path, m) }
func (OS) Mkdir(path string, m fs.FileMode) error        { return os.Mkdir(path, m) }
func (OS) MkdirTemp(dir, pattern string) (string, error) { return os.MkdirTemp(dir, pattern) }
func (OS) Getwd() (string, error)                        { return os.Getwd() }
func (OS) RemoveAll(path string) error                   { return os.RemoveAll(path) }
func (OS) EvalSymlinks(path string) (string, error)      { return filepath.EvalSymlinks(path) }
func (OS) Getenv(key string) string                      { return os.Getenv(key) }
func (OS) LookupEnv(key string) (string, bool)           { return os.LookupEnv(key) }
func (OS) ExpandEnv(s string) string                     { return os.ExpandEnv(s) }

func (OS) GitToplevel(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = "not a git repository"
		}
		return "", fmt.Errorf("%s", msg)
	}
	return strings.TrimSpace(string(out)), nil
}

func (OS) GitHasCommits(top string) bool {
	return exec.Command("git", "-C", top, "rev-parse", "--verify", "HEAD").Run() == nil
}

func (OS) GitCommonDir(worktree string) (string, error) {
	out, err := exec.Command("git", "-C", worktree, "rev-parse", "--git-common-dir").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = "not a git worktree"
		}
		return "", fmt.Errorf("%s", msg)
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(worktree, p)
	}
	return filepath.Clean(p), nil
}

func (OS) GitAddWorktree(top, branch, dest string) error {
	cmd := exec.Command("git", "-C", top, "worktree", "add", "-b", branch, dest, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (OS) ReadDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// baseRef returns the ref of the MAIN worktree (the point new work is measured
// against): its branch name, or — when the main repo is in detached HEAD — its
// HEAD sha. It parses the porcelain blocks in order; the FIRST block is always
// the main worktree, so we never mistake a linked worktree's branch for the base.
func baseRef(worktree string) string {
	out, err := exec.Command("git", "-C", worktree, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return ""
	}
	// The first block (up to the first blank line) is the main worktree.
	head := ""
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" { // end of the main worktree's block
			break
		}
		if b, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			return b // main is on a branch
		}
		if h, ok := strings.CutPrefix(line, "HEAD "); ok {
			head = strings.TrimSpace(h) // remember the sha in case main is detached
		}
	}
	return head // detached main → measure against its commit
}

func (OS) GitState(worktree string) (string, bool, int, error) {
	branch, err := exec.Command("git", "-C", worktree, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", false, 0, fmt.Errorf("git: %v", err)
	}
	status, err := exec.Command("git", "-C", worktree, "status", "--porcelain").Output()
	if err != nil {
		return "", false, 0, fmt.Errorf("git status: %v", err)
	}
	dirty := len(strings.TrimSpace(string(status))) > 0
	ahead := 0
	if base := baseRef(worktree); base != "" {
		out, err := exec.Command("git", "-C", worktree, "rev-list", "--count", base+"..HEAD").Output()
		if err != nil {
			return "", false, 0, fmt.Errorf("git rev-list %s..HEAD: %v", base, err)
		}
		fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &ahead)
	}
	return strings.TrimSpace(string(branch)), dirty, ahead, nil
}

// GitLanded asks git the content question directly: `merge-tree --write-tree
// base HEAD` computes the tree a real merge would produce, without touching
// any checkout. That tree equal to the base's own tree means the branch adds
// nothing — landed, however it landed (a squash included). A conflicting or
// failed merge-tree is not proof of anything, so it reads as not landed.
func (OS) GitLanded(worktree string) (bool, error) {
	base := baseRef(worktree)
	if base == "" {
		return false, nil
	}
	// An error here is a conflict, or a git too old for --write-tree (<2.38).
	// Both read as not landed: the pre-fix judgment, which never loses work —
	// it only keeps demanding --force for work that actually landed.
	merged, err := exec.Command("git", "-C", worktree, "merge-tree", "--write-tree", base, "HEAD").Output()
	if err != nil {
		return false, nil
	}
	baseTree, err := exec.Command("git", "-C", worktree, "rev-parse", base+"^{tree}").Output()
	if err != nil {
		return false, fmt.Errorf("git rev-parse %s^{tree}: %v", base, err)
	}
	return strings.TrimSpace(string(merged)) == strings.TrimSpace(string(baseTree)), nil
}

// GitPromptStatus reads `git status --porcelain=v2 --branch`, whose header
// lines carry the branch and ahead/behind counts and whose entry lines carry a
// two-letter XY staging code (X the index, Y the worktree) plus `?` untracked
// markers — everything a prompt needs in one call.
func (OS) GitPromptStatus(dir string) (GitPrompt, error) {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain=v2", "--branch").Output()
	if err != nil {
		return GitPrompt{}, fmt.Errorf("git status: %v", err)
	}
	var p GitPrompt
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			p.Branch = strings.TrimSpace(strings.TrimPrefix(line, "# branch.head "))
		case strings.HasPrefix(line, "# branch.ab "):
			fmt.Sscanf(strings.TrimPrefix(line, "# branch.ab "), "+%d -%d", &p.Ahead, &p.Behind)
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "):
			// "1 XY ..." (changed) or "2 XY ..." (renamed): X is the index state,
			// Y the worktree state; a '.' means unchanged in that half.
			if fields := strings.Fields(line); len(fields) >= 2 && len(fields[1]) == 2 {
				if fields[1][0] != '.' {
					p.Staged = true
				}
				if fields[1][1] != '.' {
					p.Unstaged = true
				}
			}
		case strings.HasPrefix(line, "u "):
			p.Unstaged = true // an unmerged path is work in the tree
		case strings.HasPrefix(line, "? "):
			p.Untracked = true
		}
	}
	return p, nil
}

func (OS) GitDiff(worktree string) (string, error) {
	var b strings.Builder
	if base := baseRef(worktree); base != "" {
		out, err := exec.Command("git", "-C", worktree, "diff", base+"...HEAD").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git diff %s...HEAD: %v: %s", base, err, strings.TrimSpace(string(out)))
		}
		b.Write(out)
	}
	out, err := exec.Command("git", "-C", worktree, "diff", "HEAD").CombinedOutput() // uncommitted
	if err != nil {
		return "", fmt.Errorf("git diff HEAD: %v: %s", err, strings.TrimSpace(string(out)))
	}
	b.Write(out)
	return b.String(), nil
}

// GitListWorktrees reads `git worktree list --porcelain` at top. The porcelain
// listing puts the main checkout first; it is dropped, so the result is the
// linked worktrees only.
func (OS) GitListWorktrees(top string) ([]string, error) {
	out, err := exec.Command("git", "-C", top, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %v", err)
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, strings.TrimSpace(p))
		}
	}
	if len(paths) > 0 {
		paths = paths[1:]
	}
	return paths, nil
}

func (OS) GitRemoveWorktree(worktree string) error {
	branch, _, _, _ := OS{}.GitState(worktree)
	// Resolve the main repo (via the shared git dir) before deleting anything.
	common, _ := exec.Command("git", "-C", worktree, "rev-parse", "--git-common-dir").Output()
	cd := strings.TrimSpace(string(common))
	if cd != "" && !filepath.IsAbs(cd) {
		cd = filepath.Join(worktree, cd)
	}
	repo := ""
	if cd != "" {
		repo = filepath.Dir(cd) // common dir is <repo>/.git
	}
	// Delete the worktree directory outright — the most reliable removal — then
	// let git drop its now-dangling registration and the branch.
	if err := os.RemoveAll(worktree); err != nil {
		return fmt.Errorf("remove worktree dir %s: %w", worktree, err)
	}
	if repo != "" {
		exec.Command("git", "-C", repo, "worktree", "prune").Run()
		if branch != "" {
			exec.Command("git", "-C", repo, "branch", "-D", branch).Run()
		}
	}
	return nil
}

// CopyDir copies src's contents into dst with cp(1), which preserves modes,
// symlinks and hardlinks — a naive walk does not.
func (OS) CopyDir(src, dst string) error {
	out, err := exec.Command("cp", "-a", src+"/.", dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy %s -> %s: %w: %s", src, dst, err, out)
	}
	return nil
}
