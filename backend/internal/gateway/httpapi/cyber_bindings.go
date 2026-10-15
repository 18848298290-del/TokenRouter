package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
)

// cyber_bindings.go binds HTTP cyber markers, session lookups, and terminal
// error output to the cyber handler.
// HTTP cyber handling reads refusal evidence from request context and asks the
// session core to match blocked identity. Its output callbacks stop heartbeat
// generation and record intended error status before attempting a terminal
// Responses event on an already framed stream.

// cyberHTTPBackend connects request markers and output to shared session-block operations.
type cyberHTTPBackend struct {
	core *session.CyberBlocks // shared session block lookup
}

// NewBoundCyberHandler binds session blocking, moderation, and completion services.
// Assembly manages the supplied runtime resources. The HTTP backend reads and
// writes request observations, while the session core and moderation dependencies
// retain their own storage and policy decisions.
func NewBoundCyberHandler(core *session.CyberBlocks, moderator ModerationPort, runtime moderationflow.Runtime) *CyberHandler {
	return NewCyberHandler(cyberHTTPBackend{core}, moderator, GatewayModerationEndpoints{}, runtime)
}

// Available reports whether assembly supplied a session-block core for this backend.
func (p cyberHTTPBackend) Available() bool { return p.core != nil }

// Enabled reads the session core's current runtime enablement setting.
// The core treats nil or missing settings as disabled. The returned duration
// is unused here because lookup and block writing resolve their own runtime lifetime.
func (p cyberHTTPBackend) Enabled(ctx context.Context) bool {
	enabled, _ := p.core.Runtime(ctx)
	return enabled
}

// Find resolves a blocked key using request identity and source metadata.
// It delegates lazy explicit identity and scoped transcript matching through
// FindBlockedCyberSession; an empty result leaves the HTTP request's block check unclaimed.
func (p cyberHTTPBackend) Find(ctx context.Context, id int64, c *gin.Context, body []byte) string {
	return FindBlockedCyberSession(ctx, p.core, id, c, body)
}

// Mark returns a value copy of the current refusal evidence when present.
// A missing marker returns nil. Its fields are strings and token counts,
// so subsequent struct-field changes on this copy leave the saved marker unchanged.
func (p cyberHTTPBackend) Mark(c *gin.Context) *moderationflow.Mark {
	m := GetOpsCyberPolicy(c)
	if m == nil {
		return nil
	}
	v := *m
	return &v
}

// StopKeepalive stops the heartbeat controller and reports its first-beat framing state.
// Controller stopping waits through its active write but leaves writer restoration
// to the request cleanup. The caller uses this result before selecting failure output.
func (p cyberHTTPBackend) StopKeepalive(c *gin.Context) bool {
	return StopOpenAICompactSSEKeepaliveCommitted(c)
}

// MarkStream records the intended status and message for committed-stream error observation.
func (p cyberHTTPBackend) MarkStream(c *gin.Context, k, m string, status int) {
	MarkOpsStreamError(c, k, m, status)
}

// FailedSSE attempts a Responses terminal event using the request's ID and model.
// Its boolean reports whether the output helper claimed the event path.
// Callers stop further response writing on success and select another error form on false.
func (p cyberHTTPBackend) FailedSSE(c *gin.Context, k, m string) bool {
	return WriteResponsesFailedSSE(c, k, "", m, ErrorRequestID(c), ErrorRequestModel(c))
}

// UpstreamEndpoint reads the runtime endpoint override or the platform-derived observation value.
func (p cyberHTTPBackend) UpstreamEndpoint(c *gin.Context, platform string) string {
	return GetUpstreamEndpoint(c, platform)
}
