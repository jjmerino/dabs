package cli

import (
	"errors"
	"testing"

	"github.com/jjmerino/dabs/core/actions"
)

// fakeActions records every delegation so tests can assert the cli parsed
// argv into the right action call with the right params.
type fakeActions struct {
	up   []actions.UpParams
	down []actions.DownParams
	ls   []actions.LsParams
	err  error // returned from every action
}

func (f *fakeActions) Up(p actions.UpParams) error     { f.up = append(f.up, p); return f.err }
func (f *fakeActions) Down(p actions.DownParams) error { f.down = append(f.down, p); return f.err }
func (f *fakeActions) Ls(p actions.LsParams) error     { f.ls = append(f.ls, p); return f.err }

func TestRunDelegatesToActions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want func(t *testing.T, f *fakeActions)
	}{
		{
			name: "up with flag and manifest",
			args: []string{"up", "--fresh", "m"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.up) != 1 || f.up[0] != (actions.UpParams{Manifest: "m", Fresh: true}) {
					t.Errorf("got %+v, want one Up{Manifest:m Fresh:true}", f.up)
				}
			},
		},
		{
			name: "up without flag",
			args: []string{"up", "m"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.up) != 1 || f.up[0] != (actions.UpParams{Manifest: "m"}) {
					t.Errorf("got %+v, want one Up{Manifest:m}", f.up)
				}
			},
		},
		{
			name: "down",
			args: []string{"down", "m"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.down) != 1 || f.down[0] != (actions.DownParams{Manifest: "m"}) {
					t.Errorf("got %+v, want one Down{Manifest:m}", f.down)
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
			if len(f.up)+len(f.down)+len(f.ls) != 0 {
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
