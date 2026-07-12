// Package data is the seam between dabs's core logic and the host's stateful
// world: the filesystem, environment, and the local git binary. It is to those
// what sandbox.Driver is to containers — where fs/env/git I/O crosses out of
// core, so that logic can be exercised against a fake.
//
// The recipe, auth, and worktree actions route their I/O through this seam.
// Nothing here reaches the network or a container orchestrator — those already
// live behind sandbox.Driver.
package data

import "io/fs"

// Data is the injected host-effects layer. The real implementation is OS; tests
// pass a fake that records calls and returns canned results.
type Data interface {
	// --- filesystem ---
	HomeDir() (string, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, b []byte, perm fs.FileMode) error
	// AppendFile appends b to path (creating it if absent), for append-only
	// logs like the worktree box-lifecycle journal.
	AppendFile(path string, b []byte, perm fs.FileMode) error
	Stat(path string) (fs.FileInfo, error)
	MkdirAll(path string, perm fs.FileMode) error
	MkdirTemp(dir, pattern string) (string, error)
	RemoveAll(path string) error
	// CopyDir copies the contents of src into dst, which must exist. A node that
	// owns a copy of the code needs it on the HOST, so a box can mount it and a
	// human can read it.
	CopyDir(src, dst string) error

	// --- environment ---
	Getenv(key string) string
	// Getwd is the process working directory. Actions resolve relative paths
	// against it (filepath.Abs reads it behind your back), so it belongs on the
	// seam like every other environment read — otherwise a fake cannot control it.
	Getwd() (string, error)
	// LookupEnv reports a variable's value and whether it is SET (distinct from
	// set-but-empty), so a path expansion can tell "$UNSET" from "$EMPTY".
	LookupEnv(key string) (string, bool)
	ExpandEnv(s string) string

	// --- git (the one external process core drives directly) ---
	// GitToplevel returns the repo root containing dir, or an error if dir is
	// not in a git repo.
	GitToplevel(dir string) (string, error)
	// GitHasCommits reports whether the repo at top has a born HEAD (≥1 commit).
	GitHasCommits(top string) bool
	// GitAddWorktree creates a new worktree of top at dest on a new branch off
	// HEAD.
	GitAddWorktree(top, branch, dest string) error
	// ReadDir returns the entry names of dir (empty if dir is absent).
	ReadDir(dir string) ([]string, error)
	// GitState reports a worktree's branch and whether it holds unreviewed work:
	// dirty = uncommitted changes; ahead = commits on its branch not in the
	// repo's checked-out branch. A worktree with neither is safe to drop.
	GitState(worktree string) (branch string, dirty bool, ahead int, err error)
	// GitDiff returns the worktree's changes (uncommitted + commits ahead of
	// base) as a unified diff.
	GitDiff(worktree string) (string, error)
	// GitRemoveWorktree removes the worktree and deletes its branch.
	GitRemoveWorktree(worktree string) error
	// GitCommonDir returns the absolute path of the shared object store (the
	// parent repo's .git) backing a linked worktree — what `cast` must also
	// mount so git resolves inside the box.
	GitCommonDir(worktree string) (string, error)
}
