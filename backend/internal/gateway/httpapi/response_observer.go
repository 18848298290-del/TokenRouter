package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	rawwire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

const (
	upstreamResponseModelObserverContextKey = "upstream_response_model_observer"
)

func BeginUpstreamResponseModelObservation(c *gin.Context) *forwardcore.ResponseObserver {
	observer := &forwardcore.ResponseObserver{}
	if c != nil {
		c.Set(upstreamResponseModelObserverContextKey, observer)
	}
	return observer
}

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

func ObservedUpstreamResponseServiceTier(c *gin.Context) string {
	return UpstreamResponseModelObserverFromContext(c).ServiceTier()
}

// ResolvedOpenAIUpstreamServiceTierFromObserver returns the outbound tier unchanged.
// Usage recording interprets the observed tier with the credential protocol; observation cannot promote the request tier.
func ResolvedOpenAIUpstreamServiceTierFromObserver(_ *forwardcore.ResponseObserver, outboundBodyTier *string) *string {
	return outboundBodyTier
}

func ResolvedOpenAIUpstreamServiceTier(c *gin.Context, outboundBodyTier *string) *string {
	return ResolvedOpenAIUpstreamServiceTierFromObserver(UpstreamResponseModelObserverFromContext(c), outboundBodyTier)
}

// ObserveOpenAIServiceTierInContext records an OpenAI event in the request observer
// for completion capture of the response model and service tier.
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

// ObserveOpenAISSEBody records the service tier from each Responses SSE frame.
func ObserveOpenAISSEBody(c *gin.Context, body string) {
	if c == nil || strings.TrimSpace(body) == "" {
		return
	}
	rawwire.ForEachOpenAISSEFrame(body, func(eventType string, payload []byte) {
		ObserveOpenAIServiceTierInContext(c, payload, eventType)
	})
}

// captureResponseModel fills an unset result model from the observer before the attempt returns.
func captureResponseModel(c *gin.Context, result *forwardcore.OpenAIResult) {
	if result != nil && result.UpstreamResponseModel == "" {
		result.UpstreamResponseModel = UpstreamResponseModelObserverFromContext(c).Model()
	}
}

// resetResponseModel clears the previous attempt's model before observing a recovery response.
func resetResponseModel(c *gin.Context) {
	if observer := UpstreamResponseModelObserverFromContext(c); observer != nil {
		observer.ResetModel()
	} else {
		BeginUpstreamResponseModelObservation(c)
	}
}
