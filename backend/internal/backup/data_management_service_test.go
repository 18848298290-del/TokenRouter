package backup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// TestDataManagementService_GetAgentHealth_Deprecated supplies a socket path
// with no agent behind it. Health must remain disabled, identify deprecation,
// and report the configured path even though no socket has been created there.
func TestDataManagementService_GetAgentHealth_Deprecated(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join(t.TempDir(), "unused.sock")
	svc := NewDataManagementServiceWithOptions(socketPath, 0)
	health := svc.GetAgentHealth(context.Background())

	require.False(t, health.Enabled)
	require.Equal(t, DataManagementDeprecatedReason, health.Reason)
	require.Equal(t, socketPath, health.SocketPath)
}

// TestDataManagementService_EnsureAgentEnabled_Deprecated calls the retired agent
// command guard with a chosen diagnostic path. Its HTTP mapping must report
// service unavailable, the deprecation reason, and that path in structured metadata.
func TestDataManagementService_EnsureAgentEnabled_Deprecated(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join(t.TempDir(), "unused.sock")
	svc := NewDataManagementServiceWithOptions(socketPath, 100)
	err := svc.EnsureAgentEnabled(context.Background())
	require.Error(t, err)

	statusCode, status := httpx.ToHTTP(err)
	require.Equal(t, 503, statusCode)
	require.Equal(t, DataManagementDeprecatedReason, status.Reason)
	require.Equal(t, socketPath, status.Metadata["socket_path"])
}
