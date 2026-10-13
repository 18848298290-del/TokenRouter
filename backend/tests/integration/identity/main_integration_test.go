//go:build integration

package identity_test

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// Identity integration cases obtain independent databases from one lazily
// created PostgreSQL container and migrated template. The integration runner
// keeps that suite alive through all case cleanups, then returns the suite
// result after closing its shared resources.

// databaseSuite owns the process-wide migrated PostgreSQL template and creates
// independent clones for identity cases.
var databaseSuite postgrescontainer.Suite

// runIdentityTests delegates the package runner and final shared-resource
// cleanup to databaseSuite. A suite cleanup failure changes the exit result to
// one after per-case clone cleanup has finished.
func runIdentityTests(m *testing.M) int { return databaseSuite.Run(m) }
