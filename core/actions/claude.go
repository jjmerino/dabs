package actions

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// Claude starts Claude Code in a fresh, isolated box working on a git worktree
// of the current repo. Direct harness access is a git-repo-only feature: the
// worktree is what gives the box its own checkout to mutate without touching
// the user's working tree. The shared auth/config vault (~/.dabs/auth/claude,
// populated by `dabs auth claude`) is mounted at the harness's config path so
// the box starts authenticated and its config + logs live on the host.
//
// It is shorthand for hand-rolling a dabs.json + Dockerfile per the
// examples/claude-box recipe; the image comes from the bundled images/claude.
//
// The box is torn down on exit; the worktree is KEPT (its path printed) so any
// work done in the box is never silently discarded — the user reviews, merges,
// or removes it.
func (r Real) Claude(p params.Claude) error {
	drv, err := r.driverFor("") // direct harness access is a local concern
	if err != nil {
		return err
	}

	// 1. Git repo only. Otherwise warn and stop.
	top, err := gitToplevel()
	if err != nil {
		return fmt.Errorf("claude: direct harness access is only supported for git repos")
	}
	// A worktree needs a commit to branch off; a fresh repo has an unborn HEAD.
	if exec.Command("git", "rev-parse", "--verify", "HEAD").Run() != nil {
		return fmt.Errorf("claude: repo has no commits yet — make an initial commit first")
	}

	// 2. The config/auth vault must exist to mount; warn if it holds no
	//    credential yet (Claude will prompt /login inside, and because the
	//    vault is mounted the login persists for next time).
	vault, err := vaultDir("claude")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(vault, 0o700); err != nil {
		return fmt.Errorf("claude: vault %s: %w", vault, err)
	}
	if credAccessToken(filepath.Join(vault, ".credentials.json")) == "" {
		fmt.Fprintf(os.Stdout, "no Claude credential in %s — run `dabs auth claude` first, "+
			"or /login inside the box.\n", vault)
	}

	// 3. The box image. Normally built from the bundled recipe; DABS_CLAUDE_IMAGE
	//    names an already-built image to reuse instead (the e2e runner).
	name := "claude"
	if img := os.Getenv("DABS_CLAUDE_IMAGE"); img != "" {
		name = img
	} else {
		ctxDir, err := r.stageImage("claude")
		if err != nil {
			return err
		}
		defer os.RemoveAll(ctxDir)
		if err := drv.Build(sandbox.BuildSpec{
			Name:       name,
			Dockerfile: filepath.Join(ctxDir, "Dockerfile"),
			Context:    ctxDir,
		}); err != nil {
			return err
		}
	}

	// 4. A fresh worktree off HEAD, kept after exit.
	worktree, branch, err := addClaudeWorktree(top)
	if err != nil {
		return err
	}

	// 5. Box: the worktree at /work, the shared vault at the config path.
	instance, err := drv.Up(sandbox.Spec{
		Name:    name,
		Workdir: "/work",
		Env:     claudeConfigEnv, // reuse the vault as Claude's whole config dir
		Mounts: []sandbox.Mount{
			{Host: worktree, Path: "/work"},
			{Host: vault, Path: authClaudeDir},
		},
	})
	if err != nil {
		return err
	}
	defer drv.Down(instance)

	fmt.Fprintf(os.Stdout, "worktree %s (branch %s) → box\n", worktree, branch)
	entry := []string{"claude"}
	if p.Shell {
		entry = []string{"bash"}
		fmt.Fprintf(os.Stdout, "--shell: dropping into bash (claude not launched)\n")
	}
	if err := drv.Run(instance, entry); err != nil {
		return fmt.Errorf("claude: %w", err)
	}
	fmt.Fprintf(os.Stdout, "\nworktree kept at %s (branch %s)\n", worktree, branch)
	return nil
}

// gitToplevel returns the absolute root of the git repo containing the current
// directory, or an error if the current directory is not in a git repo.
func gitToplevel() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository")
	}
	return strings.TrimSpace(string(out)), nil
}

// addClaudeWorktree creates a fresh git worktree of top under
// ~/.dabs/worktrees/<repo>-<id> on a new branch dabs/claude-<id> off HEAD, and
// returns the worktree path and branch name.
func addClaudeWorktree(top string) (path, branch string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("claude: home: %w", err)
	}
	id := randHex(4)
	base := filepath.Join(home, ".dabs", "worktrees")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", "", fmt.Errorf("claude: worktrees dir: %w", err)
	}
	path = filepath.Join(base, filepath.Base(top)+"-"+id)
	branch = "dabs/claude-" + id
	cmd := exec.Command("git", "-C", top, "worktree", "add", "-b", branch, path, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("claude: git worktree add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return path, branch, nil
}

// randHex returns 2n hex chars of cryptographic randomness for instance/branch
// naming.
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
