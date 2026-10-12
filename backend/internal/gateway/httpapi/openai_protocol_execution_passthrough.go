package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	openaiexecution "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Passthrough prepares the current provider request and handles upstream recovery.
func (s *OpenAITextExecutor) Passthrough(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body, canonicalImageIntentBody []byte, reqModel string, attemptImageIntentInvalidated bool, reasoningEffort *string, reqStream bool, startTime time.Time, tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult) (*forwardcore.OpenAIResult, error) {
	input := openaiexecution.PassthroughInput{Body: body, CanonicalImageIntentBody: canonicalImageIntentBody, Model: reqModel, ImageIntentInvalidated: attemptImageIntentInvalidated, ReasoningEffort: reasoningEffort, Stream: reqStream, StartedAt: startTime}
	p := &openAIPassthroughExecutionAdapter{openAIMessagesExecutionAdapter: &openAIMessagesExecutionAdapter{s: s, c: c, provider: provider, tls: tlsRouterMatch}}
	result, err := openaiexecution.RunPassthrough(ctx, input, p)
	out := openaiexecution.ToForwardResult(result)
	captureResponseModel(c, out)
	return out, err
}

func logOpenAIPassthroughInstructionsRejected(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	reqModel string,
	rejectReason string,
	body []byte,
) {
	if ctx == nil {
		ctx = context.Background()
	}
	providerID := int64(0)
	providerName := ""
	providerType := ""
	if provider != nil {
		providerID = provider.Record.ID
		providerName = strings.TrimSpace(provider.Record.Name)
		providerType = strings.TrimSpace(provider.Record.Type)
	}
	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.Int64("provider_id", providerID),
		zap.String("provider_name", providerName),
		zap.String("provider_type", providerType),
		zap.String("request_model", strings.TrimSpace(reqModel)),
		zap.String("reject_reason", strings.TrimSpace(rejectReason)),
	}
	fields = AppendCodexRejectedRequestFields(fields, c, body)
	logging.FromContext(ctx).With(fields...).Warn("OpenAI passthrough 本地拦截：Codex 请求缺少有效 instructions")
}

// shouldFailoverOpenAIPassthroughResponse decides whether to switch providers using provider type and platform
// error classification.
func shouldFailoverOpenAIPassthroughResponse(provider *gatewayprovider.ExecutionProvider, status int, body []byte) bool {
	return openaiexecution.ShouldFailoverPassthrough(status, body, openaiexecution.PassthroughFailureOptions{
		APIKey:        provider != nil && provider.Record.Type == capability.ProviderTypeAPIKey,
		Cyber:         func(b []byte) bool { hit, _, _ := openai.DetectOpenAICyberPolicy(b); return hit },
		ContextWindow: func(b []byte) bool { return openai.IsOpenAIContextWindowError("", b) },
		AccessState:   func(s int, b []byte) bool { return gatewayprovider.IsOpenAIHTTPUpstreamAccessStateError(s, "", b) },
		BodyTooLarge:  func(s int, b []byte) bool { return gatewayprovider.IsOpenAIRequestBodyTooLargeError(s, "", b) },
		PoolRetryable: func(s int) bool {
			return provider != nil && provider.View().IsPoolMode() && provider.View().IsPoolModeRetryableStatus(s)
		},
	})
}

func collectOpenAIPassthroughTimeoutHeaders(h http.Header) []string {
	if h == nil {
		return nil
	}
	var matched []string
	for key, values := range h {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if isOpenAIPassthroughTimeoutHeader(lowerKey) {
			entry := lowerKey
			if len(values) > 0 {
				entry = fmt.Sprintf("%s=%s", lowerKey, strings.Join(values, "|"))
			}
			matched = append(matched, entry)
		}
	}
	sort.Strings(matched)
	return matched
}
