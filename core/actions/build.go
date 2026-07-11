package actions

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jjmerino/dabs/core/manifest"
	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/tui"
)

// Build resolves the manifest and builds its sandbox image on the
// manifest's target (local by default).
func (r Real) Build(p params.Build) error {
	m, err := manifest.Load(p.ManifestPath)
	if err != nil {
		return err
	}
	drv, err := r.driverFor(m.Target)
	if err != nil {
		return err
	}
	spec := sandbox.BuildSpec{
		Name:       m.Name,
		Dockerfile: filepath.Join(m.Dir, m.Dockerfile),
		Context:    filepath.Join(m.Dir, m.Context),
	}
	if err := drv.Build(spec); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, tui.Success("%s built", tui.Accent(m.Name)))
	return nil
}
