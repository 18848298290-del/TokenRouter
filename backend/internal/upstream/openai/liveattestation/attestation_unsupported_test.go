//go:build !darwin

package liveattestation

import (
	"context"
	"errors"
	"testing"
)

// TestUnsupportedProviderReturnsExplicitPlatformError constructs the provider
// selected by a non-macOS build. Both Check and Generate must return an error
// matching the shared unsupported-platform sentinel.
func TestUnsupportedProviderReturnsExplicitPlatformError(t *testing.T) {
	provider := NewProvider()
	if err := provider.Check(context.Background()); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("Check() error = %v, want ErrUnsupportedPlatform", err)
	}
	_, err := provider.Generate(context.Background())
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("Generate() error = %v, want ErrUnsupportedPlatform", err)
	}
}
