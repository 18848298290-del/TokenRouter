package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
)

func TestResolvedOpenAIUpstreamServiceTier(t *testing.T) {
	t.Parallel()

	priority := func() *string { v := "priority"; return &v }()

	t.Run("upstream echo stays separate from outbound tier", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		observer := BeginUpstreamResponseModelObservation(c)
		observer.ObserveOpenAI([]byte(`{"type":"response.completed","response":{"model":"gpt-5.5","service_tier":"default"}}`), "response.completed")

		got := ResolvedOpenAIUpstreamServiceTier(c, priority)
		require.NotNil(t, got)
		require.Equal(t, "priority", *got)
		require.Equal(t, "default", ObservedUpstreamResponseServiceTier(c))
	})

	t.Run("no upstream echo falls back to outbound tier", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		BeginUpstreamResponseModelObservation(c)

		got := ResolvedOpenAIUpstreamServiceTier(c, priority)
		require.NotNil(t, got)
		require.Equal(t, "priority", *got)
	})

	t.Run("observed tier never promotes an untiered request", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		observer := BeginUpstreamResponseModelObservation(c)
		observer.ObserveOpenAI([]byte(`{"type":"response.completed","response":{"model":"gpt-5.5","service_tier":"fast"}}`), "response.completed")

		got := ResolvedOpenAIUpstreamServiceTier(c, nil)
		require.Nil(t, got)
		require.Equal(t, "priority", ObservedUpstreamResponseServiceTier(c))
	})

	t.Run("no observer keeps outbound tier", func(t *testing.T) {
		got := ResolvedOpenAIUpstreamServiceTier(nil, priority)
		require.NotNil(t, got)
		require.Equal(t, "priority", *got)
	})

	t.Run("no observer and no outbound tier stays nil", func(t *testing.T) {
		require.Nil(t, ResolvedOpenAIUpstreamServiceTier(nil, nil))
	})

	t.Run("local observer stays separate from outbound tier", func(t *testing.T) {
		observer := &forwardcore.ResponseObserver{}
		observer.ObserveOpenAI([]byte(`{"type":"response.completed","response":{"model":"gpt-5.5","service_tier":"default"}}`), "response.completed")

		got := ResolvedOpenAIUpstreamServiceTierFromObserver(observer, priority)
		require.NotNil(t, got)
		require.Equal(t, "priority", *got)
	})

	t.Run("nil local observer falls back to outbound tier", func(t *testing.T) {
		got := ResolvedOpenAIUpstreamServiceTierFromObserver(nil, priority)
		require.NotNil(t, got)
		require.Equal(t, "priority", *got)
	})
}

// TestResponseModelRetryClearsPreviousObservation checks that a failed attempt's model is cleared before the next response.
func TestResponseModelRetryClearsPreviousObservation(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	o := BeginUpstreamResponseModelObservation(c)
	o.ObserveOpenAI([]byte(`{"response":{"model":"failed-runtime"}}`), "response.failed")
	resetResponseModel(c)
	require.Empty(t, UpstreamResponseModelObserverFromContext(c).Model())
	o.ObserveOpenAI([]byte(`{"response":{"model":"final-runtime"}}`), "response.completed")
	require.Equal(t, "final-runtime", UpstreamResponseModelObserverFromContext(c).Model())
}
