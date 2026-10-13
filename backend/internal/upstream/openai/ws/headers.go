package ws

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// WebSocket handshake identity includes selected authentication, isolated
// sessions, device headers, and beta features used by connection compatibility.
// Agent Identity leaves bearer Authorization absent here for the selected
// dial-time authentication path, while ordinary credentials supply the prepared
// token.
//
// Codex session isolation runs before provider scoping and precomputed attempt
// fingerprints. Final User-Agent selection precedes originator pairing, and
// allowed provider overrides run before beta negotiation, routing hints, and
// diagnostics. The resolved turn state and metadata are written before those
// identity transformations. This ordering lets the pool compare the handshake
// features and fingerprint identity actually sent when deciding whether an
// existing connection can be reused.

// WSTurnStateHeader names the opaque continuation state written into a prepared
// handshake after the gateway has selected its valid source for this turn.
const WSTurnStateHeader = "x-codex-turn-state"

// WSHeaderOptions supplies selected WebSocket authentication, session, and
// handshake identity sources.
type WSHeaderOptions struct {
	openai.ResponsesRequestOptions        // shared Responses header callback sources
	AgentIdentity                  bool   // agent assertion authentication selection
	Token                          string `json:"-"` // prepared bearer access token
	// TurnState carries opaque continuation state, and TurnMetadata carries its
	// JSON identity text. Both are trimmed and written before credential scoping
	// and fingerprint transformation for this selected handshake.
	TurnState, TurnMetadata string
	ResolveSession          func() (string, string) // required session/conversation identity reader
	InboundHeaders          func() http.Header      // required stable inbound-header reader
	UserAgent               func() string           // required preferred User-Agent reader
	ApplyWSUserAgent        func(http.Header)       // required routed User-Agent writer
}

// String returns a fixed WebSocket option label that omits credentials.
func (WSHeaderOptions) String() string { return "openai websocket header options" }

// GoString returns the redacted WebSocket option label for Go-syntax
// formatting.
func (v WSHeaderOptions) GoString() string { return v.String() }

// BuildWSHeaders constructs handshake headers from prepared provider and client
// identity callbacks. All callbacks used by the selected branch are required,
// and eligibility/session readers stay stable during this build. AgentIdentity
// suppresses bearer-token construction here, while the transport can supply its
// assertion through the acquisition HeadersFactory or gateway refresh step.
//
// Inbound beta features and known device/session headers are retained before
// key isolation, turn metadata, credential scoping, and attempt fingerprints.
// Codex account headers and originator follow, then the WS beta marker,
// User-Agent routing, and official pairing. Overrides precede beta capability
// negotiation and diagnostics, so connection compatibility can use the final
// handshake identity and features.
func BuildWSHeaders(ctx context.Context, options WSHeaderOptions) (http.Header, error) {
	headers := make(http.Header)
	if !options.AgentIdentity {
		headers.Set("authorization", "Bearer "+options.Token)
	}

	sessionID, conversationID := options.ResolveSession()
	if options.InboundHeaders() != nil {
		if v := strings.TrimSpace(options.InboundHeaders().Get("accept-language")); v != "" {
			headers.Set("accept-language", v)
		}
		// Retain beta features in the handshake because pool compatibility uses
		// their final value to prevent reuse across requests negotiating different upstream capabilities.
		for _, value := range options.InboundHeaders().Values("x-codex-beta-features") {
			if value = strings.TrimSpace(value); value != "" {
				headers.Add("x-codex-beta-features", value)
			}
		}
		for _, name := range [...]string{
			"x-codex-window-id",
			"x-codex-installation-id",
			"session-id",
			"thread-id",
			"x-client-request-id",
		} {
			if value := strings.TrimSpace(options.InboundHeaders().Get(name)); value != "" {
				headers.Set(name, value)
			}
		}
	}
	// Codex sessions include the API key ID to prevent collisions across users.
	if options.UsesCodex() {
		apiKeyID := options.APIKeyID()
		if sessionID != "" {
			headers.Set("session_id", options.IsolateSession(apiKeyID, sessionID))
		}
		if conversationID != "" {
			headers.Set("conversation_id", options.IsolateSession(apiKeyID, conversationID))
		}
	} else {
		if sessionID != "" {
			headers.Set("session_id", sessionID)
		}
		if conversationID != "" {
			headers.Set("conversation_id", conversationID)
		}
	}
	if state := strings.TrimSpace(options.TurnState); state != "" {
		headers.Set(WSTurnStateHeader, state)
	}
	if metadata := strings.TrimSpace(options.TurnMetadata); metadata != "" {
		headers.Set(openai.WSTurnMetadataHeader, metadata)
	}
	options.ApplyProviderIdentity(headers)
	options.ApplyFingerprint(headers)

	if options.UsesCodex() {
		if err := options.ProviderHeaders(ctx, headers); err != nil {
			return nil, fmt.Errorf("resolve chatgpt provider headers: %w", err)
		}
		headers.Set("originator", options.Originator())
	}

	headers.Set("OpenAI-Beta", "responses_websockets=2026-02-06")

	if ua := strings.TrimSpace(options.UserAgent()); ua != "" {
		headers.Set("user-agent", ua)
	}
	options.ApplyWSUserAgent(headers)

	// Pair originator with the selected User-Agent before overrides; an
	// unrecognized identity receives the configured canonical Codex pair.
	if options.UsesCodex() {
		openai.EnforceCodexIdentityHeadersWithUA(headers, "")
	}

	// Apply eligible provider values after identity pairing in every WS mode.
	// OpenAI OAuth has no override map; Grok OAuth uses its supported header
	// policy at the shared gateway binding.
	options.OverrideHeaders(headers)
	// HTTP and WebSocket share Codex session capability negotiation; the pool uses
	// these headers to separate incompatible connections.
	options.BetaFeatures(headers)
	options.RoutingHint(headers, nil)
	options.Diagnostics(headers, nil)

	return headers, nil
}
