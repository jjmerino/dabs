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
	"cd":        {(*CLI).runCd},
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
	"build":     {"build a recipe's box image: build [recipe|path] (no name → dabs.yaml default). A boot rebuilds automatically when the Dockerfile or a bundled image's files change; a change only to build-context files a `COPY .` pulls in is not detected — run `dabs prune` then build to pick it up", "build [recipe|path]"},
	"recipe":    {"run a recipe box: recipe [name] [cmd…] (unknown/omitted name → the default recipe, else sh, with the cmd appended); --worktree <wt> binds an existing worktree (git works in-box); --name <n> names the node the boot creates (unique; an inactive holder is reaped); --no-command boots a NEW box and runs no command (--detach: unstable alias — may later mean a true background detach)", "recipe [name] [cmd… | --no-command] [--worktree <wt>] [--name <n>]"},
	"recipes":   {"list the known recipes, one line each: name, description, and origin (bundled | global ~/.dabs/recipes.yaml | project ./dabs.yaml). --print dumps the full merged registry as YAML, sources and all, marking each recipe's origin; --print <name> dumps just that recipe", "recipes [--print [name]]"},
	"worktrees": {"inspect worktree nodes (reap with `dabs rm <name>` or `dabs rm --clean-worktrees`): worktrees [ls | diff <name>]", "worktrees [ls | diff <name>]"},
	"cd":        {"print a node's working place as a bare path, resolved per kind — a project to its source repo, a worktree to its checkout, a box to its node dir (~/.dabs/nodes/<id>); shells cannot be moved by a child process, so: cd \"$(dabs cd <node>)\". A box's node dir holds the three spaces as subdirectories: volume/ survives `rm --keep`, held/ carries work you would miss (a worktree's checkout, a workdir's copy — `rm` asks before reaping), tmp/ is scratch `rm` reaps quietly", "cd <node>"},
	"exec":      {"run a command inside a box: exec <node> -- <cmd…> for an exact argv, or exec <node> <shell…> for a `sh -c` line (pipes/globs/&&)", "exec <node> [--] <cmd…>"},
	"ls":        {"list the active subtrees dabs owns, as a tree (--inactive: show only the inactive ones instead)", "ls [--inactive]"},
	"rm":        {"stop a box and remove its node and what it holds (--keep keeps the record instead; --clean-worktrees sweeps every worktree with no unreviewed work; --inactive sweeps every inactive subtree): rm <node> [-y] [--keep] [--volume] [--multiple] [--dry] [--force] | rm --clean-worktrees [--force] [--dry] | rm --inactive [--dry]", "rm <node> [-y] [--keep] [--volume] [--multiple] [--dry] [--force] | rm --clean-worktrees [--force] [--dry] | rm --inactive [--dry]"},
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
			// A `--` with no recipe named before it is the default-recipe path,
			// however many dabs flags preceded it: `recipe --name x -- cmd` is
			// cmd on the default recipe, not a recipe called "cmd".
			if len(rest) == 0 {
				if p.Worktree != "" {
					return BadArgsError{Cmd: "recipe", Reason: "--worktree needs a recipe name before the `--`"}
				}
				p.Default = true
			}
			rest = append(rest, args[i+1:]...)
			break
		}
		switch {
		case a == "--detach", a == "--no-command":
			p.Detach = true
		case a == "--worktree":
			if i+1 >= len(args) {
				return BadArgsError{Cmd: "recipe", Reason: "--worktree needs a worktree name (from: dabs worktrees ls)"}
			}
			i++
			p.Worktree = args[i]
		case strings.HasPrefix(a, "--worktree="):
			p.Worktree = strings.TrimPrefix(a, "--worktree=")
		case a == "--name":
			if i+1 >= len(args) {
				return BadArgsError{Cmd: "recipe", Reason: "--name needs a name for the node the boot creates"}
			}
			i++
			p.NodeName = args[i]
		case strings.HasPrefix(a, "--name="):
			p.NodeName = strings.TrimPrefix(a, "--name=")
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

func (c *CLI) runCd(args []string) error {
	if wantsHelp(args) {
		return HelpRequestedError{helpText("cd", nil)}
	}
	if len(args) != 1 {
		return BadArgsError{Cmd: "cd", Reason: "usage: cd <node>"}
	}
	return c.actions.Cd(params.Cd{Node: args[0]})
}

func (c *CLI) runRecipes(args []string) error {
	if wantsHelp(args) {
		fs := newFlagSet("recipes")
		fs.Bool("print", false, "print the full merged registry (bundled + ~/.dabs/recipes.yaml + ./dabs.yaml) as YAML, marking each recipe's origin; `--print <name>` prints one recipe")
		return HelpRequestedError{helpText("recipes", fs)}
	}
	p := params.Recipes{}
	var rest []string
	for _, a := range args {
		switch {
		case a == "--print":
			p.Print = true
		case strings.HasPrefix(a, "-"):
			return BadArgsError{Cmd: "recipes", Reason: "usage: recipes [--print [name]]"}
		default:
			rest = append(rest, a)
		}
	}
	// A recipe name is only meaningful with --print (the listing already shows
	// every name); flag order is free, so `recipes sh --print` works too.
	if len(rest) > 1 || (len(rest) == 1 && !p.Print) {
		return BadArgsError{Cmd: "recipes", Reason: "usage: recipes [--print [name]]"}
	}
	if len(rest) == 1 {
		p.Name = rest[0]
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
