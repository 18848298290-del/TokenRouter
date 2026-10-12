//go:build !integration

package identity_test

import "testing"

// runIdentityTests runs the tests that do not require PostgreSQL containers.
func runIdentityTests(m *testing.M) int { return m.Run() }
