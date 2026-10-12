package forward

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// GeminiSession contains group and session identifiers supplied by the entrypoint.
type GeminiSession struct {
	GroupID     int64
	SessionHash string
}

// GeminiSessionOption updates session settings; callers may omit options or pass nil, and later options overwrite earlier ones.
type GeminiSessionOption func(*GeminiSession)

// Gemini prepares an Antigravity Gemini exchange, invokes recovery, and handles protocol output.
func Gemini(ctx context.Context, p GeminiPorts, in GeminiInput) (*Result, error) {
	startTime := in.StartedAt
	originalModel, action, stream, body, isStickySession := in.Model, in.Action, in.Stream, in.Body, in.Sticky
	prefix := in.Prefix

	if strings.TrimSpace(originalModel) == "" {
		return nil, p.GoogleError(400, "Missing model in URL")
	}
	if strings.TrimSpace(action) == "" {
		return nil, p.GoogleError(400, "Missing action in URL")
	}
	if len(body) == 0 {
		return nil, p.GoogleError(400, "Request body is empty")
	}

	// Capture requested dimensions for image billing.
	imageInputSize := p.ImageInputSize(body)
	imageSize := p.ImageTier(imageInputSize)

	switch action {
	case "generateContent", "streamGenerateContent":
	case "countTokens":
		// Answer countTokens locally with zero usage.
		p.ZeroCount()
		return &Result{
			RequestID:    "",
			Usage:        upstream.TokenUsage{},
			Model:        originalModel,
			Stream:       false,
			Duration:     time.Since(startTime),
			FirstTokenMs: nil,
		}, nil
	default:
		return nil, p.GoogleError(404, "Unsupported action: "+action)
	}

	mappedModel := p.MappedModel(originalModel)
	if mappedModel == "" {
		p.FeatureDenied()
		return nil, p.GoogleError(403, fmt.Sprintf("model %s not in whitelist", originalModel))
	}
	billingModel := mappedModel

	if !in.TokenAvailable {
		return nil, p.GoogleError(502, "Antigravity token provider not configured")
	}
	err := p.Credential(ctx)
	if err != nil {
		return nil, p.Failover(502, []byte(`{"error":{"message":"Failed to get upstream access token","status":"UNAVAILABLE"}}`), false, false)
	}

	projectID, err := p.ProjectID()
	if err != nil {
		_ = p.GoogleError(400, err.Error())
		return nil, err
	}

	p.Transport()

	// Antigravity requests require an identity prompt.
	injectedBody, err := p.InjectIdentity(body)
	if err != nil {
		return nil, p.GoogleError(400, "Invalid request body")
	}

	// Schema cleanup is best effort; retain the injected body on failure.
	if cleanedBody, err := p.CleanSchema(injectedBody); err == nil {
		injectedBody = cleanedBody
		p.Log(fmt.Sprintf("[Antigravity] Cleaned request schema in forwarded request for provider %s", in.ProviderName))
	} else {
		p.Log(fmt.Sprintf("[Antigravity] Failed to clean schema: %v", err))
	}

	effectiveBody, err := p.ModelParameters(ctx, mappedModel, injectedBody)
	if err != nil {
		return nil, err
	}

	wrappedBody, err := p.Wrap(projectID, mappedModel, effectiveBody)
	if err != nil {
		if p.ProjectRequired(err) {
			return nil, p.GoogleError(400, err.Error())
		}
		return nil, p.GoogleError(500, "Failed to build upstream request")
	}

	// Use streamGenerateContent upstream for both client modes.
	// Buffered clients receive the collected stream as a complete response.
	upstreamAction := "streamGenerateContent"

	var recovered GeminiRecovery
	execution := GeminiExecution{
		Model:          billingModel,
		OriginalModel:  originalModel,
		StartedAt:      startTime,
		Stream:         stream,
		Body:           wrappedBody,
		InjectedBody:   injectedBody,
		ProjectID:      projectID,
		UpstreamAction: upstreamAction,
		GroupID:        in.GroupID,
		SessionHash:    in.SessionHash,
		Sticky:         isStickySession,
	}
	hooks := GeminiHooks{}
	hooks.Exchange = func() error {
		err := p.Retry(ctx, execution)
		if err != nil {
			// Translate platform switch signals into gateway failover errors.
			if switchSticky, ok := p.SwitchError(err); ok {
				return p.Failover(503, nil, false, switchSticky)
			}
			// Distinguish client cancellation from failed upstream retries.
			if p.ClientCanceled() {
				return p.GoogleError(502, "Client disconnected before upstream response")
			}
			return p.GoogleError(502, "Upstream request failed after retries")
		}
		recovered, err = p.Recover(ctx, execution)
		if err != nil {
			if switchSticky, ok := p.SwitchError(err); ok {
				return p.Failover(503, nil, false, switchSticky)
			}
			return err
		}
		return nil
	}
	hooks.Before = func(ctx context.Context, resp *ExchangeResponse) (bool, error) {
		if resp.StatusCode < 400 {
			return false, nil
		}
		respBody, contentType := recovered.ErrorBody, recovered.ContentType

		requestID := resp.RequestID
		if requestID != "" {
			p.RequestID(requestID)
		}

		unwrapped, unwrapErr := p.Unwrap(respBody)
		unwrappedForOps := unwrapped
		if unwrapErr != nil || len(unwrappedForOps) == 0 {
			unwrappedForOps = respBody
		}
		p.Health(ctx, resp.StatusCode, resp.Headers, respBody, execution)
		upstreamMsg := strings.TrimSpace(p.ErrorMessage(unwrappedForOps))
		upstreamMsg = p.Sanitize(upstreamMsg)
		upstreamDetail := p.Detail(unwrappedForOps)

		p.SetError(resp.StatusCode, upstreamMsg, upstreamDetail)

		// Recognized server-configuration HTTP 400 errors permit same-provider retry and failover.
		if resp.StatusCode == 400 && p.GoogleConfigError(strings.ToLower(upstreamMsg)) {
			p.StdLog(fmt.Sprintf("%s status=400 google_config_error failover=true upstream_message=%q provider=%d", prefix, upstreamMsg, in.ProviderID))
			p.Observe(Notice{
				Platform:           in.Platform,
				ProviderID:         in.ProviderID,
				ProviderName:       in.ProviderName,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  requestID,
				Kind:               "failover",
				Message:            upstreamMsg,
				Detail:             upstreamDetail,
			})
			return true, p.Failover(resp.StatusCode, unwrappedForOps, true, false)
		}

		if p.ShouldFailover(resp.StatusCode) {
			p.Observe(Notice{
				Platform:           in.Platform,
				ProviderID:         in.ProviderID,
				ProviderName:       in.ProviderName,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  requestID,
				Kind:               "failover",
				Message:            upstreamMsg,
				Detail:             upstreamDetail,
			})
			return true, p.Failover(resp.StatusCode, unwrappedForOps, false, false)
		}
		if contentType == "" {
			contentType = "application/json"
		}
		p.Observe(Notice{
			Platform:           in.Platform,
			ProviderID:         in.ProviderID,
			ProviderName:       in.ProviderName,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  requestID,
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		p.Log(fmt.Sprintf("[antigravity-Forward] upstream error status=%d body=%s", resp.StatusCode, p.TruncateBytes(unwrappedForOps, 500)))
		p.ErrorBody(resp.StatusCode, contentType, unwrappedForOps)
		return true, fmt.Errorf("antigravity upstream error: %d", resp.StatusCode)
	}
	hooks.OutputError = func(err error) {
		kind := "stream_collect_error"
		if stream {
			kind = "stream_error"
		}
		p.Log(fmt.Sprintf("%s status=%s error=%v", prefix, kind, err))
	}
	result, err := p.Execute(ctx, execution, hooks)
	if err != nil {
		return nil, err
	}
	imageCount := 0
	if p.IsImageModel(mappedModel) {
		imageCount = 1
	}
	return &Result{
		RequestID:             result.RequestID,
		UpstreamHeaders:       result.UpstreamHeaders,
		UpstreamResponseModel: result.UpstreamResponseModel,
		Usage:                 result.Usage,
		Model:                 originalModel,
		UpstreamModel:         billingModel,
		Stream:                stream,
		Duration:              result.Duration,
		FirstTokenMs:          result.FirstTokenMs,
		ClientDisconnect:      result.ClientDisconnect,
		ImageCount:            imageCount,
		ImageSize:             imageSize,
		ImageInputSize:        imageInputSize,
	}, nil
}

func WithGeminiSession(groupID int64, sessionHash string) GeminiSessionOption {
	return func(options *GeminiSession) { options.GroupID = groupID; options.SessionHash = sessionHash }
}
