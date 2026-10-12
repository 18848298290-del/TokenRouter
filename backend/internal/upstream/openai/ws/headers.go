package ws

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

const WSTurnStateHeader = "x-codex-turn-state"

type WSHeaderOptions struct {
	openai.ResponsesRequestOptions
	AgentIdentity           bool
	Token                   string `json:"-"`
	TurnState, TurnMetadata string
	ResolveSession          func() (string, string)
	InboundHeaders          func() http.Header
	UserAgent               func() string
	ApplyWSUserAgent        func(http.Header)
}

func (WSHeaderOptions) String() string     { return "openai websocket header options" }
func (v WSHeaderOptions) GoString() string { return v.String() }

// BuildWSHeaders builds the upstream WebSocket handshake headers.
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
		// Beta features participate in the handshake and the pool's connection compatibility checks.
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

	// Pair originator with the final User-Agent identity, using Codex TUI for unrecognized clients.
	// Mismatched identities can cause upstream 404 responses; see issue #3901.
	if options.UsesCodex() {
		openai.EnforceCodexIdentityHeadersWithUA(headers, "")
	}

	// The configured override callback applies API key provider headers in every WS mode.
	// OAuth callers supply a no-op callback.
	options.OverrideHeaders(headers)
	// HTTP and WebSocket share Codex session capability negotiation; the pool uses
	// these headers to separate incompatible connections.
	options.BetaFeatures(headers)
	options.RoutingHint(headers, nil)
	options.Diagnostics(headers, nil)

	return headers, nil
}
