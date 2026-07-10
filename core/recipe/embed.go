package recipe

import _ "embed"

// Bundled is the recipes registry shipped inside the binary. Load merges it
// with the user's ~/.dabs/recipes.yaml.
//
//go:embed recipes.yaml
var Bundled []byte
