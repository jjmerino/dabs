package cli

import (
	"errors"
	"flag"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
)

// A single-character flag is written with ONE dash (-y), a multi-character flag
// with two (--yes). The flags block used to prepend "--" to every name, so a
// short flag rendered as "--y" — a token no shell accepts — while the usage line
// correctly showed "-y". The renderer must match the dash count to the name.
func TestFlagRowsSingleCharUsesOneDash(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	var b bool
	fs.BoolVar(&b, "y", false, "short")
	fs.BoolVar(&b, "yes", false, "long")
	fs.BoolVar(&b, "force", false, "multi")

	want := map[string]string{"short": "-y", "long": "--yes", "multi": "--force"}
	for _, row := range flagRows(fs) {
		name, usage := row[0], row[1]
		if exp, ok := want[usage]; ok {
			if name != exp {
				t.Errorf("flag with usage %q rendered as %q, want %q", usage, name, exp)
			}
			delete(want, usage)
		}
	}
	for usage := range want {
		t.Errorf("flag with usage %q not found in rendered rows", usage)
	}
}

// fakeActions records every delegation so tests can assert the cli parsed
// argv into the right action call with the right params.
type fakeActions struct {
	build    []params.Build
	exec     []params.Exec
	ls       []params.Ls
	rm       []params.Rm
	prune    []params.Prune
	recipe   []params.Recipe
	recipes  []params.Recipes
	cd       []params.Cd
	info     []params.Info
	services []params.Services
	err      error // returned from every action
}

func (f *fakeActions) Build(p params.Build) error               { f.build = append(f.build, p); return f.err }
func (f *fakeActions) Recipe(p params.Recipe) error             { f.recipe = append(f.recipe, p); return f.err }
func (f *fakeActions) Recipes(p params.Recipes) error           { f.recipes = append(f.recipes, p); return f.err }
func (f *fakeActions) Worktrees(params.Worktrees) error         { return f.err }
func (f *fakeActions) Cd(p params.Cd) error                     { f.cd = append(f.cd, p); return f.err }
func (f *fakeActions) Exec(p params.Exec) error                 { f.exec = append(f.exec, p); return f.err }
func (f *fakeActions) Rm(p params.Rm) error                     { f.rm = append(f.rm, p); return f.err }
func (f *fakeActions) Ls(p params.Ls) error                     { f.ls = append(f.ls, p); return f.err }
func (f *fakeActions) Info(p params.Info) error                 { f.info = append(f.info, p); return f.err }
func (f *fakeActions) Prune(p params.Prune) error               { f.prune = append(f.prune, p); return f.err }
func (f *fakeActions) ServersList(params.ServersList) error     { return f.err }
func (f *fakeActions) ServersAdd(params.ServersAdd) error       { return f.err }
func (f *fakeActions) ServersRemove(params.ServersRemove) error { return f.err }
func (f *fakeActions) Services(p params.Services) error {
	f.services = append(f.services, p)
	return f.err
}

func TestRunDelegatesToActions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want func(t *testing.T, f *fakeActions)
	}{
		{
			name: "cd takes exactly one node",
			args: []string{"cd", "boxy"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.cd) != 1 || f.cd[0] != (params.Cd{Node: "boxy"}) {
					t.Errorf("got %+v, want one Cd{Node:boxy}", f.cd)
				}
			},
		},
		{
			name: "info takes exactly one node",
			args: []string{"info", "boxy"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.info) != 1 || f.info[0] != (params.Info{Node: "boxy"}) {
					t.Errorf("got %+v, want one Info{Node:boxy}", f.info)
				}
			},
		},
		{
			name: "recipes --print with a name prints that recipe",
			args: []string{"recipes", "--print", "sh"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.recipes) != 1 || f.recipes[0] != (params.Recipes{Print: true, Name: "sh"}) {
					t.Errorf("got %+v, want one Recipes{Print:true Name:sh}", f.recipes)
				}
			},
		},
		{
			name: "recipes --print after the name still binds it",
			args: []string{"recipes", "sh", "--print"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.recipes) != 1 || f.recipes[0] != (params.Recipes{Print: true, Name: "sh"}) {
					t.Errorf("got %+v, want one Recipes{Print:true Name:sh}", f.recipes)
				}
			},
		},
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
			name: "recipe --no-command with a recipe name boots without running it",
			args: []string{"recipe", "m", "--no-command"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.recipe) != 1 || !f.recipe[0].NoCommand || f.recipe[0].Detach ||
					len(f.recipe[0].Args) != 1 || f.recipe[0].Args[0] != "m" {
					t.Errorf("got %+v, want one Recipe{NoCommand:true Args:[m]}", f.recipe)
				}
			},
		},
		{
			name: "recipe --no-command with no arg → the default recipe",
			args: []string{"recipe", "--no-command"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.recipe) != 1 || !f.recipe[0].NoCommand || len(f.recipe[0].Args) != 0 {
					t.Errorf("got %+v, want one Recipe{NoCommand:true} (default recipe)", f.recipe)
				}
			},
		},
		{
			name: "recipe --detach with a recipe name starts its command in the background",
			args: []string{"recipe", "m", "--detach"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.recipe) != 1 || !f.recipe[0].Detach || f.recipe[0].NoCommand ||
					len(f.recipe[0].Args) != 1 || f.recipe[0].Args[0] != "m" {
					t.Errorf("got %+v, want one Recipe{Detach:true Args:[m]}", f.recipe)
				}
			},
		},
		{
			name: "recipe: dabs flags end at --; the command keeps its own --detach",
			args: []string{"recipe", "m", "--", "mytool", "--detach"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.recipe) != 1 || f.recipe[0].Detach ||
					len(f.recipe[0].Args) != 3 || f.recipe[0].Args[2] != "--detach" {
					t.Errorf("got %+v, want one Recipe{Args:[m mytool --detach]} with Detach:false", f.recipe)
				}
			},
		},
		{
			name: "recipe: a --worktree after -- belongs to the command, not dabs",
			args: []string{"recipe", "--worktree", "wt1", "m", "--", "mytool", "--worktree", "x"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.recipe) != 1 || f.recipe[0].Worktree != "wt1" ||
					len(f.recipe[0].Args) != 4 || f.recipe[0].Args[2] != "--worktree" || f.recipe[0].Args[3] != "x" {
					t.Errorf("got %+v, want one Recipe{Worktree:wt1 Args:[m mytool --worktree x]}", f.recipe)
				}
			},
		},
		{
			name: "exec with exact argv after -- stays exact",
			args: []string{"exec", "demo-0", "--", "echo", "--flag", "hi"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.exec) != 1 || f.exec[0].Instance != "demo-0" || f.exec[0].Shell ||
					len(f.exec[0].Cmd) != 3 || f.exec[0].Cmd[1] != "--flag" {
					t.Errorf("got %+v, want one Exec{Instance:demo-0 Shell:false Cmd:[echo --flag hi]}", f.exec)
				}
			},
		},
		{
			name: "exec with no -- takes a shell command line after the instance",
			args: []string{"exec", "demo-0", "ls", "-la"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.exec) != 1 || f.exec[0].Instance != "demo-0" || !f.exec[0].Shell ||
					len(f.exec[0].Cmd) != 2 || f.exec[0].Cmd[0] != "ls" || f.exec[0].Cmd[1] != "-la" {
					t.Errorf("got %+v, want one Exec{Instance:demo-0 Shell:true Cmd:[ls -la]}", f.exec)
				}
			},
		},
		{
			// B12: a command word starting with `-` must reach the box verbatim,
			// not be eaten as one of dabs's own flags (which left an empty `sh -c`).
			name: "exec forwards a dash-leading command word as shell",
			args: []string{"exec", "box", "-x"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.exec) != 1 || f.exec[0].Instance != "box" || !f.exec[0].Shell ||
					len(f.exec[0].Cmd) != 1 || f.exec[0].Cmd[0] != "-x" {
					t.Errorf("got %+v, want Exec{Instance:box Shell:true Cmd:[-x]}", f.exec)
				}
			},
		},
		{
			// rm gains --multiple: a prefix matching several nodes needs it.
			name: "rm --multiple after the name",
			args: []string{"rm", "demo", "--multiple"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.rm) != 1 || f.rm[0].Node != "demo" || !f.rm[0].Multiple {
					t.Errorf("got %+v, want Rm{Node:demo Multiple:true}", f.rm)
				}
			},
		},
		{
			// --yes is the long alias of -y; both set Yes.
			name: "rm --yes",
			args: []string{"rm", "demo-0", "--yes"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.rm) != 1 || f.rm[0].Node != "demo-0" || !f.rm[0].Yes {
					t.Errorf("got %+v, want Rm{Node:demo-0 Yes:true}", f.rm)
				}
			},
		},
		{
			// --keep archives (what `down` used to do), stopping the box but
			// leaving its node record.
			name: "rm --keep after the name",
			args: []string{"rm", "demo", "--keep"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.rm) != 1 || f.rm[0].Node != "demo" || !f.rm[0].Keep {
					t.Errorf("got %+v, want Rm{Node:demo Keep:true}", f.rm)
				}
			},
		},
		{
			name: "rm --dry",
			args: []string{"rm", "demo", "--dry"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.rm) != 1 || f.rm[0].Node != "demo" || !f.rm[0].Dry {
					t.Errorf("got %+v, want Rm{Node:demo Dry:true}", f.rm)
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
			// The cli passes positionals through as Args (name-vs-default is the
			// action's call, against the registry); no `--`, so Default stays false.
			name: "recipe forwards its positionals as Args",
			args: []string{"recipe", "claude", "--model", "x"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.recipe) != 1 || f.recipe[0].Default ||
					len(f.recipe[0].Args) != 3 || f.recipe[0].Args[0] != "claude" ||
					f.recipe[0].Args[1] != "--model" || f.recipe[0].Args[2] != "x" {
					t.Errorf("got %+v, want Recipe{Args:[claude --model x]}", f.recipe)
				}
			},
		},
		{
			// A leading `--` forces the default recipe: it is stripped and Default set,
			// so the following token (which may name a recipe) is part of the command.
			name: "recipe -- forces the default path",
			args: []string{"recipe", "--", "sh", "-c", "echo hi"},
			want: func(t *testing.T, f *fakeActions) {
				if len(f.recipe) != 1 || !f.recipe[0].Default ||
					len(f.recipe[0].Args) != 3 || f.recipe[0].Args[0] != "sh" {
					t.Errorf("got %+v, want Recipe{Default:true Args:[sh -c echo hi]}", f.recipe)
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
		{"bad args", []string{"recipe", "a", "b", "--no-command"}, BadArgsError{Cmd: "recipe", Reason: "recipe --no-command takes an optional recipe name or dabs.yaml path and runs no command"}},
		{"both boot flags", []string{"recipe", "m", "--no-command", "--detach"}, BadArgsError{Cmd: "recipe", Reason: "--no-command and --detach ask for opposite things: one runs no command, the other starts the recipe's"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeActions{}
			err := New(f).Run(tt.args)
			if err != tt.wantErr {
				t.Errorf("Run(%v) = %v, want %v", tt.args, err, tt.wantErr)
			}
			if len(f.build)+len(f.recipe)+len(f.exec)+len(f.rm)+len(f.ls) != 0 {
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
		{"rm", []string{"dabs rm", "--keep", "--dry", "--force", "--clean-worktrees", "<node>"}},
		{"recipe", []string{"dabs recipe", "--no-command", "--detach", "--worktree"}},
		{"worktrees", []string{"dabs worktrees", "diff <name>"}},
		{"recipes", []string{"dabs recipes", "--print"}},
		{"ls", []string{"dabs ls", "--inactive", "git signal (in STATE):", "staged", "unstaged", "untracked"}},
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
				if n := len(f.build) + len(f.recipe) + len(f.rm) + len(f.ls); n != 0 {
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
	if err := New(f).Run([]string{"exec", "demo-0", "mytool", "--help"}); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if len(f.exec) != 1 || f.exec[0].Cmd[len(f.exec[0].Cmd)-1] != "--help" {
		t.Errorf("got %+v, want exec to forward --help to the box command", f.exec)
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

// CONTRACT: --no-command and --detach are two different asks and must never
// collapse into one param. --no-command boots a box and runs nothing; --detach
// boots one and starts the recipe's command in the background.
func TestNoCommandAndDetachAreDistinct(t *testing.T) {
	for _, tt := range []struct {
		flag              string
		noCommand, detach bool
	}{
		{"--no-command", true, false},
		{"--detach", false, true},
	} {
		f := &fakeActions{}
		c := New(f)
		if err := c.Run([]string{"recipe", "m", tt.flag}); err != nil {
			t.Fatalf("recipe m %s: %v", tt.flag, err)
		}
		if len(f.recipe) != 1 || f.recipe[0].NoCommand != tt.noCommand || f.recipe[0].Detach != tt.detach {
			t.Fatalf("recipe m %s: got %+v, want NoCommand:%v Detach:%v", tt.flag, f.recipe, tt.noCommand, tt.detach)
		}
	}
}

// CONTRACT: every command answers a leading -h/--help WITHOUT touching its
// actions — main serves per-command help before any driver is built, over a
// nil Actions, and this is what makes that safe.
func TestCommandHelpNeedsNoActions(t *testing.T) {
	for name := range Commands {
		for _, flag := range []string{"-h", "--help"} {
			err := New(nil).Run([]string{name, flag})
			if _, ok := err.(HelpRequestedError); !ok {
				t.Errorf("%s %s over nil actions = %v, want HelpRequestedError", name, flag, err)
			}
		}
	}
	if err := New(nil).Run(nil); (err != NoCommandError{}) {
		t.Errorf("bare run over nil actions = %v, want NoCommandError", err)
	}
}

// CONTRACT: a recipe name for `recipes` goes with --print; bare `recipes sh`
// stays a usage error rather than guessing.
func TestRecipesNameWithoutPrintRejected(t *testing.T) {
	err := New(&fakeActions{}).Run([]string{"recipes", "sh"})
	if _, ok := err.(BadArgsError); !ok {
		t.Fatalf("recipes sh = %v, want BadArgsError", err)
	}
}

// CONTRACT: `services` lists, `services serve` serves, and `services relay`
// answers ONE box's door — which needs the door path plus something to answer
// FOR: a registry (the box may publish), a carried socket, or both. A relay
// with a door alone would run and serve nothing. Anything else is a usage
// error rather than a silently-ignored argument.
func TestServicesParsesItsSubcommands(t *testing.T) {
	for _, tc := range []struct {
		args        []string
		wantServe   bool
		wantRelay   bool
		wantDir     string
		wantCarries []params.Carry
		wantErr     bool
	}{
		{args: nil},
		{args: []string{"serve"}, wantServe: true},
		{args: []string{"serve", "extra"}, wantErr: true},
		{args: []string{"list"}, wantErr: true},
		{args: []string{"relay"}, wantErr: true},
		{args: []string{"relay", "--door", "/n/door.sock"}, wantErr: true},
		{args: []string{"relay", "--dir", "/n/services"}, wantErr: true},
		{args: []string{"relay", "--door", "/n/door.sock", "--dir", "/n/services"}, wantRelay: true, wantDir: "/n/services"},
		{args: []string{"relay", "--door", "/n/door.sock", "--dir", "/n/services", "extra"}, wantErr: true},
		{args: []string{"relay", "--door", "/n/door.sock", "--carry", "/n/carry0.sock=/run/app.sock"},
			wantRelay: true, wantCarries: []params.Carry{{Listen: "/n/carry0.sock", Dial: "/run/app.sock"}}},
		{args: []string{"relay", "--door", "/n/door.sock", "--dir", "/n/services",
			"--carry", "/n/carry0.sock=/run/a.sock", "--carry", "/n/carry1.sock=/run/dir=name/b.sock"},
			wantRelay: true, wantDir: "/n/services",
			wantCarries: []params.Carry{{Listen: "/n/carry0.sock", Dial: "/run/a.sock"}, {Listen: "/n/carry1.sock", Dial: "/run/dir=name/b.sock"}}},
		{args: []string{"relay", "--door", "/n/door.sock", "--carry", "no-separator"}, wantErr: true},
		{args: []string{"relay", "--door", "/n/door.sock", "--egress", "/n/engine.sock"}, wantRelay: true},
	} {
		f := &fakeActions{}
		err := New(f).Run(append([]string{"services"}, tc.args...))
		if tc.wantErr {
			if err == nil {
				t.Errorf("services %v = nil, want a usage error", tc.args)
			}
			continue
		}
		if err != nil {
			t.Fatalf("services %v: %v", tc.args, err)
		}
		if len(f.services) != 1 || f.services[0].Serve != tc.wantServe || f.services[0].Relay != tc.wantRelay {
			t.Errorf("services %v → %+v, want Serve=%v Relay=%v", tc.args, f.services, tc.wantServe, tc.wantRelay)
		}
		if tc.wantRelay {
			if f.services[0].Door != "/n/door.sock" || f.services[0].Dir != tc.wantDir {
				t.Errorf("services %v → %+v, want the door and the registry it was given", tc.args, f.services[0])
			}
			if !reflect.DeepEqual(f.services[0].Carries, tc.wantCarries) {
				t.Errorf("services %v carries = %+v, want %+v", tc.args, f.services[0].Carries, tc.wantCarries)
			}
			if wantEgress := flagValue(tc.args, "--egress"); f.services[0].Egress != wantEgress {
				t.Errorf("services %v egress = %q, want %q", tc.args, f.services[0].Egress, wantEgress)
			}
		}
	}
}

// flagValue returns the value following a flag in args, or "".
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// `dabs version` must answer from the binary alone: main serves it with a nil
// Actions (no driver built), so the runner must never touch c.actions.
func TestVersionNeedsNoActions(t *testing.T) {
	err := New(nil).Run([]string{"version"})
	var v VersionRequestedError
	if !errors.As(err, &v) {
		t.Fatalf("version returned %v, want VersionRequestedError", err)
	}
	// The text is checked against the shapes the line is allowed to take,
	// not against the resolver that produced it: a resolver that returns
	// something else entirely must fail here.
	shape := regexp.MustCompile(`^dabs (v[0-9][^ \n]*|[0-9a-f]{1,12}(-dirty)? \(built from source\)|\(unknown\))\n$`)
	if !shape.MatchString(v.Text) {
		t.Errorf("version printed %q, want one line of the form `dabs <tag|commit|(unknown)>`", v.Text)
	}
}

// The verb takes no arguments; a stray one is a usage error, not a version line.
func TestVersionRejectsArguments(t *testing.T) {
	err := New(nil).Run([]string{"version", "extra"})
	var bad BadArgsError
	if !errors.As(err, &bad) {
		t.Fatalf("version extra returned %v, want BadArgsError", err)
	}
}

// The verb is listed in the top-level menu, so it needs a doc entry like any
// other command.
func TestVersionIsDocumented(t *testing.T) {
	if doc := commandDocs["version"]; doc.Help == "" || doc.Args == "" {
		t.Errorf("commandDocs[version] = %+v, want a Help and an Args", doc)
	}
}
