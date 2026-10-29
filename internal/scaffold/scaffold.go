// Package scaffold holds example files that `rollout init` writes into an
// environment repository.
package scaffold

import _ "embed"

// Renovate is an example Renovate config for an environment repository.
//
//go:embed renovate.json5
var Renovate []byte
