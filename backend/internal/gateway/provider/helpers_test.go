package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
)

// stubCredRepo returns one configured target for credential-parent lookup
// tests.
type stubCredRepo struct {
	// ExecutionProviderStore supplies the unused repository methods; invoking
	// one of them through the nil embedded interface panics in this fixture.
	ExecutionProviderStore

	parent *ExecutionProvider // configured credential parent target
}

// mustAPIKeyResponseModels retrieves response model stages from the request's
// redirect trace. The helper fails the test when the trace is absent before
// returning the captured ResponseModels slice.
func mustAPIKeyResponseModels(t *testing.T, ctx context.Context) []string {
	t.Helper()
	trace, ok := modeltrace.FromContext(ctx)
	require.True(t, ok)
	return trace.ResponseModels()
}

// GetByID returns the fixture's parent target regardless of the requested ID.
// Tests supply the intended parent directly; other repository operations
// remain on the embedded interface and must stay unused.
func (s *stubCredRepo) GetByID(_ context.Context, _ int64) (*ExecutionProvider, error) {
	return s.parent, nil
}

// newStubCredRepo constructs a credential lookup fixture with the supplied
// parent.
func newStubCredRepo(parent *ExecutionProvider) ExecutionProviderStore {
	return &stubCredRepo{parent: parent}
}
