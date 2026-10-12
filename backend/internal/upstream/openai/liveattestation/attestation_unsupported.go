//go:build !darwin

package liveattestation

import "context"

type unsupportedProvider struct{}

// NewProvider returns an unsupported attestation provider on non-macOS systems.
func NewProvider() Provider {
	return unsupportedProvider{}
}

func (unsupportedProvider) Check(context.Context) error {
	return ErrUnsupportedPlatform
}

func (unsupportedProvider) Generate(context.Context) (string, error) {
	return "", ErrUnsupportedPlatform
}
