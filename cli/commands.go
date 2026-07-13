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
	"recipes":   {(*CLI).runRecipes},
	"worktrees": {(*CLI).runWorktrees},
	"exec":      {(*CLI).runExec},
	"ls":        {(*CLI).runLs},
	"rm":        {(*CLI).runRm},
	"prune":     {(*CLI).runPrune},
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
	"recipe":    {"run a recipe box: recipe [name] [cmd…] (unknown/omitted name → the default recipe, else sh, with the cmd appended); --worktree <wt> binds an existing worktree (git works in-box); --detach boots a NEW detached box and runs no command", "recipe [name] [cmd… | --detach] [--worktree <wt>]"},
	"recipes":   {"list the known recipes and what each mounts", "recipes [--print]"},
	"worktrees": {"inspect worktree nodes (reap with `dabs rm <name>` or `dabs rm --clean-worktrees`): worktrees [ls | diff <name>]", "worktrees [ls | diff <name>]"},
	"exec":      {"run a command inside a box: exec <node> -- <cmd…> for an exact argv, or exec <node> <shell…> for a `sh -c` line (pipes/globs/&&)", "exec <node> [--] <cmd…>"},
	"ls":        {"list what dabs owns, as a tree, live (--all: include archived nodes)", "ls [--all]"},
	"rm":        {"stop a box and remove its node and what it holds (--keep archives instead; --clean-worktrees sweeps every worktree with no unreviewed work): rm <node> [-y] [--keep] [--volume] [--multiple] [--dry] [--force] | rm --clean-worktrees [--force] [--dry]", "rm <node> [-y] [--keep] [--volume] [--multiple] [--dry] [--force] | rm --clean-worktrees [--force] [--dry]"},
	"prune":     {"reclaim built box images (they rebuild on the next build); --dry lists what exists, --force removes even images a live box uses", "prune [--dry] [--force]"},
	"servers":   {"manage registered servers: servers [ls] | add <name> [host] | rm <name>", "servers [ls | add <name> [host] | rm <name>]"},
}

func (c *CLI) runRecipe(args []string) error {
	if wantsHelp(args) {
		return HelpRequestedError{helpText("recipe", nil)}
	}
	// recipe [name] [cmd…]: a first arg naming a known recipe selects it and the
	// rest are appended to its command; otherwise (or with no args) the default
	// recipe runs with ALL args appended. The action resolves name-vs-default
	// against the registry. A leading `--` forces the default path — an escape
	// hatch for a command whose first token happens to be a recipe name.
	p := params.Recipe{}
	if len(args) > 0 && args[0] == "--" {
		p.Default = true
		p.Args = args[1:]
		return c.actions.Recipe(p)
	}
	// --detach boots a NEW pristine DETACHED box and runs no command; it takes an
	// optional recipe name or dabs.yaml path and no appended command. --worktree
	// <wt> binds an existing worktree to the recipe's `.` source (git works in-box)
	// instead of the cwd, and composes with --detach.
	//
	// dabs's own flags END at the first bare `--`: everything after it is the
	// appended command, verbatim, whatever recipe was named. Without that stop,
	// this scan reached into the user's command and ate its `--detach`/`--worktree`
	// tokens — `recipe sh -- mytool --worktree x` silently lost two of mytool's
	// arguments to dabs.
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i+1:]...)
			break
		}
		switch {
		case a == "--detach":
			p.Detach = true
		case a == "--worktree":
			if i+1 >= len(args) {
				return BadArgsError{Cmd: "recipe", Reason: "--worktree needs a worktree name (from: dabs worktrees ls)"}
			}
			i++
			p.Worktree = args[i]
		case strings.HasPrefix(a, "--worktree="):
			p.Worktree = strings.TrimPrefix(a, "--worktree=")
		default:
			rest = append(rest, a)
		}
	}
	if p.Detach && len(rest) > 1 {
		return BadArgsError{Cmd: "recipe", Reason: "recipe --detach takes an optional recipe name or dabs.yaml path and runs no command"}
	}
	if p.Worktree != "" && len(rest) == 0 {
		return BadArgsError{Cmd: "recipe", Reason: "recipe --worktree <wt> needs a recipe name (from: dabs recipes)"}
	}
	p.Args = rest
	return c.actions.Recipe(p)
}

func (c *CLI) runWorktrees(args []string) error {
	if wantsHelp(args) {
		return HelpRequestedError{helpText("worktrees", nil)}
	}
	p := params.Worktrees{}
	if len(args) > 2 {
		return BadArgsError{Cmd: "worktrees", Reason: "usage: worktrees [ls | diff <name>]"}
	}
	if len(args) > 0 {
		p.Sub = args[0]
	}
	if len(args) > 1 {
		p.Name = args[1]
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

func (c *CLI) runExec(args []string) error {
	p, err := parseExec(args)
	if err != nil {
		return err
	}
	return c.actions.Exec(p)
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

func (c *CLI) runPrune(args []string) error {
	if wantsHelp(args) {
		fs := newFlagSet("prune")
		fs.Bool("dry", false, "list what exists (sizes) without removing anything")
		fs.Bool("force", false, "remove even an image a live box still depends on")
		return HelpRequestedError{helpText("prune", fs)}
	}
	p := params.Prune{}
	for _, a := range args {
		switch a {
		case "--dry":
			p.Dry = true
		case "--force", "-f":
			p.Force = true
		default:
			return BadArgsError{Cmd: "prune", Reason: "usage: prune [--dry] [--force]"}
		}
	}
	return c.actions.Prune(p)
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
