package forward

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// gemini.go prepares Antigravity Gemini requests and coordinates recovery,
// error handling, and response capture. Antigravity Gemini input uses the
// platform's streaming upstream action even for a buffered client. Identity and
// schema preparation precede model-attribute application and internal
// project/model wrapping. The schema step can fail without replacing the
// identity-injected base used for later preparation.
//
// Retry and recovery finish before the response hook decides error output or
// provider switching. The hook retains original error evidence when unwrapping
// cannot produce a usable payload, and sticky switch signals become cache-billing
// metadata on the gateway error. Returned model metadata retains this wrapper's
// selected mapping even when provider-local recovery prepares another request;
// the upstream response declaration remains separately available for audit.

// GeminiSession contains group and session identifiers supplied by the entrypoint.
type GeminiSession struct {
	GroupID     int64  // selected request group
	SessionHash string // request session affinity hash
}

// GeminiSessionOption updates session settings; callers may omit options or pass nil, and later options overwrite earlier ones.
type GeminiSessionOption func(*GeminiSession)

// Gemini prepares one Antigravity Gemini exchange and returns its attempt
// observations or selected error outcome. Missing model/action or an empty body
// produces Google 400; unsupported actions produce 404. countTokens writes a
// local zero-token response before credential or model-eligibility checks.
//
// Generation requires a nonempty selected model and configured credential
// source. Identity injection, optional schema cleanup, model parameters, and
// wrapping feed the platform executor, whose hooks run retry/recovery before
// error handling. Unusable unwrapping retains source bytes. A successful result
// retains the selected mapped billing model and input model separately, while
// output observations and resource closure belong to the executor.
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
		// Count is a local compatibility response on this entrypoint. Complete it
		// before credential acquisition so count requests need no provider exchange
		// or selected-model eligibility beyond the required URL/body checks.
		p.ZeroCount()
		return &Result{
			RequestID: "",
			Usage:     upstream.TokenUsage{},
			Model:     originalModel,
			Stream:    false,
			Duration:  time.Since(startTime),
			Timings:   nil,
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
		// Credential failure stops preparation before request-body rewriting. The
		// synthetic unavailable payload gives the outer request loop a classified
		// provider failure to handle under its own switching allowance.
		return nil, p.Failover(502, []byte(`{"error":{"message":"Failed to get upstream access token","status":"UNAVAILABLE"}}`), false, false)
	}

	projectID, err := p.ProjectID()
	if err != nil {
		// Project lookup keeps its original error for the caller after requesting
		// a client envelope; an output error does not replace that preparation cause.
		_ = p.GoogleError(400, err.Error())
		return nil, err
	}

	p.Transport()

	// Build the identity-injected base before schema/model adjustments. Recovery
	// later uses this stage to prepare a replacement request with its own chosen
	// model attributes and internal wrapper.
	injectedBody, err := p.InjectIdentity(body)
	if err != nil {
		return nil, p.GoogleError(400, "Invalid request body")
	}

	// Schema cleanup is optional preparation. Retain the injected base on
	// failure so model attributes and wrapping can continue with usable original
	// request structure, while diagnostics retain the cleanup error.
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
		// Non-project wrapping failures use a generic client message. The wrapper
		// exposes no partially prepared internal project/model payload through it.
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
			// Preserve sticky-switch treatment when converting a platform signal.
			// The gateway request loop receives 503 and the cache-billing flag while
			// retaining responsibility for its remaining provider-switch budget.
			if switchSticky, ok := p.SwitchError(err); ok {
				return p.Failover(503, nil, false, switchSticky)
			}
			// Request cancellation has its own client-facing explanation after a
			// retry failure. It should not be presented as ordinary upstream retry
			// exhaustion when the request context already ended.
			if p.ClientCanceled() {
				return p.GoogleError(502, "Client disconnected before upstream response")
			}
			return p.GoogleError(502, "Upstream request failed after retries")
		}
		recovered, err = p.Recover(ctx, execution)
		if err != nil {
			// Recovery can also exhaust a provider-local attempt and request switching.
			// Carry its sticky flag through the same gateway error shape as retry
			// signals so the surrounding loop applies the same billing treatment.
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
		// Preserve source evidence when unwrapping fails or yields no body.
		// Diagnostics and final error output can still use the captured bytes,
		// while health classification receives the original wrapped response.
		if unwrapErr != nil || len(unwrappedForOps) == 0 {
			unwrappedForOps = respBody
		}
		p.Health(ctx, resp.StatusCode, resp.Headers, respBody, execution)
		upstreamMsg := strings.TrimSpace(p.ErrorMessage(unwrappedForOps))
		upstreamMsg = p.Sanitize(upstreamMsg)
		upstreamDetail := p.Detail(unwrappedForOps)

		p.SetError(resp.StatusCode, upstreamMsg, upstreamDetail)

		// A recognized configuration failure differs from ordinary request
		// validation despite sharing source status 400. Carry same-provider retry
		// permission into its failover signal before selecting raw error output.
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
		// Limit the routine log excerpt independently of optional detailed logging.
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
	// This wrapper assigns one image billing unit from the selected model's
	// image classification; it does not count generated objects in the response.
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
		Timings:               result.Timings,
		ClientDisconnect:      result.ClientDisconnect,
		ImageCount:            imageCount,
		ImageSize:             imageSize,
		ImageInputSize:        imageInputSize,
	}, nil
}

// WithGeminiSession returns an option assigning group and session affinity to
// a nonnil GeminiSession. Entrypoints skip nil options and apply options in
// order, so a later assignment replaces these values for the same request.
func WithGeminiSession(groupID int64, sessionHash string) GeminiSessionOption {
	return func(options *GeminiSession) { options.GroupID = groupID; options.SessionHash = sessionHash }
}
