package actions

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/core/manifest"
	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// Up resolves the manifest and starts a NEW pristine instance of its
// sandbox on the manifest's target (local by default), reporting the
// instance name.
func (r Real) Up(p params.Up) error {
	m, err := manifest.Load(p.ManifestPath)
	if err != nil {
		return err
	}
	drv, err := r.driverFor(m.Target)
	if err != nil {
		return err
	}
	spec := sandbox.Spec{
		Name:    m.Name,
		Workdir: m.Workdir,
		Env:     m.Env,
	}
	instance, err := drv.Up(spec)
	if err != nil {
		return err
	}
	if m.Target != "" {
		fmt.Fprintf(os.Stdout, "%s up on %s\n", instance, m.Target)
		return nil
	}
	fmt.Fprintf(os.Stdout, "%s up\n", instance)
	return nil
}
