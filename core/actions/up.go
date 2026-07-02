package actions

import (
	"fmt"
	"os"

	"github.com/jjmerino/dabs/core/manifest"
	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
)

// Up resolves the manifest and starts a NEW pristine instance of its
// sandbox, reporting the instance name.
func (r Real) Up(p params.Up) error {
	m, err := manifest.Load(p.ManifestPath)
	if err != nil {
		return err
	}
	spec := sandbox.Spec{
		Name:    m.Name,
		Workdir: m.Workdir,
		Env:     m.Env,
	}
	instance, err := r.driver.Up(spec)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "%s up\n", instance)
	return nil
}
