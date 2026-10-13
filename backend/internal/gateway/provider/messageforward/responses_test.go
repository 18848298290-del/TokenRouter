package messageforward

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/pkg/requestcontext"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// upstreamContextTestKey identifies a retained request value in cancellation
// fixtures.
type upstreamContextTestKey string

// TestDetachUpstreamContextIgnoresClientCancel cancels the parent after
// constructing a detached streaming context carrying a value. The upstream
// context remains usable and retains that request value after cancellation.
func TestDetachUpstreamContextIgnoresClientCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), upstreamContextTestKey("test-key"), "test-value"))
	upstreamCtx, release := detachedStreamContext(parent, true)
	defer release()

	// Cancel the client parent after detachment to check that the upstream
	// context still reads the retained value and remains live.
	cancel()

	require.NoError(t, upstreamCtx.Err())
	require.Equal(t, "test-value", upstreamCtx.Value(upstreamContextTestKey("test-key")))
}

// ResponseOptionsForTest exposes runtime response options with fresh attempt
// state to external tests. The supplied output and target stay borrowed, and
// the test owns response reading and cleanup.
func ResponseOptionsForTest(runtime *Runtime, ctx context.Context, output HTTPBoundary, target *gatewayadapter.ExecutionProvider, model string, passthrough bool) anthropic.ResponseOptions {
	return runtime.responseOptions(ctx, output, &AttemptState{}, target, model, passthrough)
}

// TestMessagesStreamAbort separates construction release, client cancellation,
// and the stored server abort for a streaming context. Release and client
// cancellation keep it live; internal abort returns context.Canceled.
func TestMessagesStreamAbort(t *testing.T) {
	client, cancelClient := context.WithCancel(context.Background())
	request, abort := requestcontext.WithAbort(client)
	defer abort()
	upstream, release := detachedStreamContext(request, true)
	// Invoke construction release before client cancellation and internal
	// abort to distinguish its empty operation from the server-controlled
	// stop used by detached work.
	release()
	require.NoError(t, upstream.Err())
	cancelClient()
	require.NoError(t, upstream.Err())
	abort()
	require.ErrorIs(t, upstream.Err(), context.Canceled)
}
