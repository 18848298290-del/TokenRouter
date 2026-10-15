package openaiattempt

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
)

// moderation.go records Cyber warning evidence from provider responses and
// forwarding errors.
// Attempt errors can carry upstream Cyber warning evidence independently of
// HTTP status and retry classification. Support copies the key and projects
// provider identity before delegating moderation, while the error-chain adapter
// reports whether evidence was present even if recording could not complete.

// RecordOpenAICyberWarning delegates upstream evidence with the current prompt
// excerpt. It reads the HTTP moderation snapshot through the next adapter and
// ignores the recording boolean; callers use request policy handling for
// completion eligibility.
func (h *Support) RecordOpenAICyberWarning(c *gin.Context, reqLog *zap.Logger, apiKey *apikey.APIKey, provider *gatewayprovider.ExecutionProvider, model string, statusCode int, responseBody []byte, warningText string) {
	h.RecordOpenAICyberWarningWithPromptExcerpt(c, reqLog, apiKey, provider, model, statusCode, responseBody, warningText, gatewayhttp.CurrentOpenAICyberWarningPromptExcerpt(c))
}

// RecordOpenAICyberWarningWithPromptExcerpt records evidence using the current
// HTTP moderation snapshot. The caller supplies the excerpt. The delegated
// result reports whether the shared warning recorder handled that evidence for
// this request.
func (h *Support) RecordOpenAICyberWarningWithPromptExcerpt(c *gin.Context, reqLog *zap.Logger, apiKey *apikey.APIKey, provider *gatewayprovider.ExecutionProvider, model string, statusCode int, responseBody []byte, warningText string, promptExcerpt string) bool {
	return h.RecordOpenAICyberWarningWithSnapshot(c, reqLog, apiKey, provider, model, statusCode, responseBody, warningText, promptExcerpt, gatewayhttp.CurrentOpenAICyberWarningSnapshot(c))
}

// RecordOpenAICyberWarningWithSnapshot delegates evidence using the supplied
// moderation snapshot. A nil Support returns false. The key is copied and
// provider identity projected before the shared recorder receives status, body,
// warning text, and excerpt.
func (h *Support) RecordOpenAICyberWarningWithSnapshot(c *gin.Context, reqLog *zap.Logger, apiKey *apikey.APIKey, provider *gatewayprovider.ExecutionProvider, model string, statusCode int, responseBody []byte, warningText string, promptExcerpt string, snapshot moderation.ContentModerationInput) bool {
	if h == nil {
		return false
	}
	return gatewayhttp.RecordOpenAICyberWarningWithSnapshot(gatewayhttp.GatewayModerationEndpoints{}, h.Moderation, c, reqLog, apikey.CopyAPIKey(apiKey), moderationProviderView(provider), model, statusCode, responseBody, warningText, promptExcerpt, snapshot)
}

// RecordOpenAIForwardErrorCyberWarning finds warning evidence carried by the
// forwarding error chain. It returns false when no warning exists, otherwise
// attempts recording and returns true regardless of that recording outcome. A
// positive warning status overrides the caller's supplied status.
func (h *Support) RecordOpenAIForwardErrorCyberWarning(c *gin.Context, reqLog *zap.Logger, apiKey *apikey.APIKey, provider *gatewayprovider.ExecutionProvider, model string, statusCode int, err error) bool {
	warning, ok := forwardcore.WarningFromError(err)
	if !ok || warning == nil {
		return false
	}
	if warning.StatusCode > 0 {
		statusCode = warning.StatusCode
	}
	// Evidence presence is the return criterion here. Recording delegates
	// its own handling rules, so callers must not interpret true as durable
	// warning persistence.
	h.RecordOpenAICyberWarning(c, reqLog, apiKey, provider, model, statusCode, warning.ResponseBody, warning.Message)
	return true
}

// moderationProviderView supplies the selected provider's identity fields for
// moderation operations. Nil provider returns nil. The result contains ID,
// name, and platform values used to associate upstream evidence with its
// current attempt.
func moderationProviderView(a *gatewayprovider.ExecutionProvider) *moderationflow.Provider {
	if a == nil {
		return nil
	}
	return &moderationflow.Provider{ID: a.Record.ID, Name: a.Record.Name, Platform: a.Record.Platform}
}
