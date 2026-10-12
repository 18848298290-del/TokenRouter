package messageforward

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/pkg/requestcontext"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

type upstreamContextTestKey string

func TestDetachUpstreamContextIgnoresClientCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), upstreamContextTestKey("test-key"), "test-value"))
	upstreamCtx, release := detachedStreamContext(parent, true)
	defer release()

	cancel()

	require.NoError(t, upstreamCtx.Err())
	require.Equal(t, "test-value", upstreamCtx.Value(upstreamContextTestKey("test-key")))
}

func ResponseOptionsForTest(runtime *Runtime, ctx context.Context, output HTTPBoundary, target *gatewayadapter.ExecutionProvider, model string, passthrough bool) anthropic.ResponseOptions {
	return runtime.responseOptions(ctx, output, &AttemptState{}, target, model, passthrough)
}

// TestMessagesStreamAbort checks construction release, client disconnection, and internal abort separately.
func TestMessagesStreamAbort(t *testing.T) {
	client, cancelClient := context.WithCancel(context.Background())
	request, abort := requestcontext.WithAbort(client)
	defer abort()
	upstream, release := detachedStreamContext(request, true)
	release()
	require.NoError(t, upstream.Err())
	cancelClient()
	require.NoError(t, upstream.Err())
	abort()
	require.ErrorIs(t, upstream.Err(), context.Canceled)
}
