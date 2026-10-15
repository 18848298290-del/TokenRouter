package liveattestation

import (
	"context"
	"errors"
)

// attestation.go defines the Live attestation provider interface and errors
// for unavailable platform prerequisites.
// Live attestation needs a platform provider that can inspect local runtime
// prerequisites and generate a DeviceCheck header. The interface separates
// preflight checks from token generation, allowing gateway creation to report
// unsupported platforms or a missing ChatGPT installation before sending an
// upstream request.

// These sentinel errors distinguish unsupported host platforms from missing app
// prerequisites so callers can report the appropriate Live setup failure.
var (
	ErrUnsupportedPlatform = errors.New("live attestation requires TokenRouter to run on Apple Silicon macOS; other server platforms are not supported")
	ErrChatGPTAppMissing   = errors.New("live attestation requires the official ChatGPT app on the TokenRouter server")
)

// Provider checks local prerequisites and generates attestation for a Live
// request.
type Provider interface {
	// Check reports whether the host has usable attestation prerequisites under ctx.
	// A successful check establishes local runtime availability; Generate still
	// performs token generation for each Live attempt.
	Check(ctx context.Context) error
	// Generate returns the serialized header value for a Live attestation request.
	// Context cancellation bounds platform work and generation failures return an error for the caller.
	Generate(ctx context.Context) (string, error)
}
