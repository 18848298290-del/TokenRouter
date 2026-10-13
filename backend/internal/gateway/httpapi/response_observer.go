package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	rawwire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Upstream response declarations are captured before client-visible model
// restoration. Each request binding points at mutable attempt evidence, while
// completion keeps the outbound tier separate from the observed execution tier;
// recovery can clear model evidence without clearing the same observer's tier
// history.

// The context key associates current attempt observation with HTTP readers and
// completion capture; creating a fresh observer replaces this binding.
const (
	upstreamResponseModelObserverContextKey = "upstream_response_model_observer"
)

// BeginUpstreamResponseModelObservation creates a fresh model and tier observer
// for an attempt. A nonnil Gin context replaces its previous binding; nil still
// returns the new observer. Callers serialize access to this mutable
// observation state.
func BeginUpstreamResponseModelObservation(c *gin.Context) *forwardcore.ResponseObserver {
	observer := &forwardcore.ResponseObserver{}
	if c != nil {
		c.Set(upstreamResponseModelObserverContextKey, observer)
	}
	return observer
}

// UpstreamResponseModelObserverFromContext returns the currently bound observer
// for this request. Missing contexts, absent values and values of another type
// return nil; the returned pointer shares mutable state with stream and
// completion consumers.
func UpstreamResponseModelObserverFromContext(c *gin.Context) *forwardcore.ResponseObserver {
	if c == nil {
		return nil
	}
	value, ok := c.Get(upstreamResponseModelObserverContextKey)
	if !ok {
		return nil
	}
	observer, _ := value.(*forwardcore.ResponseObserver)
	return observer
}

// ObservedUpstreamResponseServiceTier reads the observer's preferred execution
// tier for completion capture. A missing observer yields empty through its
// nil-safe method; the value reflects terminal precedence and unresolved
// nonterminal conflicts handled by ResponseObserver.
func ObservedUpstreamResponseServiceTier(c *gin.Context) string {
	return UpstreamResponseModelObserverFromContext(c).ServiceTier()
}

// ResolvedOpenAIUpstreamServiceTierFromObserver returns the supplied outbound
// tier pointer unchanged. The observer argument does not alter this result;
// completion applies credential-specific interpretation to its separate
// observed-tier evidence, including when the outbound pointer is nil.
func ResolvedOpenAIUpstreamServiceTierFromObserver(_ *forwardcore.ResponseObserver, outboundBodyTier *string) *string {
	return outboundBodyTier
}

// ResolvedOpenAIUpstreamServiceTier returns the outbound tier pointer through
// the shared resolver. Request observation remains independent, so missing or
// contradictory upstream evidence cannot create a requested tier at this HTTP
// call site.
func ResolvedOpenAIUpstreamServiceTier(c *gin.Context, outboundBodyTier *string) *string {
	return ResolvedOpenAIUpstreamServiceTierFromObserver(UpstreamResponseModelObserverFromContext(c), outboundBodyTier)
}

// ObserveOpenAIServiceTierInContext observes model and eligible tier
// declarations for the current attempt. Missing context or empty payload skips
// observation; an absent observer is created. Callers serialize updates and
// supply the event classification used by the output adapter.
func ObserveOpenAIServiceTierInContext(c *gin.Context, payload []byte, eventType string) {
	if c == nil || len(payload) == 0 {
		return
	}
	observer := UpstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = BeginUpstreamResponseModelObservation(c)
	}
	observer.ObserveOpenAI(payload, eventType)
}

// ObserveOpenAISSEBody submits each parsed SSE frame to the request observer.
// Nil contexts and blank bodies skip processing; frame parsing and event
// classification use the shared OpenAI SSE iterator before model and tier
// observation.
func ObserveOpenAISSEBody(c *gin.Context, body string) {
	if c == nil || strings.TrimSpace(body) == "" {
		return
	}
	rawwire.ForEachOpenAISSEFrame(body, func(eventType string, payload []byte) {
		ObserveOpenAIServiceTierInContext(c, payload, eventType)
	})
}

// captureResponseModel fills a blank result model from current upstream
// observation. Nil results and models already supplied by execution are
// retained; a missing observer supplies empty evidence through its nil-safe
// Model method.
func captureResponseModel(c *gin.Context, result *forwardcore.OpenAIResult) {
	if result != nil && result.UpstreamResponseModel == "" {
		result.UpstreamResponseModel = UpstreamResponseModelObserverFromContext(c).Model()
	}
}

// resetResponseModel clears model evidence before a recovery response in the
// same observer. Existing tier history stays available because ResetModel
// clears only the model; absent observers are initialized, while unrelated
// attempts need fresh observation state.
func resetResponseModel(c *gin.Context) {
	if observer := UpstreamResponseModelObserverFromContext(c); observer != nil {
		observer.ResetModel()
	} else {
		BeginUpstreamResponseModelObservation(c)
	}
}
