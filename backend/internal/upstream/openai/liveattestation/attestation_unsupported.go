//go:build !darwin

package liveattestation

import "context"

// Non-macOS builds provide a Live attestation implementation that reports the
// unsupported-platform sentinel. Both prerequisite checking and generation
// return that same error, letting callers handle unavailable platform support
// before they attempt to create a Live call.

// unsupportedProvider reports unavailable Live attestation on non-macOS server
// builds.
type unsupportedProvider struct{}

// NewProvider returns the stateless provider for unsupported host builds.
func NewProvider() Provider {
	return unsupportedProvider{}
}

// Check returns ErrUnsupportedPlatform when Live attestation prerequisites are
// requested.
func (unsupportedProvider) Check(context.Context) error {
	return ErrUnsupportedPlatform
}

// Generate returns no attestation value and the unsupported-platform sentinel.
func (unsupportedProvider) Generate(context.Context) (string, error) {
	return "", ErrUnsupportedPlatform
}
