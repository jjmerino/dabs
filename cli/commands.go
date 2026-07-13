package cli

import (
	"strings"

	"github.com/jjmerino/dabs/core/params"
)

// Command is one dabs subcommand: the core action it invokes. Each Run
// composes a pure parser from argparser.go with the action on the CLI's
// injected Actions; the logic lives in core/actions.
type Command struct {
	Run func(c *CLI, args []string) error
}

// Commands maps each CLI-facing command name to its runner.
var Commands = map[string]Command{
	"build":     {(*CLI).runBuild},
	"recipe":    {(*CLI).runRecipe},
	"do":        {(*CLI).runDo},
	"cast":      {(*CLI).runCast},
	"recipes":   {(*CLI).runRecipes},
	"worktrees": {(*CLI).runWorktrees},
	"up":        {(*CLI).runUp},
	"exec":      {(*CLI).runExec},
	"run":       {(*CLI).runRun},
	"down":      {(*CLI).runDown},
	"ls":        {(*CLI).runLs},
	"rm":        {(*CLI).runRm},
	"images":    {(*CLI).runImages},
	"servers":   {(*CLI).runServers},
}

// cmdDoc is a command's human-facing help: Help is the one-line description
// shown in the top-level menu (`dabs --help`); Args is the argument shape
// shown (with the command's own flags, if any) by `dabs <cmd> --help`. This
// is pure data — kept separate from Commands so the runner map has no static
// reference back to itself (an initialization cycle) through the help path.
type cmdDoc struct{ Help, Args string }

var commandDocs = map[string]cmdDoc{
	"build":     {"build a recipe's box image: build [recipe|path] (no name → dabs.yaml default)", "build [recipe|path]"},
	"recipe":    {"run a named recipe box: recipe <name> [cmd…] (no name → dabs.yaml default)", "recipe [<name>] [cmd…]"},
	"do":        {"run a command in a throwaway box via the default recipe (else sh): do <cmd…>", "do <cmd…>"},
	"cast":      {"run a recipe onto an existing worktree: cast <recipe> <worktree>", "cast <recipe> <worktree>"},
	"recipes":   {"list the known recipes and what each mounts", "recipes [--print]"},
	"worktrees": {"inspect/reap worktree nodes (rm is `dabs rm` on one): worktrees [ls | diff <name> | rm <name> | prune] [--force]", "worktrees [ls | diff <name> | rm <name> | prune] [--force]"},
	"up":        {"start a NEW detached box from a recipe (no command): up [recipe|path]", "up [recipe|path]"},
	"exec":      {"exec an exact command inside a box (no shell): exec <node> -- <cmd…>", "exec <node> -- <cmd…>"},
	"run":       {"run a shell command inside a box (args joined into one `sh -c` line — use `exec` for exact argv): run <node> <shell…>", "run <node> <shell…>"},
	"down":      {"stop a box; its node is archived, not removed (--multiple for several matches)", "down [--force] [--dry] [--multiple] <node>"},
	"ls":        {"list what dabs owns, as a tree, live (--all: include archived nodes)", "ls [--all]"},
	"rm":        {"remove a node (a place or a box) and what it holds: rm <node> [-y] [--volume] [--force]", "rm <node> [-y] [--volume] [--force]"},
	"images":    {"list built box images, or reclaim them: images [prune] (they rebuild on the next build)", "images [prune]"},
	"servers":   {"manage registered servers: servers [ls] | add <name> [host] | rm <name>", "servers [ls | add <name> [host] | rm <name>]"},
}

func (c *CLI) runRecipe(args []string) error {
	if wantsHelp(args) {
		return HelpRequestedError{helpText("recipe", nil)}
	}
	// recipe [<name>] [cmd…]: the first arg is the recipe (no name → the
	// dabs.yaml default); any remaining args are a command appended to the
	// recipe's own command (which triggers a confirmation before running).
	p := params.Recipe{}
	if len(args) >= 1 {
		p.Name = args[0]
	}
	if len(args) > 1 {
		p.Cmd = args[1:]
	}
	return c.actions.Recipe(p)
}

func (c *CLI) runDo(args []string) error {
	if wantsHelp(args) {
		return HelpRequestedError{helpText("do", nil)}
	}
	// do <cmd…>: everything is the command (flags included), appended to the
	// default recipe's command — no name, no `--` needed.
	return c.actions.Do(params.Do{Cmd: args})
}

func (c *CLI) runCast(args []string) error {
	if wantsHelp(args) {
		return HelpRequestedError{helpText("cast", nil)}
	}
	if len(args) != 2 {
		return BadArgsError{Cmd: "cast", Reason: "usage: cast <recipe> <worktree> (worktree name from: dabs worktrees ls)"}
	}
	return c.actions.Recipe(params.Recipe{Name: args[0], Worktree: args[1]})
}

func (c *CLI) runWorktrees(args []string) error {
	if wantsHelp(args) {
		fs := newFlagSet("worktrees")
		fs.Bool("force", false, "with rm/prune: reap even worktrees with unreviewed work")
		return HelpRequestedError{helpText("worktrees", fs)}
	}
	p := params.Worktrees{}
	var pos []string
	for _, a := range args {
		if a == "--force" || a == "-f" {
			p.Force = true
			continue
		}
		pos = append(pos, a)
	}
	if len(pos) > 2 {
		return BadArgsError{Cmd: "worktrees", Reason: "usage: worktrees [ls | diff <name> | rm <name> | prune] [--force]"}
	}
	if len(pos) > 0 {
		p.Sub = pos[0]
	}
	if len(pos) > 1 {
		p.Name = pos[1]
	}
	return c.actions.Worktrees(p)
}

func (c *CLI) runRecipes(args []string) error {
	if wantsHelp(args) {
		fs := newFlagSet("recipes")
		fs.Bool("print", false, "print each recipe's resolved manifest, not just its name")
		return HelpRequestedError{helpText("recipes", fs)}
	}
	p := params.Recipes{}
	for _, a := range args {
		switch a {
		case "--print":
			p.Print = true
		default:
			return BadArgsError{Cmd: "recipes", Reason: "usage: recipes [--print]"}
		}
	}
	return c.actions.Recipes(p)
}

func (c *CLI) runBuild(args []string) error {
	p, err := parseBuild(args)
	if err != nil {
		return err
	}
	return c.actions.Build(p)
}

func (c *CLI) runUp(args []string) error {
	p, err := parseUp(args)
	if err != nil {
		return err
	}
	return c.actions.Up(p)
}

func (c *CLI) runExec(args []string) error {
	p, err := parseExec(args)
	if err != nil {
		return err
	}
	return c.actions.Exec(p)
}

func (c *CLI) runRun(args []string) error {
	p, err := parseRun(args)
	if err != nil {
		return err
	}
	return c.actions.Run(p)
}

func (c *CLI) runDown(args []string) error {
	p, err := parseDown(args)
	if err != nil {
		return err
	}
	return c.actions.Down(p)
}

func (c *CLI) runLs(args []string) error {
	p, err := parseLs(args)
	if err != nil {
		return err
	}
	return c.actions.Ls(p)
}

func (c *CLI) runRm(args []string) error {
	p, err := parseRm(args)
	if err != nil {
		return err
	}
	return c.actions.Rm(p)
}

func (c *CLI) runImages(args []string) error {
	if wantsHelp(args) {
		return HelpRequestedError{helpText("images", nil)}
	}
	p := params.Images{}
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "prune":
		p.Prune = true
	default:
		return BadArgsError{Cmd: "images", Reason: "usage: images [prune]"}
	}
	return c.actions.Images(p)
}

func (c *CLI) runServers(args []string) error {
	if wantsHelp(args) {
		return HelpRequestedError{helpText("servers", nil)}
	}
	sub := "ls"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "ls":
		if len(args) > 1 {
			return BadArgsError{Cmd: "servers", Reason: "usage: servers ls"}
		}
		return c.actions.ServersList(params.ServersList{})
	case "add":
		if len(args) < 2 || len(args) > 3 {
			return BadArgsError{Cmd: "servers", Reason: "usage: servers add <name> [host]"}
		}
		if strings.HasPrefix(args[1], "-") {
			return BadArgsError{Cmd: "servers", Reason: "server name cannot start with '-' (got " + args[1] + ")"}
		}
		p := params.ServersAdd{Name: args[1]}
		if len(args) == 3 {
			p.Host = args[2]
		}
		return c.actions.ServersAdd(p)
	case "rm":
		if len(args) != 2 {
			return BadArgsError{Cmd: "servers", Reason: "usage: servers rm <name>"}
		}
		return c.actions.ServersRemove(params.ServersRemove{Name: args[1]})
	default:
		return BadArgsError{Cmd: "servers", Reason: "unknown subcommand " + sub + " (ls | add | rm)"}
	}
}
