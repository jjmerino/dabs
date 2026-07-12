package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
)

// fakeActions records every delegation so tests can assert the cli parsed
// argv into the right action call with the right params.
type fakeActions struct {
	build  []params.Build
	up     []params.Up
	exec   []params.Exec
	run    []params.Run
	down   []params.Down
	ls     []params.Ls
	rm     []params.Rm
	images []params.Images
	recipe []params.Recipe
	do     []params.Do
	err    error // returned from every action
}

func (f *fakeActions) Build(p params.Build) error               { f.build = append(f.build, p); return f.err }
func (f *fakeActions) Up(p params.Up) error                     { f.up = append(f.up, p); return f.err }
func (f *fakeActions) Recipe(p params.Recipe) error             { f.recipe = append(f.recipe, p); return f.err }
func (f *fakeActions) Do(p params.Do) error                     { f.do = append(f.do, p); return f.err }
func (f *fakeActions) Recipes(params.Recipes) error             { return f.err }
func (f *fakeActions) Worktrees(params.Worktrees) error         { return f.err }
func (f *fakeActions) Exec(p params.Exec) error                 { f.exec = append(f.exec, p); return f.err }
func (f *fakeActions) Run(p params.Run) error                   { f.run = append(f.run, p); return f.err }
func (f *fakeActions) Down(p params.Down) error                 { f.down = append(f.down, p); return f.err }
func (f *fakeActions) Rm(p params.Rm) error                     { f.rm = append(f.rm, p); return f.err }
func (f *fakeActions) Ls(p params.Ls) error                     { f.ls = append(f.ls, p); return f.err }
func (f *fakeActions) Images(p params.Images) error             { f.images = append(f.images, p); return f.err }
func (f *fakeActions) ServersList(params.ServersList) error     { return f.err }
func (f *fakeActions) ServersAdd(params.ServersAdd) error       { return f.err }
func (f *fakeActions) ServersRemove(params.ServersRemove) error { return f.err }

func TestRunDelegatesToActions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want func(t *testing.T, f *fakeActions)
	}{
		{
			name: "build with a recipe name",
			args: []string{"build", "m"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.build) != 1 || f.build[0] != (params.Build{Name: "m"}) {
					t.Errorf("got %+v, want one Build{Name:m}", f.build)
				}
			},
		},
		{
			name: "build with no arg → the default recipe",
			args: []string{"build"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.build) != 1 || f.build[0] != (params.Build{}) {
					t.Errorf("got %+v, want one Build{} (default recipe)", f.build)
				}
			},
		},
		{
			name: "up with a recipe name",
			args: []string{"up", "m"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.up) != 1 || f.up[0] != (params.Up{Name: "m"}) {
					t.Errorf("got %+v, want one Up{Name:m}", f.up)
				}
			},
		},
		{
			name: "up with no arg → the default recipe",
			args: []string{"up"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.up) != 1 || f.up[0] != (params.Up{}) {
					t.Errorf("got %+v, want one Up{} (default recipe)", f.up)
				}
			},
		},
		{
			name: "exec with exact argv after --",
			args: []string{"exec", "demo-0", "--", "echo", "--flag", "hi"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.exec) != 1 || f.exec[0].Instance != "demo-0" ||
					len(f.exec[0].Cmd) != 3 || f.exec[0].Cmd[1] != "--flag" {
					t.Errorf("got %+v, want one Exec{Instance:demo-0 Cmd:[echo --flag hi]}", f.exec)
				}
			},
		},
		{
			name: "run takes a shell command line after the instance",
			args: []string{"run", "demo-0", "ls", "-la"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.run) != 1 || f.run[0].Instance != "demo-0" ||
					len(f.run[0].Cmd) != 2 || f.run[0].Cmd[0] != "ls" || f.run[0].Cmd[1] != "-la" {
					t.Errorf("got %+v, want one Run{Instance:demo-0 Cmd:[ls -la]}", f.run)
				}
			},
		},
		{
			// B12: a command word starting with `-` must reach the box verbatim,
			// not be eaten as one of dabs's own flags (which left an empty `sh -c`).
			name: "run forwards a dash-leading command word",
			args: []string{"run", "box", "-x"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.run) != 1 || f.run[0].Instance != "box" ||
					len(f.run[0].Cmd) != 1 || f.run[0].Cmd[0] != "-x" {
					t.Errorf("got %+v, want Run{Instance:box Cmd:[-x]}", f.run)
				}
			},
		},
		{
			// B14: rm gains --multiple, mirroring down.
			name: "rm --multiple after the name",
			args: []string{"rm", "demo", "--multiple"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.rm) != 1 || f.rm[0].Node != "demo" || !f.rm[0].Multiple {
					t.Errorf("got %+v, want Rm{Node:demo Multiple:true}", f.rm)
				}
			},
		},
		{
			name: "down",
			args: []string{"down", "demo-0"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.down) != 1 || f.down[0] != (params.Down{Instance: "demo-0"}) {
					t.Errorf("got %+v, want one Down{Instance:demo-0}", f.down)
				}
			},
		},
		{
			name: "down --multiple after the name",
			args: []string{"down", "demo", "--multiple"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.down) != 1 || f.down[0] != (params.Down{Instance: "demo", Multiple: true}) {
					t.Errorf("got %+v, want Down{Instance:demo Multiple:true}", f.down)
				}
			},
		},
		{
			name: "ls",
			args: []string{"ls"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.ls) != 1 {
					t.Errorf("got %+v, want one Ls call", f.ls)
				}
			},
		},
		{
			name: "recipe name with appended command",
			args: []string{"recipe", "claude", "--model", "x"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.recipe) != 1 || f.recipe[0].Name != "claude" ||
					len(f.recipe[0].Cmd) != 2 || f.recipe[0].Cmd[0] != "--model" {
					t.Errorf("got %+v, want Recipe{Name:claude Cmd:[--model x]}", f.recipe)
				}
			},
		},
		{
			name: "do passes all args as the command",
			args: []string{"do", "-c", "echo hi"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.do) != 1 || len(f.do[0].Cmd) != 2 || f.do[0].Cmd[0] != "-c" || f.do[0].Cmd[1] != "echo hi" {
					t.Errorf("got %+v, want Do{Cmd:[-c echo hi]}", f.do)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeActions{}
			if err := New(f).Run(tt.args); err != nil {
				t.Fatalf("Run(%v) = %v, want nil", tt.args, err)
			}
			tt.want(t, f)
		})
	}
}

func TestRunErrorsReachNoAction(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr error
	}{
		{"no command", nil, NoCommandError{}},
		{"unknown command", []string{"bogus"}, UnknownCommandError{Name: "bogus"}},
		{"bad args", []string{"up", "a", "b"}, BadArgsError{Cmd: "up", Reason: "expected an optional recipe name or dabs.yaml path"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeActions{}
			err := New(f).Run(tt.args)
			if err != tt.wantErr {
				t.Errorf("Run(%v) = %v, want %v", tt.args, err, tt.wantErr)
			}
			if len(f.build)+len(f.up)+len(f.exec)+len(f.run)+len(f.down)+len(f.ls) != 0 {
				t.Errorf("action was called despite error %v", err)
			}
		})
	}
}

// TestCommandHelpShowsOwnUsage asserts that `dabs <cmd> --help` (and -h) yields
// that command's OWN usage — its argument shape and its own flags — as a
// HelpRequestedError that calls NO action, and never leaks the top-level menu.
func TestCommandHelpShowsOwnUsage(t *testing.T) {
	tests := []struct {
		cmd  string
		want []string // substrings that must appear in the command's own help
	}{
		{"down", []string{"dabs down", "--force", "--dry", "<instance>"}},
		{"up", []string{"dabs up", "[recipe|path]"}},
		{"worktrees", []string{"dabs worktrees", "diff <name>", "prune", "--force"}},
		{"recipes", []string{"dabs recipes", "--print"}},
	}
	for _, tt := range tests {
		for _, flag := range []string{"--help", "-h"} {
			t.Run(tt.cmd+flag, func(t *testing.T) {
				f := &fakeActions{}
				err := New(f).Run([]string{tt.cmd, flag})
				var h HelpRequestedError
				if !errors.As(err, &h) {
					t.Fatalf("Run(%s %s) = %v, want HelpRequestedError", tt.cmd, flag, err)
				}
				for _, sub := range tt.want {
					if !strings.Contains(h.Text, sub) {
						t.Errorf("%s help missing %q in:\n%s", tt.cmd, sub, h.Text)
					}
				}
				// Its own help must NOT be the top-level command menu. The menu
				// lists sibling commands; a command's own help must not.
				if strings.Contains(h.Text, "dabs <command> [args]") {
					t.Errorf("%s help leaked the top-level menu:\n%s", tt.cmd, h.Text)
				}
				if n := len(f.build) + len(f.up) + len(f.down) + len(f.ls); n != 0 {
					t.Errorf("%s --help invoked an action", tt.cmd)
				}
			})
		}
	}
}

// TestPassThroughHelpNotIntercepted guards that a `--help` meant for the
// command run INSIDE a box is forwarded, not eaten as dabs's own help.
func TestPassThroughHelpNotIntercepted(t *testing.T) {
	f := &fakeActions{}
	if err := New(f).Run([]string{"run", "demo-0", "mytool", "--help"}); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if len(f.run) != 1 || f.run[0].Cmd[len(f.run[0].Cmd)-1] != "--help" {
		t.Errorf("got %+v, want run to forward --help to the box command", f.run)
	}
}

func TestRunPropagatesActionError(t *testing.T) {
	boom := errors.New("boom")
	f := &fakeActions{err: boom}
	if err := New(f).Run([]string{"ls"}); !errors.Is(err, boom) {
		t.Errorf("Run(ls) = %v, want %v", err, boom)
	}
}

// CONTRACT: the names people actually type reach the command they meant. A CLI
// that knows what you meant and refuses anyway is just being difficult.
func TestAliasesDispatch(t *testing.T) {
	for alias, want := range map[string]string{
		"worktree": "worktrees",
		"remove":   "rm",
		"delete":   "rm",
		"list":     "ls",
		"ps":       "ls",
	} {
		if _, ok := Commands[want]; !ok {
			t.Fatalf("alias %q points at %q, which is not a command", alias, want)
		}
		f := &fakeActions{}
		err := New(f).Run([]string{alias})
		// It must not be rejected as unknown; whatever the command then does with
		// no args is that command's business.
		var unknown UnknownCommandError
		if errors.As(err, &unknown) {
			t.Errorf("alias %q was rejected as an unknown command", alias)
		}
	}
	// An alias is NOT a second entry in the help: each command is listed once.
	if _, dup := Commands["worktree"]; dup {
		t.Error("alias leaked into the command table; help would list it twice")
	}
}
