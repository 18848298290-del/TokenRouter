package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	openaierrors "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// OpenAICompactSessionSeedKey stores the HTTP request's compact session seed.
const OpenAICompactSessionSeedKey = "openai_compact_session_seed"

// IsOpenAIResponsesCompactPath identifies the legacy compact endpoint and allowed subpaths.
func IsOpenAIResponsesCompactPath(c *gin.Context) bool {
	suffix := strings.TrimSpace(OpenAIResponsesRequestPathSuffix(c))
	return suffix == "/compact" || strings.HasPrefix(suffix, "/compact/")
}

// ResolveOpenAICompactSessionID uses a session header, then a saved seed, then a random ID.
func ResolveOpenAICompactSessionID(c *gin.Context) string {
	if c != nil {
		if sessionID := strings.TrimSpace(c.GetHeader("session_id")); sessionID != "" {
			return sessionID
		}
		if conversationID := strings.TrimSpace(c.GetHeader("conversation_id")); conversationID != "" {
			return conversationID
		}
		if seed, ok := c.Get(OpenAICompactSessionSeedKey); ok {
			if seedStr, ok := seed.(string); ok && strings.TrimSpace(seedStr) != "" {
				return strings.TrimSpace(seedStr)
			}
		}
	}
	return uuid.NewString()
}

// IsBareOpenAIResponsesPath matches Responses endpoints without subpaths such as /compact.
// Body-triggered compact promotion applies here; /responses/{id}/... subpaths keep their own handling.
func IsBareOpenAIResponsesPath(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	normalizedPath := strings.TrimRight(strings.TrimSpace(c.Request.URL.Path), "/")
	switch normalizedPath {
	case EndpointResponses, "/openai/v1/responses", "/responses", "/backend-api/codex/responses":
		return true
	default:
		return false
	}
}

// IsOpenAIRemoteCompactionV2Request identifies native remote compaction v2 by its wire format.
func IsOpenAIRemoteCompactionV2Request(body []byte) bool {
	stream, valid := ParseOpenAICompatibleStream(body)
	return valid && stream && protocolopenai.HasCompactionTriggerInInput(body)
}

// normalizeOpenAIResponsesCompactRequest uses streaming Responses for native v2 and the Compact bridge for other body
// triggers.
// It returns the normalized body; false means an error response has been written and the caller must stop.
func (h *OpenAITextHandler) normalizeOpenAIResponsesCompactRequest(c *gin.Context, reqLog *zap.Logger, body []byte) ([]byte, bool) {
	isCompactRequest := IsOpenAIResponsesCompactPath(c)
	if !isCompactRequest && IsBareOpenAIResponsesPath(c) && protocolopenai.HasCompactionTriggerInInput(body) {
		if normalized, changed, err := protocolopenai.NormalizeCompactionTriggerInputOrder(body); err != nil {
			reqLog.Warn("codex.remote_compact.trigger_order_normalization_failed", zap.Error(err))
		} else if changed {
			body = normalized
		}
		if IsOpenAIRemoteCompactionV2Request(body) {
			// Save v2 negotiation state for outbound header construction.
			MarkOpenAINativeCompactionV2(c)
			return body, true
		}
		c.Request.URL.Path = strings.TrimRight(c.Request.URL.Path, "/") + "/compact"
		isCompactRequest = true
		clientStream := gjson.GetBytes(body, "stream").Bool()
		if clientStream {
			MarkOpenAICompactClientStream(c)
		}
		reqLog.Info("codex.remote_compact.detected_body_signal", zap.Bool("client_stream", clientStream))
	}
	if !isCompactRequest {
		return body, true
	}
	if compactSeed := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); compactSeed != "" {
		c.Set(OpenAICompactSessionSeedKey, compactSeed)
	}
	normalizedCompactBody, normalizedCompact, compactErr := openaierrors.NormalizeOpenAICompactRequestBody(body)
	if compactErr != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to normalize compact request body")
		return nil, false
	}
	if normalizedCompact {
		body = normalizedCompactBody
	}
	return body, true
}

func (h *OpenAITextHandler) LogRemoteCompactOutcome(c *gin.Context, startedAt time.Time) {
	if !IsOpenAIResponsesCompactPath(c) {
		return
	}

	var (
		ctx    = context.Background()
		path   string
		status int
	)
	if c != nil {
		if c.Request != nil {
			ctx = c.Request.Context()
			if c.Request.URL != nil {
				path = strings.TrimSpace(c.Request.URL.Path)
			}
		}
		if c.Writer != nil {
			status = c.Writer.Status()
		}
	}

	outcome := "failed"
	if status >= 200 && status < 300 {
		outcome = "succeeded"
	}
	// After a heartbeat commits HTTP 200, MarkOpsStreamError records the failure represented by response.failed.
	if outcome == "succeeded" && c != nil {
		if _, hasStreamErr := GetOpsStreamError(c); hasStreamErr {
			outcome = "failed"
		}
	}
	latencyMs := max(time.Since(startedAt).Milliseconds(), 0)

	fields := []zap.Field{
		zap.String("component", "handler.openai_gateway.responses"),
		zap.Bool("remote_compact", true),
		zap.String("compact_outcome", outcome),
		zap.Int("status_code", status),
		zap.Int64("latency_ms", latencyMs),
		zap.String("path", path),
		zap.Bool("force_codex_cli", h != nil && h.options.ForceCodexCLI),
	}

	if c != nil {
		if userAgent := strings.TrimSpace(c.GetHeader("User-Agent")); userAgent != "" {
			fields = append(fields, zap.String("request_user_agent", userAgent))
		}
		if v, ok := c.Get(OpsModelKey); ok {
			if model, ok := v.(string); ok && strings.TrimSpace(model) != "" {
				fields = append(fields, zap.String("request_model", strings.TrimSpace(model)))
			}
		}
		if v, ok := c.Get(OpsProviderIDKey); ok {
			if providerID, ok := v.(int64); ok && providerID > 0 {
				fields = append(fields, zap.Int64("provider_id", providerID))
			}
		}
		if c.Writer != nil {
			if upstreamRequestID := strings.TrimSpace(c.Writer.Header().Get("x-request-id")); upstreamRequestID != "" {
				fields = append(fields, zap.String("upstream_request_id", upstreamRequestID))
			} else if upstreamRequestID := strings.TrimSpace(c.Writer.Header().Get("X-Request-Id")); upstreamRequestID != "" {
				fields = append(fields, zap.String("upstream_request_id", upstreamRequestID))
			}
		}
	}

	log := logging.FromContext(ctx).With(fields...)
	if outcome == "succeeded" {
		log.Info("codex.remote_compact.succeeded")
		return
	}
	log.Warn("codex.remote_compact.failed")
}
