//go:build !integration

package identity_test

import "testing"

// Identity tests compiled without the integration tag run through the ordinary
// Go test runner. This build-specific adapter supplies the runner used by
// TestMain when PostgreSQL suite support is excluded, returning m.Run's status
// directly to process exit.

// runIdentityTests returns the ordinary Go package test result for builds
// excluding integration fixtures. TestMain performs process configuration
// before this call and exits with the returned status.
func runIdentityTests(m *testing.M) int { return m.Run() }
