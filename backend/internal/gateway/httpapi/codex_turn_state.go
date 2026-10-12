package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Codex clients echo an opaque turn-state header across requests, but the state
// can become inappropriate when routing selects another provider. Ownership is
// indexed by API key and the original client session, recording the most recent
// provider that supplied state for that pair. The index contains provider IDs,
// so filtering depends on recorded session ownership rather than comparing opaque blobs.
//
// Response state can be staged before an attempt is chosen for output. Staging
// changes headers alone; Relay or Commit records ownership at the caller's
// delivery point. Guard removes a client's echoed value when known ownership
// disagrees with the next provider, while unknown or expired ownership permits
// the value to pass through for upstream handling.

// CodexTurnStateHeader names the opaque state value relayed from upstream output
// and checked against remembered session ownership on later requests.
const CodexTurnStateHeader = "x-codex-turn-state"

// CodexTurnStateHeaders stages response state and records session ownership at caller-selected delivery points.
// @project-doc docs/interfaces/openai_upstream.md#openai_protocol_dispatch
type CodexTurnStateHeaders struct {
	// Origins is the shared session-owner index. A nil index ignores records
	// and reports unknown ownership, allowing echoed state to pass through Guard.
	Origins *session.CodexTurnOrigins
	// TTL supplies the lifetime passed to Origins.Record. A nil callback uses
	// one hour; returned durations are passed through without adapter validation.
	TTL func() time.Duration
}

// codexTurnStateSeed identifies the API-key and original client-session pair.
// Missing context, request, or session yields an empty seed and skips ownership
// tracking. A missing API-key value contributes the zero ID returned by APIKeyIDFromContext.
//
// Session extraction runs on inbound headers before provider isolation rewrites
// them, using the upstream helper's hyphenated-header preference. Decimal key ID,
// a null separator, and the session form the lookup key, keeping equal client
// sessions on different API keys in separate ownership entries.
func codexTurnStateSeed(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	id := openai.ExtractClientSessionID(c.Request.Header)
	if id == "" {
		return ""
	}
	return strconv.FormatInt(APIKeyIDFromContext(c), 10) + "\x00" + id
}

// ExtractCodexTurnState reads and trims the opaque turn-state header value.
// A nil map or missing header yields an empty string, which staging and relay
// interpret as absence rather than a value to record.
func ExtractCodexTurnState(headers http.Header) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get(CodexTurnStateHeader))
}

// StageCodexTurnState copies state headers without recording ownership, allowing
// the attempt to be discarded before output.
// A nil destination pointer is ignored. Missing or blank state removes any
// previous staged value; a present value initializes the map if needed and
// stores its trimmed canonical header spelling.
func StageCodexTurnState(dst *http.Header, headers http.Header) {
	if dst == nil {
		return
	}
	canonical := http.CanonicalHeaderKey(CodexTurnStateHeader)
	value := ExtractCodexTurnState(headers)
	if value == "" {
		// A later attempt may omit state after an earlier one supplied it.
		// Clear the staged header so subsequent commitment cannot publish that earlier attempt's value.
		if *dst != nil {
			dst.Del(canonical)
		}
		return
	}
	if *dst == nil {
		*dst = http.Header{}
	}
	dst.Set(canonical, value)
}

// note records the issuing provider for the request's key-session ownership seed.
// Nil adapters, missing targets, nonpositive provider IDs, or absent session seeds
// are ignored. TTL defaults to one hour and Origins handles storage and expiry.
func (s *CodexTurnStateHeaders) note(c *gin.Context, target *gatewayadapter.ExecutionProvider) {
	if s == nil || target == nil || target.Record.ID <= 0 {
		return
	}
	seed := codexTurnStateSeed(c)
	if seed == "" {
		return
	}
	ttl := time.Hour
	if s.TTL != nil {
		ttl = s.TTL()
	}
	s.Origins.Record(seed, target.Record.ID, ttl)
}

// Relay places upstream turn state in the client response headers and records ownership.
// Missing state removes an earlier value from the current header map and records
// nothing. The selected provider is remembered after a nonempty header is set.
//
// Delivering that value as an HTTP header requires calling Relay before response
// headers are committed. Ownership is recorded even if the caller has already
// committed headers and the client cannot receive the late value. The helper
// does not write a body or confirm network delivery.
func (s *CodexTurnStateHeaders) Relay(c *gin.Context, target *gatewayadapter.ExecutionProvider, headers http.Header) {
	if c == nil || c.Writer == nil {
		return
	}
	canonical := http.CanonicalHeaderKey(CodexTurnStateHeader)
	value := ExtractCodexTurnState(headers)
	if value == "" {
		c.Writer.Header().Del(canonical)
		return
	}
	// Remember ownership after installing the response header. Output adapters call
	// this when relaying the selected attempt so discarded staged headers do not claim ownership.
	c.Writer.Header().Set(canonical, value)
	s.note(c, target)
}

// Commit records ownership for staged headers that the caller selected for output.
// Nil maps or blank state are ignored. It performs no header or body write,
// so callers invoke it after their own staging decision makes the state eligible for delivery.
func (s *CodexTurnStateHeaders) Commit(c *gin.Context, target *gatewayadapter.ExecutionProvider, headers http.Header) {
	if headers == nil || strings.TrimSpace(headers.Get(CodexTurnStateHeader)) == "" {
		return
	}
	s.note(c, target)
}

// Guard removes state known to belong to another provider. Same-provider, unknown, and expired ownership allow replay.
// Nil adapters, missing headers or targets, and absent key-session seeds leave
// the supplied headers unchanged. A nonempty value is checked against the most
// recently recorded owner for that request's key and original client session.
//
// The index does not identify individual opaque state values. Its known owner
// protects provider switching for this session, while missing or expired entries
// defer validation to upstream. Deleting a foreign header changes the caller's
// outgoing map before forwarding; callers supply the original inbound context
// so lookup uses the same session seed as response ownership recording.
func (s *CodexTurnStateHeaders) Guard(c *gin.Context, target *gatewayadapter.ExecutionProvider, headers http.Header) {
	if s == nil || headers == nil || target == nil || strings.TrimSpace(headers.Get(CodexTurnStateHeader)) == "" {
		return
	}
	seed := codexTurnStateSeed(c)
	if seed == "" {
		return
	}
	if id, ok := s.Origins.Owner(seed); ok && id != target.Record.ID {
		// A remembered owner belongs to another provider for this key-session pair.
		// Remove the echoed blob before forwarding so the selected provider receives no foreign state.
		headers.Del(CodexTurnStateHeader)
	}
}
