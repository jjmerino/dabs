package cli

import (
	"errors"
	"testing"

	"github.com/jjmerino/dabs/core/params"
)

// fakeActions records every delegation so tests can assert the cli parsed
// argv into the right action call with the right params.
type fakeActions struct {
	build []params.Build
	up    []params.Up
	run   []params.Run
	down  []params.Down
	ls    []params.Ls
	err   error // returned from every action
}

func (f *fakeActions) Build(p params.Build) error { f.build = append(f.build, p); return f.err }
func (f *fakeActions) Up(p params.Up) error       { f.up = append(f.up, p); return f.err }
func (f *fakeActions) Run(p params.Run) error     { f.run = append(f.run, p); return f.err }
func (f *fakeActions) Down(p params.Down) error   { f.down = append(f.down, p); return f.err }
func (f *fakeActions) Ls(p params.Ls) error       { f.ls = append(f.ls, p); return f.err }

func TestRunDelegatesToActions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want func(t *testing.T, f *fakeActions)
	}{
		{
			name: "build",
			args: []string{"build", "m"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.build) != 1 || f.build[0] != (params.Build{ManifestPath: "m"}) {
					t.Errorf("got %+v, want one Build{ManifestPath:m}", f.build)
				}
			},
		},
		{
			name: "up",
			args: []string{"up", "m"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.up) != 1 || f.up[0] != (params.Up{ManifestPath: "m"}) {
					t.Errorf("got %+v, want one Up{ManifestPath:m}", f.up)
				}
			},
		},
		{
			name: "run with command tail",
			args: []string{"run", "exo-0", "--", "echo", "--flag", "hi"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.run) != 1 || f.run[0].Instance != "exo-0" ||
					len(f.run[0].Cmd) != 3 || f.run[0].Cmd[1] != "--flag" {
					t.Errorf("got %+v, want one Run{Instance:exo-0 Cmd:[echo --flag hi]}", f.run)
				}
			},
		},
		{
			name: "down",
			args: []string{"down", "exo-0"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.down) != 1 || f.down[0] != (params.Down{Instance: "exo-0"}) {
					t.Errorf("got %+v, want one Down{Instance:exo-0}", f.down)
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
		{"bad args", []string{"up", "a", "b"}, BadArgsError{Cmd: "up", Reason: "expected exactly one <manifest|dir> argument"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeActions{}
			err := New(f).Run(tt.args)
			if err != tt.wantErr {
				t.Errorf("Run(%v) = %v, want %v", tt.args, err, tt.wantErr)
			}
			if len(f.build)+len(f.up)+len(f.run)+len(f.down)+len(f.ls) != 0 {
				t.Errorf("action was called despite error %v", err)
			}
		})
	}
}

func TestRunPropagatesActionError(t *testing.T) {
	boom := errors.New("boom")
	f := &fakeActions{err: boom}
	if err := New(f).Run([]string{"ls"}); !errors.Is(err, boom) {
		t.Errorf("Run(ls) = %v, want %v", err, boom)
	}
}
