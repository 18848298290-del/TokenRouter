package googleforward

import (
	"context"
	"net/http"
	"strings"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

// Gemini retry uses one exchange configuration for Messages, native Google, and
// OpenAI output. The selected mode chooses request-building and terminal error
// presentation, while provider policy, status retry checks, and default health
// observation remain bound to the selected attempt.

// These modes select the client protocol error envelope when Gemini request
// construction or retry exhaustion produces a failure.
const (
	geminiExchangeMessages geminiExchangeMode = iota // Messages error output mode
	geminiExchangeNative                             // native Google error mode
	geminiExchangeOpenAI                             // OpenAI compatible error mode
)

// geminiExchangeMode selects the client error presenter used by Gemini exchange
// callbacks.
type geminiExchangeMode int

// geminiExchangeOptions binds retry, policy, health, and diagnostic callbacks
// for one provider. Messages and native modes recognize missing-project build
// errors specially, while OpenAI uses its protocol-specific build-error
// presentation.
func (s *Gemini) geminiExchangeOptions(c *attempt, ctx context.Context, provider *gatewayprovider.ExecutionProvider, model string, mode geminiExchangeMode, protocol gemininative.OpenAICompatProtocol) gemininative.ExchangeOptions {
	options := gemininative.ExchangeOptions{
		ProviderID:   provider.Record.ID,
		ProviderName: provider.Record.Name,
		Platform:     provider.Record.Platform,
		MaxRetries:   geminiMaxRetries,
		ReadError:    s.readUpstreamErrorBody,
		Sanitize:     logredact.SanitizeUpstreamQueries,
		Message:      upstream.ExtractErrorMessage,
		CheckPolicy: func(ctx context.Context, resp *http.Response) (bool, *http.Response) {
			return s.checkErrorPolicyInLoop(ctx, provider, resp, model)
		},
		ShouldRetry: func(code int) bool { return s.shouldRetryGeminiUpstreamError(provider, code) },
		OnStatus: func(ctx context.Context, status int, header http.Header, body []byte) {
			s.observeHealth(ctx, provider, status, header, body)
		},
		Observe: func(value gemininative.ExchangeNotice) {
			c.Observe(ops.OpsUpstreamErrorEvent{
				Platform:           value.Platform,
				ProviderID:         value.ProviderID,
				ProviderName:       value.ProviderName,
				UpstreamStatusCode: value.UpstreamStatusCode,
				UpstreamRequestID:  value.UpstreamRequestID,
				Kind:               value.Kind,
				Message:            value.Message,
				Detail:             value.Detail,
			})
		},
		SetError: func(code int, message, detail string) { c.SetError(code, message, detail) },
		Detail:   s.upstreamErrorDetail,
	}
	options.BuildError = func(err error) error {
		if mode == geminiExchangeOpenAI {
			return c.GeminiOpenAICompatError(protocol, http.StatusBadGateway, "upstream_error", err.Error())
		}
		status := http.StatusBadGateway
		kind := "upstream_error"
		if strings.Contains(err.Error(), "missing project_id") {
			status = http.StatusBadRequest
			kind = "invalid_request_error"
		}
		if mode == geminiExchangeNative {
			return c.GoogleError(status, err.Error())
		}
		return c.ClaudeError(status, kind, err.Error())
	}
	options.FinalError = func(message string) error {
		if mode == geminiExchangeNative {
			return c.GoogleError(http.StatusBadGateway, message)
		}
		if mode == geminiExchangeOpenAI {
			return c.GeminiOpenAICompatError(protocol, http.StatusBadGateway, "upstream_error", message)
		}
		return c.ClaudeError(http.StatusBadGateway, "upstream_error", message)
	}
	return options
}
