package actions

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// Build resolves a recipe (no name → the registry default, a name → that
// recipe, a path → a dabs.yaml to load) and builds its box image on the
// recipe's target (local by default). It reuses the recipe engine's image
// resolution, so a bare-name image builds from its bundled recipe and an inline
// {dockerfile,context} image builds from that Dockerfile.
func (r Real) Build(p params.Build) error {
	reg, name, err := r.resolveRecipe(p.Name)
	if err != nil {
		return err
	}
	rec, err := reg.Get(name)
	if err != nil {
		return err
	}
	drv, err := r.driverFor(rec.Target)
	if err != nil {
		return err
	}
	// The `build` verb FORCES a rebuild of an inline-Dockerfile image — it is how
	// an edited Dockerfile is rebuilt, so it must not reuse an existing image the
	// way recipe/up do. A bare-name image has nothing of its own to build; it goes
	// through ensureImage, which builds it from the bundled recipe only if missing.
	if rec.Image.Dockerfile != "" {
		if _, err := r.buildDockerImage(drv, name, rec.Image); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, tui.Success("%s built", tui.Accent(name)))
	} else {
		if _, err := r.ensureImage(drv, name, rec.Image); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, tui.Success("using image %s (nothing to build)", tui.Accent(rec.Image.Name)))
	}
	return nil
}
