package kind

import "github.com/jarrod-lowe/rdk/internal/kind/config"

// registry holds every kind keyed by name; regErr captures a duplicate-name
// programming error, surfaced by Validate. This var initializer is the one
// place the set of kinds is declared — adding a kind means adding one line here
// plus its package.
var registry, regErr = buildRegistry(
	config.New(),
)
