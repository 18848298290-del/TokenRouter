package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func OpenAIClientPolicyForbiddenMessage(result providercore.CodexClientRestrictionDetectionResult) string {
	// Choose the rejection reason for the configured client policy.
	if result.Policy == providercore.OpenAIOAuthClientPolicyCodexOnly {
		return "This provider only allows Codex official clients"
	}
	if result.Policy == providercore.OpenAIOAuthClientPolicyTLSRouterMatchedOnly {
		return "This provider only allows clients matched by the configured TLS router"
	}
	return "This provider only allows configured OpenAI OAuth clients"
}

func (s *OpenAIRequests) DetectClient(c *gin.Context, provider *gatewayprovider.ExecutionProvider, tlsRouterMatch egress.TLSFingerprintRouterMatchResult) providercore.CodexClientRestrictionDetectionResult {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	return s.DetectClientInput(ctx, func() (string, string) {
		if c == nil {
			return "", ""
		}
		return c.GetHeader("User-Agent"), c.GetHeader("originator")
	}, provider, tlsRouterMatch)
}

// DetectClientInput reads dynamic global policy before detecting the supplied client identity.
func (s *OpenAIRequests) DetectClientInput(ctx context.Context, readClient func() (string, string), provider *gatewayprovider.ExecutionProvider, tlsRouterMatch egress.TLSFingerprintRouterMatchResult) providercore.CodexClientRestrictionDetectionResult {
	var globalAllowedClients []string
	if provider != nil && provider.View().IsCodexCLIOnlyEnabled() && s != nil && s.Readers != nil {
		if s.Readers.Gateway.IsOpenAIAllowClaudeCodeCodexPluginEnabled(ctx) {
			globalAllowedClients = []string{openai.AllowedClientClaudeCode}
		}
	}
	return s.clientDetector().DetectClient(readClient, gatewayprovider.ExecutionRecord(provider), globalAllowedClients, tlsRouterMatch.Matched)
}

func LogCodexCLIOnlyDetection(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, apiKeyID int64, result providercore.CodexClientRestrictionDetectionResult, body []byte) {
	if !result.Enabled {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	providerID := int64(0)
	if provider != nil {
		providerID = provider.Record.ID
	}
	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.Int64("provider_id", providerID),
		zap.Bool("codex_cli_only_enabled", result.Enabled),
		zap.Bool("codex_official_client_match", result.Matched),
		zap.String("reject_reason", result.Reason),
	}
	if apiKeyID > 0 {
		fields = append(fields, zap.Int64("api_key_id", apiKeyID))
	}
	if !result.Matched {
		fields = AppendCodexRejectedRequestFields(fields, c, body)
	}
	log := logging.FromContext(ctx).With(fields...)
	if result.Matched {
		log.Info("OpenAI codex_cli_only 放行请求")
		return
	}
	log.Warn("OpenAI codex_cli_only 拒绝非官方客户端请求")
}

// EnforceClient applies OpenAI OAuth client restrictions outside the main /responses entry.
func (s *OpenAIRequests) EnforceClient(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte, tlsRouterMatch egress.TLSFingerprintRouterMatchResult) error {
	result := s.DetectClient(c, provider, tlsRouterMatch)
	apiKeyID := APIKeyIDFromContext(c)
	LogCodexCLIOnlyDetection(ctx, c, provider, apiKeyID, result, body)
	if !result.Enabled || result.Matched {
		return nil
	}
	MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
	if c != nil && GetOpenAIClientTransport(c) != OpenAIClientTransportWS {
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"type":    "forbidden_error",
				"message": OpenAIClientPolicyForbiddenMessage(result),
			},
		})
	}
	return errors.New("openai oauth client policy restriction: client is not allowed")
}

// ApplyUserAgentHeader applies HTTP upstream UA rules to WebSocket handshake headers.
func (s *OpenAIRequests) ApplyUserAgentHeader(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	headers http.Header,
	passthrough bool,
	routerMatch ...egress.TLSFingerprintRouterMatchResult,
) {
	if headers == nil {
		return
	}
	req := &http.Request{Header: headers}
	s.ApplyUserAgent(ctx, c, provider, req, passthrough, routerMatch...)
}

func (s *OpenAIRequests) MatchTLS(c *gin.Context, provider *gatewayprovider.ExecutionProvider) egress.TLSFingerprintRouterMatchResult {
	return s.MatchTLSInput(func() string {
		if c == nil {
			return ""
		}
		return c.GetHeader("User-Agent")
	}, provider)
}

// MatchTLSInput reads User-Agent only when the provider has a configured TLS router.
func (s *OpenAIRequests) MatchTLSInput(readUserAgent func() string, provider *gatewayprovider.ExecutionProvider) egress.TLSFingerprintRouterMatchResult {
	if s == nil || s.Routers == nil || provider == nil || provider.View().GetTLSFingerprintRouterID() <= 0 {
		return egress.TLSFingerprintRouterMatchResult{}
	}
	userAgent := readUserAgent()
	return s.Routers.MatchUserAgent(provider.View().GetTLSFingerprintRouterID(), userAgent)
}

// TLSProfile resolves the egress profile, returning nil when profile selection is unavailable.
func (s *OpenAIRequests) TLSProfile(value *gatewayprovider.ExecutionProvider, routerMatch ...egress.TLSFingerprintRouterMatchResult) *tlsfingerprint.Profile {
	if s == nil || s.Profiles == nil {
		return nil
	}
	return s.Profiles.ResolveRequestTLS(gatewayprovider.ExecutionTLSSelection(value, routerMatch))
}

func (s *OpenAIRequests) WSTLSProfile(provider *gatewayprovider.ExecutionProvider, routerMatch ...egress.TLSFingerprintRouterMatchResult) (*tlsfingerprint.Profile, string) {
	profile := s.TLSProfile(provider, routerMatch...)
	if profile == nil {
		return nil, ""
	}
	// Responses WebSocket uses HTTP/1.1 Upgrade; pool identity includes the profile with h2 removed.
	profile = tlsfingerprint.HTTP1OnlyProfile(profile)
	return profile, egress.WebSocketTLSIdentity(gatewayprovider.ExecutionTLSSelection(provider, routerMatch), true, tlsfingerprint.CacheKey(profile))
}

func (s *OpenAIRequests) ApplyUserAgent(ctx context.Context, _ *gin.Context, value *gatewayprovider.ExecutionProvider, req *http.Request, passthrough bool, matches ...egress.TLSFingerprintRouterMatchResult) {
	var match egress.TLSFingerprintRouterMatchResult
	if len(matches) > 0 {
		match = matches[0]
	}
	s.ClientPolicy.ApplyUserAgent(ctx, gatewayprovider.ExecutionRecord(value), req, passthrough, match)
}

func (s *OpenAIRequests) clientDetector() providercore.ClientRestrictionDetector {
	if s != nil && s.Detector != nil {
		return s.Detector
	}
	force := s != nil && s.Options.ForceCLI
	return &providercore.CodexClientDetector{Options: providercore.CodexClientOptions{ForceCLI: force, OfficialUserAgent: openai.IsCodexOfficialClientRequestStrict, OfficialOriginator: openai.IsCodexOfficialClientOriginator, AllowedClients: openai.MatchAllowedClients}}
}
