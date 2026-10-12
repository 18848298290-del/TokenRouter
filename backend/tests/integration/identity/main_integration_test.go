//go:build integration

package identity_test

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// databaseSuite shares the migration template within this test process.
var databaseSuite postgrescontainer.Suite

// runIdentityTests releases shared containers after the identity tests finish.
func runIdentityTests(m *testing.M) int { return databaseSuite.Run(m) }
