package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// cyber_policy_marker.go retains the first upstream cyber-policy observation
// for an HTTP request or WebSocket turn.
// Upstream policy evidence can appear in several events from one request or
// WebSocket turn. Sequential publication retains the first saved marker until
// reset, so later events cannot replace its captured token counts or explanation
// while completion uses that turn's observation.
//
// Publication copies the input mark and normalizes its code and text before
// storing a pointer in request context. Lookup returns that same pointer, so
// consumers coordinate mutations and turn reset with evidence publication.
// Event recognition remains separate from storage: a recognized event can
// return true even when no context exists or earlier evidence remains saved.

// opsCyberPolicyKey identifies the saved upstream evidence shared by HTTP error
// observation and WebSocket turn completion until the turn's marker is cleared.
const opsCyberPolicyKey = "ops_cyber_policy"

// MarkOpsCyberPolicy stores a value copy of the first marker for this request or turn.
// Nil contexts and an existing nonnil marker leave storage unchanged; a typed
// nil reset permits publication again. The code is set to cyber_policy, and
// surrounding whitespace is removed from Message and Body.
//
// Callers serialize publication with reset because the read and write are
// separate Gin operations. Consumers receive the saved pointer directly, so
// changes to its fields also need coordination with current-turn completion.
func MarkOpsCyberPolicy(c *gin.Context, mark moderationflow.Mark) {
	// Later events can carry different usage. Inspect the saved marker before
	// storing this event so completion retains the evidence already chosen for
	// the current turn by the serialized publisher.
	if c == nil || GetOpsCyberPolicy(c) != nil {
		return
	}
	mark.Code = "cyber_policy"
	mark.Message = strings.TrimSpace(mark.Message)
	mark.Body = strings.TrimSpace(mark.Body)
	c.Set(opsCyberPolicyKey, &mark)
}

// GetOpsCyberPolicy returns the request or turn's stored evidence pointer.
// Nil contexts, absent or mistyped values, and typed nil markers return nil.
// A returned marker is shared with other request consumers, so callers coordinate
// any mutation with the current turn's publication and completion.
func GetOpsCyberPolicy(c *gin.Context) *moderationflow.Mark {
	if c == nil {
		return nil
	}
	if value, ok := c.Get(opsCyberPolicyKey); ok {
		if mark, ok := value.(*moderationflow.Mark); ok && mark != nil {
			return mark
		}
	}
	return nil
}

// ClearOpsCyberPolicy stores a typed nil so a later turn can publish fresh evidence.
// A nil context is ignored. Callers finish consumers of the current turn before
// resetting the marker, keeping subsequent completion from reading a different turn's observation.
func ClearOpsCyberPolicy(c *gin.Context) {
	if c != nil {
		c.Set(opsCyberPolicyKey, (*moderationflow.Mark)(nil))
	}
}

// MarkOpenAICyberPolicyEvent recognizes provider refusal evidence and attempts to save it.
// False means no policy marker was parsed. True reports recognition even when
// the context is nil or an earlier saved marker prevents replacement; usage
// values are captured by the provider parser before publication.
func MarkOpenAICyberPolicyEvent(c *gin.Context, payload []byte, upstreamStatus int, usage *openai.ForwardUsage) bool {
	mark := gatewayadapter.ParseOpenAICyberPolicyEvent(payload, upstreamStatus, usage)
	if mark == nil {
		return false
	}
	MarkOpsCyberPolicy(c, *mark)
	return true
}
