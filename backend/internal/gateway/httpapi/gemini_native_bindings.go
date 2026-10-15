package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	protocolgemini "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
)

// gemini_native_bindings.go binds Gemini request validation, moderation, and
// digest-session services to the native HTTP handler.
// Gemini native admission shares Messages funding and observation services,
// but uses Gemini content moderation and digest identity. Bound callbacks
// validate the model path segment and resolve or write the sticky provider
// association for that request.

// GeminiHTTPBindings supplies URL validation and persistent digest-session
// associations.
type GeminiHTTPBindings struct {
	SafeModelSegment func(string) bool // URL model validation
	// FindSession matches a group, prefix and conversation chain, returning the
	// session UUID, provider, matched chain and presence flag used by admission.
	FindSession func(context.Context, int64, string, string) (string, int64, string, bool)
	BindSticky  func(context.Context, *int64, string, int64) error // session-provider binding write
}

// geminiNativeHTTPBackend combines shared admission bindings with Gemini
// session callbacks.
type geminiNativeHTTPBackend struct {
	messagesHTTPBackend                    // shared admission operations
	gemini              GeminiHTTPBindings // native Gemini callbacks
}

// NewBoundGeminiNativeHandler creates Gemini HTTP admission using the supplied
// Messages services and Gemini callbacks. It retains these dependencies;
// runtime preparation and provider switching execute through the supplied
// executor.
func NewBoundGeminiNativeHandler(options GeminiNativeOptions, bindings MessagesBindings, gemini GeminiHTTPBindings, prompt MessagesPrompt, concurrency *ConcurrencyHelper, newID func() string, executor execution.Executor) *GeminiNativeHandler {
	return NewGeminiNativeHandler(options, geminiNativeHTTPBackend{messagesHTTPBackend{bindings}, gemini}, prompt, concurrency, newID, executor)
}

// HasForcedPlatform reports whether API-key middleware installed a forced
// platform.
func (p geminiNativeHTTPBackend) HasForcedPlatform(c *gin.Context) bool {
	return keyhttp.HasForcePlatform(c)
}

// SafeModelSegment delegates URL model validation to the bound Gemini
// callback.
func (p geminiNativeHTTPBackend) SafeModelSegment(model string) bool {
	return p.gemini.SafeModelSegment(model)
}

// Moderate submits the current Gemini body and authenticated subject to
// content moderation. It passes an API-key copy and returns the configured
// decision, including nil when moderation produces none.
func (p geminiNativeHTTPBackend) Moderate(c *gin.Context, log *zap.Logger, key *apikey.APIKey, subject authctx.AuthSubject, model string, body []byte) *moderation.Decision {
	return RunContentModeration(GatewayModerationEndpoints{}, c, log, p.bindings.Moderation, apikey.CopyAPIKey(key), subject, moderation.ContentModerationProtocolGemini, model, body)
}

// Isolate checks this Gemini session against the configured owner-isolation
// service. An absent callback permits the request; otherwise the returned
// error controls native Google-format admission rejection.
func (p geminiNativeHTTPBackend) Isolate(ctx context.Context, key *apikey.APIKey, userID int64, hash string) error {
	if p.bindings.IsolateSession == nil {
		return nil
	}
	return p.bindings.IsolateSession(ctx, apikey.CopyAPIKey(key), userID, session.SessionIsolationSourceGemini, hash)
}

// DigestChain returns the ordered content digests for Gemini conversation
// matching.
func (p geminiNativeHTTPBackend) DigestChain(request *protocolgemini.GeminiRequest) string {
	return session.BuildGeminiDigestChain(request)
}

// PrefixHash hashes caller, Key, client and model inputs for digest lookup.
func (p geminiNativeHTTPBackend) PrefixHash(userID, keyID int64, ip, agent, platform, model string) string {
	return session.GenerateGeminiPrefixHash(userID, keyID, ip, agent, platform, model)
}

// FindSession delegates group-scoped prefix and chain matching to the
// configured callback. Callers use its presence flag before interpreting the
// returned UUID, provider identity and matched chain.
func (p geminiNativeHTTPBackend) FindSession(ctx context.Context, groupID int64, prefix, chain string) (string, int64, string, bool) {
	return p.gemini.FindSession(ctx, groupID, prefix, chain)
}

// DigestSessionKey returns the sticky key for a digest prefix and UUID.
func (p geminiNativeHTTPBackend) DigestSessionKey(prefix, id string) string {
	return session.GenerateGeminiDigestSessionKey(prefix, id)
}

// BindSticky writes the selected provider association through the bound
// session callback. The callback owns persistence and expiration; native
// admission can continue after an unsuccessful best-effort binding write.
func (p geminiNativeHTTPBackend) BindSticky(ctx context.Context, groupID *int64, key string, providerID int64) error {
	return p.gemini.BindSticky(ctx, groupID, key, providerID)
}
