package httpapi

// These tests check HTTP close errors against gateway/ws provider-failure classification.

import (
	"errors"
	"fmt"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// TestShouldReportOpenAIWSProxyProviderFailure checks that local routing rejections do not penalize provider health.
func TestShouldReportOpenAIWSProxyProviderFailure(t *testing.T) {
	t.Run("本地模型路由拒绝不惩罚提供商", func(t *testing.T) {
		routingErr := errors.New("model is not supported by the selected websocket provider")
		err := fmt.Errorf("wrapped ingress turn: %w", gatewayhttp.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, gatewayws.EntryLocalRoutingReason("gpt-unsupported"), gatewayws.EntryLocalRoutingCause(routingErr)))

		require.False(t, gatewayws.EntryShouldReportFailure(err))
		require.ErrorIs(t, err, routingErr)
		var closeErr *gatewayhttp.OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
		require.Equal(t, "model gpt-unsupported is not available for this websocket group or provider", closeErr.Reason())
	})

	t.Run("上游策略错误仍惩罚提供商", func(t *testing.T) {
		err := gatewayhttp.NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			"upstream websocket authentication failed",
			errors.New("upstream rejected credentials"),
		)
		require.True(t, gatewayws.EntryShouldReportFailure(err))
	})

	t.Run("分组推理超限拒绝不惩罚提供商", func(t *testing.T) {
		overLimit := &routing.ReasoningEffortOverLimitError{Requested: "high", Max: "low"}
		err := gatewayhttp.NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			overLimit.Error(),
			overLimit,
		)
		require.False(t, gatewayws.EntryShouldReportFailure(err))
	})

	t.Run("普通代理错误仍惩罚提供商", func(t *testing.T) {
		require.True(t, gatewayws.EntryShouldReportFailure(errors.New("upstream websocket read failed")))
	})

	t.Run("空错误不惩罚提供商", func(t *testing.T) {
		require.False(t, gatewayws.EntryShouldReportFailure(nil))
	})
}
