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

const (
	openCodeSessionHeader        = "X-OpenCode-Session"
	OpenAICodexRoutingHintHeader = "x-codex-routing-hint"
)

// ApplyOpenCodeSessionHeader forwards the caller-owned conversation identifier
// to HTTPS targets on opencode.ai. The caller applies this after provider
// header overrides so a per-conversation value cannot be replaced by a fixed
// provider-wide override.
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
	for key := range headers {
		if strings.EqualFold(key, openCodeSessionHeader) {
			delete(headers, key)
		}
	}
	headers.Set(openCodeSessionHeader, sessionID)
}

func ResolveOpenAIUpstreamOriginator(c *gin.Context, isOfficialClient bool, routerMatch ...egress.TLSFingerprintRouterMatchResult) string {
	return ResolveOpenAIUpstreamOriginatorForClient(func() string {
		if c == nil {
			return ""
		}
		return c.GetHeader("originator")
	}, isOfficialClient, routerMatch...)
}

// ResolveOpenAIUpstreamOriginatorForClient passes the router match to the upstream originator resolver.
func ResolveOpenAIUpstreamOriginatorForClient(read func() string, official bool, matches ...egress.TLSFingerprintRouterMatchResult) string {
	var match egress.TLSFingerprintRouterMatchResult
	if len(matches) > 0 {
		match = matches[0]
	}
	return openai.ResolveUpstreamOriginator(read, official, match.Matched, match.UpstreamOriginator)
}

// SetOpenAICodexRoutingHint generates a Codex routing hint for OpenAI OAuth requests.
// Callers supply the final upstream model and service tier after local rewrites and filtering.
func SetOpenAICodexRoutingHint(headers http.Header, provider *gatewayprovider.ExecutionProvider, model string, serviceTier string) {
	if headers == nil {
		return
	}

	// Remove every casing variant before writing the final routing hint.
	// Header.Del removes the canonical key, but inbound maps may also contain lowercase keys.
	DeleteOpenAIHeaderEqualFold(headers, OpenAICodexRoutingHintHeader)
	if provider == nil || !provider.View().IsOpenAIOAuthLike() {
		return
	}

	model = strings.TrimSpace(model)
	if model == "" || strings.ContainsAny(model, ";=") {
		return
	}

	// Codex default omits the tier; fast becomes priority, while flex and ultrafast remain unchanged.
	canonicalTier := protocolopenai.ServiceTierValue(serviceTier)
	// Without a Codex model catalogue snapshot, arbitrary tier IDs cannot be validated.
	// Send recognized Codex tiers; default, empty, and other compatibility values send only the model.
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

func SetOpenAICodexRoutingHintFromBody(headers http.Header, provider *gatewayprovider.ExecutionProvider, body []byte) {
	fields := gjson.GetManyBytes(body, "model", "service_tier")
	SetOpenAICodexRoutingHint(headers, provider, fields[0].String(), fields[1].String())
}

// LogOpenAIRoutingDiagnostics records routing values derived by the gateway.
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
