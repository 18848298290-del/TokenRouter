package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// antigravity_probe.go prepares an authenticated Antigravity connection test
// and binds the platform retry adapter. Antigravity connection tests address
// one selected OAuth provider through the same platform retries used by
// forwarding. Credentials, project and final model permission are resolved
// before a minimal request is built; the platform probe reads the bounded
// response and releases the application attempt gate.

// AntigravityProbe runs a selected-provider probe through platform retries and
// the application attempt gate.
type AntigravityProbe struct {
	// Attributes resolves the final model for routed group probes. A nil callback
	// skips profile lookup, leaving declared output and sampling permissions unknown.
	Attributes ModelAttributeLookup
	// Tokens supplies OAuth access credentials. A nil source returns a
	// configuration
	// error before project resolution or any platform request is attempted.
	Tokens *provider.AntigravityTokenSource
	// Retry is required after credential preparation and supplies health callbacks
	// together with the response-body limit used by the platform probe.
	Retry *AntigravityRetry
	// Enter optionally registers an application attempt. The platform probe invokes
	// its returned release after response reading and closing, including error
	// paths.
	Enter func() (func(), error)
}

// Execute probes one provider after resolving OAuth credentials, project and
// model admission.
// A nonnil record and configured retry service are required once credentials
// are available. Blank prompts become a period; automatic probes pass their
// supplied UserAgent. The platform probe closes its final response and releases
// an optional application attempt registration.
func (s *AntigravityProbe) Execute(ctx context.Context, value *provider.Record, request provider.PreparedTestRequest) (*antigravity.TestConnectionResult, error) {
	var model string
	if request.ProtocolRoute.ClientProtocol != "" {
		// Routed probes validate their final model and profile before acquiring
		// credentials, so a failed profile cannot trigger an upstream generation.
		model = MapAntigravityModel(value, request.Model)
		if model == "" {
			return nil, fmt.Errorf("model %s not in whitelist", request.Model)
		}
		if _, err := resolveProbeType(ctx, s.Attributes, value, request.GroupID, model, "gemini_generate_content", provider.ProviderTestTypeText); err != nil {
			return nil, err
		}
	}
	if s.Tokens == nil {
		return nil, errors.New("antigravity token provider not configured")
	}
	token, err := s.Tokens.GetAccessToken(ctx, value)
	if err != nil {
		return nil, fmt.Errorf("failed to get access_token: %w", err)
	}
	project, err := provider.ResolveAntigravityProjectID(value, antigravity.ErrProjectIDRequired)
	if err != nil {
		return nil, err
	}
	if model == "" {
		model = MapAntigravityModel(value, request.Model)
		if model == "" {
			return nil, fmt.Errorf("model %s not in whitelist", request.Model)
		}
	}
	prompt := "."
	if strings.TrimSpace(request.Prompt) != "" {
		prompt = strings.TrimSpace(request.Prompt)
	}
	var body []byte
	// The requested model family chooses the test encoder, while the admitted
	// mapped target is placed into either encoder so custom routing retains
	// its identity.
	if strings.HasPrefix(request.Model, "gemini-") {
		body, err = antigravity.BuildGeminiTestRequest(project, model, prompt)
	} else {
		body, err = antigravity.BuildClaudeTestRequest(project, model, prompt)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	proxy := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxy = value.Proxy.URL()
	}
	prefix := fmt.Sprintf("[antigravity-Test] provider=%d(%s)", value.ID, value.Name)
	input := AntigravityRetryRequest{
		Context: ctx, Provider: value, ModelStore: s.Retry.Health.Store, Prefix: prefix, ProxyURL: proxy, AccessToken: token, Action: "streamGenerateContent", Body: body, RequestedModel: request.Model,
		HandleError: func(status int, _ http.Header, body []byte) {
			// Probe failures are diagnostic observations; keep their body
			// bounded and redacted
			// while leaving provider health decisions to the shared retry
			// callbacks.
			logging.LegacyPrintf("service.antigravity_gateway", "%s test_handle_error status=%d model=%s provider=%d body=%s", prefix, status, request.Model, value.ID, logredact.TruncateLine(body, 200))
		},
	}
	if request.Automatic {
		input.UserAgent = request.UserAgent
	}
	adapter, attempt := s.Retry.Bind(input)
	return antigravity.Probe(ctx, attempt, adapter.Options, model, s.Retry.BodyLimit, s.Enter)
}
