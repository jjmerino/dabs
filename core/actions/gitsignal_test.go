package actions

// Unit tests for the git-prompt signal: formatGitSignal turns a data.GitPrompt
// into a compact zsh-style string, and gitSignal routes a directory through the
// data seam (blank when it is not a repo).

import (
	"io/fs"
	"testing"

	"github.com/jjmerino/dabs/core/data"
)

func TestFormatGitSignal(t *testing.T) {
	cases := []struct {
		name string
		in   data.GitPrompt
		want string
	}{
		{"clean main is just the branch", data.GitPrompt{Branch: "main"}, "main"},
		{"unstaged", data.GitPrompt{Branch: "main", Unstaged: true}, "main *"},
		{"staged", data.GitPrompt{Branch: "main", Staged: true}, "main +"},
		{"untracked", data.GitPrompt{Branch: "main", Untracked: true}, "main %"},
		{"staged+unstaged+untracked in order", data.GitPrompt{Branch: "dev", Staged: true, Unstaged: true, Untracked: true}, "dev +*%"},
		{"ahead and behind", data.GitPrompt{Branch: "main", Ahead: 2, Behind: 3}, "main ⇡2⇣3"},
		{"flags then divergence", data.GitPrompt{Branch: "wip", Unstaged: true, Ahead: 1}, "wip *⇡1"},
		{"no branch is empty (not a repo)", data.GitPrompt{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatGitSignal(c.in); got != c.want {
				t.Fatalf("formatGitSignal(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// gitSignalData is a tiny Data whose only meaningful method is GitPromptStatus,
// so gitSignal's seam routing can be pinned without a full fake.
type gitSignalData struct {
	*vFakeData
	prompts map[string]data.GitPrompt
}

func (g gitSignalData) GitPromptStatus(dir string) (data.GitPrompt, error) {
	if p, ok := g.prompts[dir]; ok {
		return p, nil
	}
	return data.GitPrompt{}, fs.ErrNotExist
}

// CONTRACT: gitSignal reads the dir through the seam — a repo yields its
// formatted signal, a non-repo (or an empty dir) yields "".
func TestGitSignalRoutesThroughSeam(t *testing.T) {
	fd := gitSignalData{vFakeData: &vFakeData{}, prompts: map[string]data.GitPrompt{
		"/repo": {Branch: "main", Staged: true},
	}}
	r := New(nil, nil, nil, fd)

	if got := r.gitSignal("/repo"); got != "main +" {
		t.Fatalf("gitSignal on a repo = %q, want %q", got, "main +")
	}
	if got := r.gitSignal("/not-a-repo"); got != "" {
		t.Fatalf("gitSignal on a non-repo = %q, want empty", got)
	}
	if got := r.gitSignal(""); got != "" {
		t.Fatalf("gitSignal on an empty dir = %q, want empty", got)
	}
}
