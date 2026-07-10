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
	"build":     {"build the sandbox image from the manifest's Dockerfile", (*CLI).runBuild},
	"auth":      {"log a harness into a persistent vault future boxes mount: auth claude", (*CLI).runAuth},
	"recipe":    {"run a named recipe box: recipe <name> (e.g. recipe claude)", (*CLI).runRecipe},
	"recipes":   {"list the known recipes and what each mounts", (*CLI).runRecipes},
	"up":        {"start a NEW box from a manifest (dir or dabs.json); to run a recipe use `recipe`", (*CLI).runUp},
	"run":       {"execute a command inside an instance: run <instance> -- <cmd…>", (*CLI).runRun},
	"down":      {"stop + remove instances by name (--force downs all matches)", (*CLI).runDown},
	"mcp":       {"serve the dabash MCP tool on stdio, curried to an instance", (*CLI).runMcp},
	"ls":        {"list sandboxes", (*CLI).runLs},
	"servers":   {"manage registered servers: servers [ls] | add <name> [host] | rm <name>", (*CLI).runServers},
	"install":   {"install the dabash integration for a harness: install [pi|claude]", (*CLI).runInstall},
	"uninstall": {"remove a harness integration: uninstall <pi|claude>", (*CLI).runUninstall},
}

func (c *CLI) runInstall(args []string) error {
	h := ""
	if len(args) > 1 {
		return BadArgsError{Cmd: "install", Reason: "usage: install [pi|claude]"}
	}
	if len(args) == 1 {
		h = args[0]
	}
	return c.actions.Install(params.Install{Harness: h})
}

func (c *CLI) runUninstall(args []string) error {
	if len(args) != 1 {
		return BadArgsError{Cmd: "uninstall", Reason: "usage: uninstall <pi|claude>"}
	}
	return c.actions.Uninstall(params.Uninstall{Harness: args[0]})
}

func (c *CLI) runAuth(args []string) error {
	if len(args) != 1 {
		return BadArgsError{Cmd: "auth", Reason: "usage: auth <provider> (e.g. auth claude)"}
	}
	return c.actions.Auth(params.Auth{Provider: args[0]})
}

func (c *CLI) runRecipe(args []string) error {
	if len(args) != 1 {
		return BadArgsError{Cmd: "recipe", Reason: "usage: recipe <name> (see: dabs recipes)"}
	}
	return c.actions.Recipe(params.Recipe{Name: args[0]})
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

func (c *CLI) runMcp(args []string) error {
	p, err := parseMcp(args)
	if err != nil {
		return err
	}
	return c.actions.Mcp(p)
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
