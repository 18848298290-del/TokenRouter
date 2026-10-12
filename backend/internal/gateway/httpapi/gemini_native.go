package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolgemini "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

var GeminiCLITmpDirRegex = regexp.MustCompile(`/\.gemini/tmp/([A-Fa-f0-9]{64})`)

type GeminiNativeOptions struct{ MaxSwitches int }

type GeminiNativeBackend interface {
	Access(*gin.Context) (*apikey.APIKey, bool)
	HasForcedPlatform(*gin.Context) bool
	SafeModelSegment(string) bool
	ObserveRequest(*gin.Context, string, bool)
	ObserveEndpoint(*gin.Context, bool)
	Moderate(*gin.Context, *zap.Logger, *apikey.APIKey, authctx.AuthSubject, string, []byte) *moderation.Decision
	Plan(context.Context, *apikey.APIKey, string) routing.RoutePlan
	BindPlan(*gin.Context, routing.RoutePlan)
	BindErrors(*gin.Context)
	Eligibility(context.Context, *apikey.APIKey, *billing.UserSubscription) error
	Isolate(context.Context, *apikey.APIKey, int64, string) error
	CachedSession(context.Context, *int64, string) (int64, error)
	Prefetch(*gin.Context, int64, int64)
	DigestChain(*protocolgemini.GeminiRequest) string
	PrefixHash(int64, int64, string, string, string, string) string
	FindSession(context.Context, int64, string, string) (string, int64, string, bool)
	DigestSessionKey(string, string) string
	BindSticky(context.Context, *int64, string, int64) error
	FailoverObservation(context.Context, string, map[string]any)
}
type GeminiNativeHandler struct {
	executor execution.Executor
	RequestLifetime

	options     GeminiNativeOptions
	backend     GeminiNativeBackend
	prompt      MessagesPrompt
	concurrency *ConcurrencyHelper
	newID       func() string
}

type GeminiPathParseError struct{ message string }

func NewGeminiNativeHandler(options GeminiNativeOptions, backend GeminiNativeBackend, prompt MessagesPrompt, concurrency *ConcurrencyHelper, newID func() string, executor execution.Executor) *GeminiNativeHandler {
	return &GeminiNativeHandler{executor: executor, options: options, backend: backend, prompt: prompt, concurrency: concurrency, newID: newID}
}

// WriteGoogleError writes a Google-format JSON error.
func WriteGoogleError(c *gin.Context, status int, message string) {
	status, message = modelAttributeError(c, status, message)
	c.JSON(status, gin.H{"error": gin.H{"code": status, "message": message, "status": HTTPStatusToGoogleStatus(status)}})
}

func geminiNativeIsolationError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, session.ErrSessionIsolationConflict) {
		WriteGoogleError(c, 403, session.SessionIsolationConflictMessage)
	} else {
		WriteGoogleError(c, 503, "Service temporarily unavailable")
	}
	return true
}

func (e *GeminiPathParseError) Error() string { return e.message }

// @project-doc docs/interfaces/gemini_upstream.md#gemini_protocol_dispatch
func (h *GeminiNativeHandler) GeminiV1BetaModels(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "google")
	if !accepted {
		return
	}
	defer done()

	apiKey, ok := h.backend.Access(c)
	if !ok || apiKey == nil {
		WriteGoogleError(c, http.StatusUnauthorized, "Invalid API key")
		return
	}
	authSubject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		WriteGoogleError(c, http.StatusInternalServerError, "User context not found")
		return
	}
	reqLog := RequestLogger(
		c,
		"handler.gemini_v1beta.models",
		zap.Int64("user_id", authSubject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	// Admission checks the group protocol; provider selection determines the upstream platform.
	if apiKey.Group == nil || !apiKey.Group.AllowsClientProtocol(protocol.ProtocolGeminiGenerateContent) {
		WriteGoogleError(c, http.StatusForbidden, "This group does not allow Gemini GenerateContent requests")
		return
	}

	modelName, action, err := ParseGeminiModelAction(strings.TrimPrefix(c.Param("modelAction"), "/"))
	if err != nil {
		WriteGoogleError(c, http.StatusNotFound, err.Error())
		return
	}
	// The model becomes part of /v1beta/models/{model}:{action}, so validate it as a
	// URL path segment before forwarding.
	if !h.backend.SafeModelSegment(modelName) {
		WriteGoogleError(c, http.StatusBadRequest, "Invalid model in URL")
		return
	}

	stream := action == "streamGenerateContent"
	reqLog = reqLog.With(zap.String("model", modelName), zap.String("action", action), zap.Bool("stream", stream))

	body, err := ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := compatibleMaxBytesError(err); ok {
			WriteGoogleError(c, http.StatusRequestEntityTooLarge, BodyTooLargeMessage(maxErr.Limit))
			return
		}
		WriteGoogleError(c, http.StatusBadRequest, "Failed to read request body")
		return
	}
	if len(body) == 0 {
		WriteGoogleError(c, http.StatusBadRequest, "Request body is empty")
		return
	}

	apiKey, err = resolveClientGroupForRequest(c, h.backend, apiKey, protocol.ProtocolGeminiGenerateContent)
	if err != nil {
		writeClientGroupFallbackError(c, err, func(c *gin.Context, status int, _, message string) { WriteGoogleError(c, status, message) })
		return
	}
	h.backend.ObserveRequest(c, modelName, stream)
	h.backend.ObserveEndpoint(c, stream)
	// Apply prompt replacement before moderation, session hashing, and forwarding so they use the same body.
	body = h.prompt.ApplyUserPromptReplacementToBody(c.Request.Context(), body, "gemini")

	if decision := h.backend.Moderate(c, reqLog, apiKey, authSubject, modelName, body); decision != nil && decision.Blocked {
		WriteGoogleError(c, ModerationHTTPStatus(decision), decision.Message)
		return
	}

	// Capture the effective group and mapping in the route plan.
	groupMappingRoutePlan := h.backend.Plan(c.Request.Context(), apiKey, modelName)
	groupMapping := groupMappingRoutePlan.Mapping()
	h.backend.BindPlan(c, groupMappingRoutePlan)
	reqModel := modelName // Client model before group mapping.
	if groupMapping.Mapped {
		modelName = groupMapping.MappedModel
	}

	subscription, _ := SubscriptionFromContext(c)

	// Native Gemini requests must not receive Claude heartbeat frames.
	geminiConcurrency := NewConcurrencyHelper(h.concurrency.Service(), SSEPingFormatNone, 0)

	streamStarted := false
	h.backend.BindErrors(c)
	userReleaseFunc, err := geminiConcurrency.AcquireUserSlotWithWait(c, authSubject.UserID, authSubject.Concurrency, stream, &streamStarted)
	if err != nil {
		reqLog.Warn("gemini.user_slot_acquire_failed", zap.Error(err))
		WriteGoogleError(c, http.StatusTooManyRequests, err.Error())
		return
	}
	// Release the user slot on request cancellation, including connection loss.
	userReleaseFunc = scheduler.WrapRelease(c.Request.Context(), scheduler.ReleaseOnCancel, userReleaseFunc)
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// Recheck funding eligibility after waiting for the user slot.
	if err := h.backend.Eligibility(c.Request.Context(), apiKey, subscription); err != nil {
		reqLog.Info("gemini.billing_eligibility_check_failed", zap.Error(err))
		status, _, message, retryAfter := BillingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		WriteGoogleError(c, status, message)
		return
	}

	// Prefer Gemini CLI identity from the privileged user ID and temporary-directory hash.
	sessionHash := ExtractGeminiCLISessionHash(c, body)
	explicitGeminiCLISession := sessionHash != ""
	if sessionHash == "" {
		// Other clients use the shared session hash derived from request content and metadata.
		parsedReq, _ := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(body), capability.PlatformGemini)
		if parsedReq != nil {
			parsedReq.SessionContext = &requeststate.SessionContext{
				ClientIP:  clientip.GetClientIP(c),
				UserAgent: c.GetHeader("User-Agent"),
				APIKeyID:  apiKey.ID,
			}
		}
		sessionHash = session.GenerateSessionHash(parsedReq, slog.Info)
	}
	sessionKey := sessionHash
	if sessionHash != "" {
		sessionKey = "gemini:" + sessionHash
	}
	if explicitGeminiCLISession {
		if err := h.backend.Isolate(c.Request.Context(), apiKey, authSubject.UserID, sessionKey); geminiNativeIsolationError(c, err) {
			return
		}
	}

	// Read the bound provider so later attempts can detect a provider switch.
	var sessionBoundProviderID int64
	if sessionKey != "" {
		sessionBoundProviderID, _ = h.backend.CachedSession(c.Request.Context(), apiKey.GroupID, sessionKey)
		if sessionBoundProviderID > 0 {
			prefetchedGroupID := int64(0)
			if apiKey.GroupID != nil {
				prefetchedGroupID = *apiKey.GroupID
			}
			h.backend.Prefetch(c, sessionBoundProviderID, prefetchedGroupID)
		}
	}

	// When there is no provider binding, try matching the conversation digest chain.
	var geminiDigestChain string
	var geminiPrefixHash string
	var geminiSessionUUID string
	var matchedDigestChain string
	useDigestFallback := sessionBoundProviderID == 0

	if useDigestFallback {
		var geminiReq protocolgemini.GeminiRequest
		if err := json.Unmarshal(body, &geminiReq); err == nil && len(geminiReq.Contents) > 0 {
			geminiDigestChain = h.backend.DigestChain(&geminiReq)
			if geminiDigestChain != "" {
				userAgent := c.GetHeader("User-Agent")
				clientIP := clientip.GetClientIP(c)
				geminiPrefixHash = h.backend.PrefixHash(
					authSubject.UserID,
					apiKey.ID,
					clientIP,
					userAgent,
					"",
					modelName,
				)

				foundUUID, foundProviderID, foundMatchedChain, found := h.backend.FindSession(
					c.Request.Context(),
					geminiGroupID(apiKey.GroupID),
					geminiPrefixHash,
					geminiDigestChain,
				)
				if found {
					matchedDigestChain = foundMatchedChain
					sessionBoundProviderID = foundProviderID
					geminiSessionUUID = foundUUID
					reqLog.Info("gemini.digest_fallback_matched",
						zap.String("session_uuid_prefix", GeminiShortPrefix(foundUUID, 8)),
						zap.Int64("provider_id", foundProviderID),
						zap.String("digest_chain", TruncateGeminiDigestChain(geminiDigestChain)),
					)

					// Create a key from the prefix hash and matched UUID when no explicit key exists,
					// so provider selection can reuse the matched provider through the sticky binding.
					if sessionKey == "" {
						sessionKey = h.backend.DigestSessionKey(geminiPrefixHash, foundUUID)
					}
					_ = h.backend.BindSticky(c.Request.Context(), apiKey.GroupID, sessionKey, foundProviderID)
				} else {
					geminiSessionUUID = h.newID()
					// Give new digest sessions a key for subsequent sticky requests.
					if sessionKey == "" {
						sessionKey = h.backend.DigestSessionKey(geminiPrefixHash, geminiSessionUUID)
					}
				}
			}
		}
	}

	hasBoundSession := sessionKey != "" && sessionBoundProviderID > 0

	request := execution.Request{
		Hints:       requeststate.ExecutionHintsFromContext(c.Request.Context()),
		Routing:     requeststate.RoutingStateFromContext(c.Request.Context()),
		Route:       groupMappingRoutePlan,
		UserID:      authSubject.UserID,
		Concurrency: authSubject.Concurrency,
		Stream:      stream,
		Body:        body,
		Model:       reqModel,
		Funding: execution.FundingState{
			Key:          apiKey,
			Subscription: subscription,
		},
		SessionHash: sessionKey,
		Text: execution.TextState{
			Kind:               execution.TextNativeGemini,
			BoundProviderID:    sessionBoundProviderID,
			HasBoundSession:    hasBoundSession,
			GeminiModel:        modelName,
			Action:             action,
			UseDigestFallback:  useDigestFallback,
			DigestChain:        geminiDigestChain,
			PrefixHash:         geminiPrefixHash,
			SessionUUID:        geminiSessionUUID,
			MatchedDigestChain: matchedDigestChain,
			SignatureState: requeststate.GeminiSignatureState{
				BoundProviderID: sessionBoundProviderID,
			},
			Mapping: groupMapping,
		},
	}
	output := &MessagesOutput{
		ResponseSink:  ResponseSink{Writer: c.Writer},
		HTTP:          c,
		Log:           reqLog,
		StreamStarted: &streamStarted,
		Concurrency:   geminiConcurrency,
	}
	_, _ = h.executor.Execute(c.Request.Context(), request, output)
}

func ParseGeminiModelAction(rest string) (model string, action string, err error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", &GeminiPathParseError{"missing path"}
	}

	// Standard syntax: {model}:{action}.
	if i := strings.Index(rest, ":"); i > 0 && i < len(rest)-1 {
		return rest[:i], rest[i+1:], nil
	}

	// Compatibility syntax: {model}/{action}.
	if i := strings.Index(rest, "/"); i > 0 && i < len(rest)-1 {
		return rest[:i], rest[i+1:], nil
	}

	return "", "", &GeminiPathParseError{"invalid model action path"}
}

func ExtractGeminiCLISessionHash(c *gin.Context, body []byte) string {
	match := GeminiCLITmpDirRegex.FindSubmatch(body)
	if len(match) < 2 {
		return "" // No CLI-specific session identity; the caller can use shared session detection.
	}
	tmpDirHash := string(match[1])

	privilegedUserID := strings.TrimSpace(c.GetHeader("x-gemini-api-privileged-user-id"))

	if privilegedUserID != "" {
		// Combine user identity with the directory hash to scope the session.
		combined := privilegedUserID + ":" + tmpDirHash
		hash := sha256.Sum256([]byte(combined))
		return hex.EncodeToString(hash[:])
	}

	// Without a privileged user ID, use the directory hash directly.
	return tmpDirHash
}

func TruncateGeminiDigestChain(chain string) string {
	if len(chain) <= 50 {
		return chain
	}
	return chain[:50] + "..."
}

func GeminiShortPrefix(value string, n int) string {
	if n <= 0 || len(value) <= n {
		return value
	}
	return value[:n]
}

func geminiGroupID(groupID *int64) int64 {
	if groupID == nil {
		return 0
	}
	return *groupID
}
