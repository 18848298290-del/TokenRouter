package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

func TestPasskeyServiceDisabledFailsClosed(t *testing.T) {
	svc, err := providePasskey(&config.Config{}, nil, nil, nil)
	require.NoError(t, err)
	require.False(t, svc.Enabled())

	// Public login is disabled when relying-party security settings are absent.
	_, _, err = svc.BeginLogin(context.Background())
	require.ErrorIs(t, err, identity.ErrPasskeysDisabled)
}
