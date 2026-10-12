package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
)

// stubCredRepo returns the configured parent for credential and shadow-provider tests.
// Unimplemented methods on the embedded interface panic if called.
type stubCredRepo struct {
	ExecutionProviderStore

	parent *ExecutionProvider
}

func mustAPIKeyResponseModels(t *testing.T, ctx context.Context) []string {
	t.Helper()
	trace, ok := modeltrace.FromContext(ctx)
	require.True(t, ok)
	return trace.ResponseModels()
}

func (s *stubCredRepo) GetByID(_ context.Context, _ int64) (*ExecutionProvider, error) {
	return s.parent, nil
}

func newStubCredRepo(parent *ExecutionProvider) ExecutionProviderStore {
	return &stubCredRepo{parent: parent}
}
