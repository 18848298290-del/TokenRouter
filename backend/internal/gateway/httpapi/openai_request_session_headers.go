package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"golang.org/x/net/http/httpguts"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// A conversation header carries client state to one trusted API-key origin,
// while Codex routing hints describe the final model and service tier sent by
// an OAuth attempt. Provider overrides and model rewrites can otherwise leave
// headers referring to a different conversation or routing choice. Session
// forwarding uses an exact HTTPS hostname check and runs after provider
// overrides.
//
// Routing hints are rebuilt after deleting all casing variants from the
// outgoing map. The encoded model rejects delimiter characters, and the tier is
// reduced to the recognized Codex values before header validation. API-key
// attempts therefore clear inherited Codex hints, while diagnostics describe
// the final body and the header actually produced for the current provider.

// openCodeSessionHeader and OpenAICodexRoutingHintHeader name the outbound
// conversation identifier and final-route hint inspected by request
// construction and routing diagnostics.
const (
	// openCodeSessionHeader carries the caller's OpenCode conversation ID.
	openCodeSessionHeader = "X-OpenCode-Session"
	// OpenAICodexRoutingHintHeader names the final OAuth model/tier hint.
	OpenAICodexRoutingHintHeader = "x-codex-routing-hint"
)

// ApplyOpenCodeSessionHeader forwards a trimmed client conversation ID to HTTPS
// opencode.ai. Callers invoke it after provider overrides with the final target
// URL and header map. Only API-key providers qualify; missing caller values
// leave existing overrides intact.
//
// A nonempty caller ID replaces every casing variant in the outgoing map before
// setting the canonical header. This keeps that conversation's identity
// authoritative when an override used a different casing or a fixed
// provider-wide value.
func ApplyOpenCodeSessionHeader(c *gin.Context, provider *gatewayprovider.ExecutionProvider, targetURL string, headers http.Header) {
	if c == nil || c.Request == nil || provider == nil || provider.Record.Type != capability.ProviderTypeAPIKey || headers == nil {
		return
	}

	parsed, err := url.Parse(targetURL)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "opencode.ai") {
		return
	}

	sessionID := strings.TrimSpace(c.GetHeader(openCodeSessionHeader))
	if sessionID == "" {
		return
	}
	// Provider overrides can create noncanonical map keys. Remove every
	// variant before installing the conversation ID so upstream sees one
	// authoritative value.
	for key := range headers {
		if strings.EqualFold(key, openCodeSessionHeader) {
			delete(headers, key)
		}
	}
	headers.Set(openCodeSessionHeader, sessionID)
}

// ResolveOpenAIUpstreamOriginator resolves the outbound identity from HTTP
// originator and routing. A nil Gin context supplies an empty originator. The
// upstream resolver evaluates the official-client flag and the first optional
// TLS-router match.
func ResolveOpenAIUpstreamOriginator(c *gin.Context, isOfficialClient bool, routerMatch ...egress.TLSFingerprintRouterMatchResult) string {
	return ResolveOpenAIUpstreamOriginatorForClient(func() string {
		if c == nil {
			return ""
		}
		return c.GetHeader("originator")
	}, isOfficialClient, routerMatch...)
}

// ResolveOpenAIUpstreamOriginatorForClient delegates identity selection using
// the first optional router match. Callers provide an originator reader and
// official-client decision; the upstream resolver decides whether the client,
// router, or canonical identity supplies the value.
func ResolveOpenAIUpstreamOriginatorForClient(read func() string, official bool, matches ...egress.TLSFingerprintRouterMatchResult) string {
	var match egress.TLSFingerprintRouterMatchResult
	if len(matches) > 0 {
		match = matches[0]
	}
	return openai.ResolveUpstreamOriginator(read, official, match.Matched, match.UpstreamOriginator)
}

// SetOpenAICodexRoutingHint rebuilds the OAuth hint from final model and tier
// values. Callers supply values after request rewrites and tier filtering.
// Existing casing variants are removed before checking provider eligibility or
// validating the new hint.
//
// Only nonblank models without hint delimiters can be encoded. Recognized
// priority, flex, and ultrafast tiers are included after normalization; other
// values produce a model-only hint. Invalid HTTP header values leave the old
// hint cleared rather than advertising stale routing data.
func SetOpenAICodexRoutingHint(headers http.Header, provider *gatewayprovider.ExecutionProvider, model string, serviceTier string) {
	if headers == nil {
		return
	}

	// A previous attempt or override may have supplied noncanonical keys.
	// Delete them before eligibility checks so another provider cannot
	// inherit the hint.
	DeleteOpenAIHeaderEqualFold(headers, OpenAICodexRoutingHintHeader)
	if provider == nil || !provider.View().IsOpenAIOAuthLike() {
		return
	}

	model = strings.TrimSpace(model)
	if model == "" || strings.ContainsAny(model, ";=") {
		return
	}

	// Normalize the protocol's fast alias to priority before encoding it in
	// the Codex hint; tier filtering below leaves unsupported values
	// model-only.
	canonicalTier := protocolopenai.ServiceTierValue(serviceTier)
	// The hint encodes the recognized Codex routing tiers. Empty, default,
	// and other compatibility values omit tier metadata while retaining the
	// final model.
	switch canonicalTier {
	case tierpolicy.OpenAIFastTierPriority, tierpolicy.OpenAIFastTierFlex, tierpolicy.OpenAIFastTierUltrafast:
	default:
		canonicalTier = ""
	}

	hint := "model=" + model
	if canonicalTier != "" {
		hint += ";tier=" + canonicalTier
	}
	if !httpguts.ValidHeaderFieldValue(hint) {
		return
	}
	headers.Set(OpenAICodexRoutingHintHeader, hint)
}

// DeleteOpenAIHeaderEqualFold removes every outgoing key matching the trimmed
// header name. It trims keys as well as comparing case-insensitively, covering
// directly constructed header maps. A nil map needs no mutation.
func DeleteOpenAIHeaderEqualFold(headers http.Header, name string) {
	if headers == nil {
		return
	}
	name = strings.TrimSpace(name)
	for key := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			delete(headers, key)
		}
	}
}

// SetOpenAICodexRoutingHintFromBody extracts final model and tier values for
// hint construction. It passes gjson's string rendering of model and
// service_tier, with missing or null fields rendered empty.
// SetOpenAICodexRoutingHint applies provider eligibility, normalization, and
// header validation to those values.
func SetOpenAICodexRoutingHintFromBody(headers http.Header, provider *gatewayprovider.ExecutionProvider, body []byte) {
	fields := gjson.GetManyBytes(body, "model", "service_tier")
	SetOpenAICodexRoutingHint(headers, provider, fields[0].String(), fields[1].String())
}

// LogOpenAIRoutingDiagnostics logs final routing metadata for one transport
// decision. A nil context uses background logging and a nil provider uses ID
// zero. String values are trimmed, and the service tier uses protocol
// normalization.
func LogOpenAIRoutingDiagnostics(
	ctx context.Context,
	provider *gatewayprovider.ExecutionProvider,
	transport string,
	model string,
	serviceTier string,
	hintGenerated bool,
	wsAffinityDecision string,
) {
	if ctx == nil {
		ctx = context.Background()
	}
	providerID := int64(0)
	if provider != nil {
		providerID = provider.Record.ID
	}

	logging.FromContext(ctx).Debug("openai routing decision",
		zap.String("component", "service.openai_routing"),
		zap.String("transport", strings.TrimSpace(transport)),
		zap.Int64("provider_id", providerID),
		zap.String("final_model", strings.TrimSpace(model)),
		zap.String("final_service_tier", protocolopenai.ServiceTierValue(serviceTier)),
		zap.Bool("routing_hint_generated", hintGenerated),
		zap.String("ws_affinity_decision", strings.TrimSpace(wsAffinityDecision)),
	)
}

// LogOpenAIRoutingDiagnosticsFromBody logs route values from the final outgoing
// payload. It derives hint presence from the outgoing header map and passes the
// transport and WebSocket affinity decision to the shared diagnostic logger.
func LogOpenAIRoutingDiagnosticsFromBody(
	ctx context.Context,
	provider *gatewayprovider.ExecutionProvider,
	transport string,
	headers http.Header,
	body []byte,
	wsAffinityDecision string,
) {
	fields := gjson.GetManyBytes(body, "model", "service_tier")
	LogOpenAIRoutingDiagnostics(
		ctx,
		provider,
		transport,
		fields[0].String(),
		fields[1].String(),
		strings.TrimSpace(headers.Get(OpenAICodexRoutingHintHeader)) != "",
		wsAffinityDecision,
	)
}
