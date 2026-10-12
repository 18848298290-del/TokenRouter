package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// A disabled WebAuthn configuration constructs a disabled passkey service even
// with unset storage dependencies. The login entry point checks that state
// before ceremony work, returning ErrPasskeysDisabled so disabled deployments
// cannot begin a login.

// TestPasskeyServiceDisabledFailsClosed constructs passkeys with WebAuthn
// disabled and nil dependencies. Construction must succeed, Enabled must be
// false, and BeginLogin must return ErrPasskeysDisabled.
func TestPasskeyServiceDisabledFailsClosed(t *testing.T) {
	svc, err := providePasskey(&config.Config{}, nil, nil, nil)
	require.NoError(t, err)
	require.False(t, svc.Enabled())

	// The Enabled flag remains false in this configuration. BeginLogin checks
	// the disabled state before accessing the nil verifier or storage dependencies.
	_, _, err = svc.BeginLogin(context.Background())
	require.ErrorIs(t, err, identity.ErrPasskeysDisabled)
}
