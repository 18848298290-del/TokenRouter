package httpapi

import (
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

// A converted Responses failure can apply provider health before the output
// adapter constructs its failover error. The HTTP context holds a shared
// ResponseFailureEffects slot so that handoff can reuse the applied status and
// scheduling decision.
//
// Context initialization has a second lookup under a package mutex, preventing
// concurrent creators from publishing distinct slots for the same request. The
// slot itself synchronizes Store and Consume and retains the latest payload
// after its presence flag is cleared. Consumers therefore check the third
// return value before reusing stored effects; a later producer can publish
// another observation, and this request state does not enumerate all failures.

// responseFailureEffectsKey identifies the request slot carrying
// already-applied provider health evidence
// from conversion failure handling into subsequent failover construction.
const responseFailureEffectsKey = "gateway_response_failure_effects"

// responseFailureEffectsInit serializes lazy slot publication after callers
// miss the first context lookup,
// allowing concurrent request helpers to retain one effects slot.
var responseFailureEffectsInit sync.Mutex

// responseFailureEffects returns the request's applied-health slot and
// optionally creates it. Nil contexts return nil, and create false leaves
// missing or differently typed entries untouched. Existing correctly typed
// entries are shared with producers and consumers.
//
// Creation rechecks the context under responseFailureEffectsInit before
// publishing an empty slot, so another creator's value wins. Existing typed
// nil entries return nil even when creation is requested; callers reserve this
// private key for the initializer. That mutex
// governs initialization; the ResponseFailureEffects methods govern each
// stored observation and its consumption after publication. Callers use the
// presence result from Consume to distinguish pending evidence from retained
// payload values.
func responseFailureEffects(c *gin.Context, create bool) *requeststate.ResponseFailureEffects {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(responseFailureEffectsKey); ok {
		if s, ok := v.(*requeststate.ResponseFailureEffects); ok {
			return s
		}
	}
	if !create {
		return nil
	}
	// Another creator may publish between the first lookup and this lock; recheck
	// while serialized so both failure paths use the same consumable effects slot.
	responseFailureEffectsInit.Lock()
	defer responseFailureEffectsInit.Unlock()
	if v, ok := c.Get(responseFailureEffectsKey); ok {
		if s, ok := v.(*requeststate.ResponseFailureEffects); ok {
			return s
		}
	}
	s := &requeststate.ResponseFailureEffects{}
	c.Set(responseFailureEffectsKey, s)
	return s
}

// MarkOpenAIResponseFailureEffects publishes the applied health status and
// scheduling result for this request. Nil contexts are ignored; a later call
// replaces unconsumed evidence, and the failover builder checks the slot's
// presence before reusing it. Keep the private context slot owned by its
// initializer; a typed nil stored there cannot accept Store.
func MarkOpenAIResponseFailureEffects(c *gin.Context, status int, disabled bool) {
	if c != nil {
		responseFailureEffects(c, true).Store(status, disabled)
	}
}

// consumeOpenAIResponseFailureEffects consumes the latest pending
// applied-health observation for failover construction. Missing slots return
// zero, false, false; after consumption, retained status and disabled values
// are accompanied by false presence and must not be treated as new evidence.
func consumeOpenAIResponseFailureEffects(c *gin.Context) (int, bool, bool) {
	return responseFailureEffects(c, false).Consume()
}
