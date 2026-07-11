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
func (OS) Stat(path string) (fs.FileInfo, error)             { return os.Stat(path) }
func (OS) MkdirAll(path string, m fs.FileMode) error         { return os.MkdirAll(path, m) }
func (OS) MkdirTemp(dir, pattern string) (string, error)     { return os.MkdirTemp(dir, pattern) }
func (OS) RemoveAll(path string) error                       { return os.RemoveAll(path) }
func (OS) Getenv(key string) string                          { return os.Getenv(key) }
func (OS) LookupEnv(key string) (string, bool)               { return os.LookupEnv(key) }
func (OS) ExpandEnv(s string) string                         { return os.ExpandEnv(s) }

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
