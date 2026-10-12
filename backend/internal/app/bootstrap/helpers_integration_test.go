//go:build integration

package bootstrap

import _ "github.com/TokenFlux/TokenRouter/ent/runtime"

// Ent field defaults and hooks are installed by the generated runtime package.
// Integration fixtures import that package for its initialization side
// effects, so entities created through Ent use their configured defaults and
// validation hooks during bootstrap tests.
