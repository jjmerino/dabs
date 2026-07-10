package data

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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
func (OS) ExpandEnv(s string) string                         { return os.ExpandEnv(s) }

func (OS) GitToplevel(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository")
	}
	return strings.TrimSpace(string(out)), nil
}

func (OS) GitHasCommits(top string) bool {
	return exec.Command("git", "-C", top, "rev-parse", "--verify", "HEAD").Run() == nil
}

func (OS) GitAddWorktree(top, branch, dest string) error {
	cmd := exec.Command("git", "-C", top, "worktree", "add", "-b", branch, dest, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
