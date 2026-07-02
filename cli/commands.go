package cli

// Command is one dabs subcommand.
type Command struct {
	Help string
	Run  func(c *CLI, args []string) error
}

// Commands maps each CLI-facing command to the core action it invokes.
// Each Run composes a pure parser from argparser.go with the action on the
// CLI's injected Actions; the logic lives in core/actions.
var Commands = map[string]Command{
	"build": {"build the sandbox image from the manifest's Dockerfile", (*CLI).runBuild},
	"up":    {"start a NEW pristine instance (named <name>-<n>)", (*CLI).runUp},
	"run":   {"execute a command inside an instance: run <instance> -- <cmd…>", (*CLI).runRun},
	"down":  {"stop + remove an instance by name (see ls)", (*CLI).runDown},
	"mcp":   {"serve the dabash MCP tool on stdio, curried to an instance", (*CLI).runMcp},
	"ls":    {"list sandboxes", (*CLI).runLs},
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
