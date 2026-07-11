package cli

import (
	"strings"

	"github.com/jjmerino/dabs/core/params"
)

// Command is one dabs subcommand.
type Command struct {
	Help string
	Run  func(c *CLI, args []string) error
}

// Commands maps each CLI-facing command to the core action it invokes.
// Each Run composes a pure parser from argparser.go with the action on the
// CLI's injected Actions; the logic lives in core/actions.
var Commands = map[string]Command{
	"build":     {"build a recipe's box image: build [recipe|path] (no name → dabs.yaml default)", (*CLI).runBuild},
	"auth":      {"log a harness into a persistent vault future boxes mount: auth claude", (*CLI).runAuth},
	"recipe":    {"run a named recipe box: recipe <name> [cmd…] (no name → dabs.yaml default)", (*CLI).runRecipe},
	"do":        {"run a command in a throwaway box via the default recipe (else sh): do <cmd…>", (*CLI).runDo},
	"cast":      {"run a recipe onto an existing worktree: cast <recipe> <worktree>", (*CLI).runCast},
	"recipes":   {"list the known recipes and what each mounts", (*CLI).runRecipes},
	"worktrees": {"inspect/reap recipe worktrees: worktrees [ls | diff <name> | rm <name> | prune] [--force]", (*CLI).runWorktrees},
	"up":        {"start a NEW detached box from a recipe (no command): up [recipe|path]", (*CLI).runUp},
	"exec":      {"exec an exact command inside an instance (no shell): exec <instance> -- <cmd…>", (*CLI).runExec},
	"run":       {"run a shell command inside an instance (args joined into one `sh -c` line — use `exec` for exact argv): run <instance> <shell…>", (*CLI).runRun},
	"down":      {"stop + remove instances by name (--force downs all matches)", (*CLI).runDown},
	"ls":        {"list sandboxes", (*CLI).runLs},
	"servers":   {"manage registered servers: servers [ls] | add <name> [host] | rm <name>", (*CLI).runServers},
}

func (c *CLI) runAuth(args []string) error {
	if len(args) != 1 {
		return BadArgsError{Cmd: "auth", Reason: "usage: auth <provider> (e.g. auth claude)"}
	}
	return c.actions.Auth(params.Auth{Provider: args[0]})
}

func (c *CLI) runRecipe(args []string) error {
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
	// do <cmd…>: everything is the command (flags included), appended to the
	// default recipe's command — no name, no `--` needed.
	return c.actions.Do(params.Do{Cmd: args})
}

func (c *CLI) runCast(args []string) error {
	if len(args) != 2 {
		return BadArgsError{Cmd: "cast", Reason: "usage: cast <recipe> <worktree> (worktree name from: dabs worktrees ls)"}
	}
	return c.actions.Recipe(params.Recipe{Name: args[0], Worktree: args[1]})
}

func (c *CLI) runWorktrees(args []string) error {
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

func (c *CLI) runServers(args []string) error {
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
