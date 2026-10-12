package messageforward

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// attempt holds request preparation, credentials, and response state; gateway/forward controls execution order.
type attempt struct {
	s                          *Runtime
	c                          HTTPBoundary
	provider                   *gatewayprovider.ExecutionProvider
	token, tokenType, proxyURL string
	profile                    *tlsfingerprint.Profile
	toolRewrite                *claude.ToolNameRewrite
	state                      *AttemptState
	response                   *http.Response
}

func newAttempt(s *Runtime, c HTTPBoundary, provider *gatewayprovider.ExecutionProvider) *attempt {
	return &attempt{s: s, c: c, provider: provider, state: &AttemptState{}}
}

func (a *attempt) input() forwardcore.MessageInput {
	in := forwardcore.MessageInput{ProviderPresent: a.provider != nil, HTTPPresent: a.c.Present()}
	if v := a.provider; v != nil {
		in.ProviderID = v.Record.ID
		in.ProviderName = v.Record.Name
		in.ProviderType = v.Record.Type
		in.Platform = v.Record.Platform
		in.OAuth = v.View().IsOAuth()
		in.Passthrough = v.View().IsAnthropicAPIKeyPassthroughEnabled()
		in.Bedrock = v.View().IsBedrock()
	}
	if a.s.options.Configured {
		in.LogErrorBody = a.s.options.LogErrorBody
		in.LogErrorBodyMaxBytes = a.s.options.LogErrorBodyMaxBytes
		in.FailoverOn400 = a.s.options.FailoverOn400
	}
	return in
}

func (a *attempt) ShouldEmulate(ctx context.Context, group *int64, body []byte) bool {
	return a.s.shouldEmulate(ctx, a.provider, group, body)
}

func (a *attempt) Emulate(ctx context.Context, p *requeststate.ParsedRequest) (*forwardcore.Result, error) {
	return a.s.emulate(ctx, a.c, a.provider, p)
}

func (a *attempt) MappedModel(model string) string {
	return gatewayprovider.ExecutionModelPolicy(a.provider).Mapped(model)
}

func (a *attempt) ReplaceModel(body []byte, model string) []byte {
	return protocolopenai.ReplaceModelInBody(body, model)
}

func (a *attempt) Passthrough(ctx context.Context, in forwardcore.PassthroughInput) (*forwardcore.Result, error) {
	return a.s.passthrough(ctx, a.c, a.provider, forwardcore.APIKeyInput{Body: in.Body, Parsed: in.Parsed, RequestModel: in.RequestModel, OriginalModel: in.OriginalModel, RequestStream: in.Stream, StartTime: in.StartedAt})
}

func (a *attempt) Bedrock(ctx context.Context, p *requeststate.ParsedRequest, start time.Time) (*forwardcore.Result, error) {
	return a.s.bedrock(ctx, a.c, a.state, a.provider, p, start)
}

func (a *attempt) Begin() (func(), error) {
	if a.s.dependencies.Enter != nil {
		return a.s.dependencies.Enter()
	}
	return func() {}, nil
}

func (a *attempt) Beta(ctx context.Context, model string) error {
	return a.s.checkBeta(ctx, a.state, a.provider, a.c.RequestHeaders().Get("anthropic-beta"), model)
}

func (a *attempt) DebugOriginal(body []byte, model string, stream bool) {
	if a.s.dependencies.Debug != nil {
		a.s.dependencies.Debug.Snapshot("CLIENT_ORIGINAL", a.c.RequestHeaders(), body, map[string]string{"provider": fmt.Sprintf("%d(%s)", a.provider.Record.ID, a.provider.Record.Name), "provider_type": a.provider.Record.Type, "model": model, "stream": strconv.FormatBool(stream)})
	}
}

func (a *attempt) ProviderMappedModel(model string) string {
	return gatewayprovider.ExecutionModelPolicy(a.provider).Mapped(model)
}

func (a *attempt) IsClaudeCode(ctx context.Context, body []byte, metadata string) bool {
	ua := ""
	if a.c.Present() {
		ua = a.c.RequestHeaders().Get("User-Agent")
	}
	return requeststate.IsClaudeCodeClient(ctx) || clientmeta.IsClaudeCodeClient(ua, metadata) || claude.IsProxiedClaudeCodeRequest(body, metadata)
}

func (a *attempt) SystemSettings(ctx context.Context) (bool, string, string) {
	if a.s.dependencies.Settings == nil {
		return true, "", ""
	}
	return a.s.dependencies.Settings.GetClaudeOAuthSystemPromptInjectionSettings(ctx)
}

func (a *attempt) RewriteSystem(body []byte, p *requeststate.ParsedRequest, prompt, blocks string) []byte {
	system, _ := p.SystemValue()
	return claude.RewriteSystemForNonClaudeCodeWithPromptBlocks(body, system, prompt, blocks)
}

func (a *attempt) Metadata(ctx context.Context, p *requeststate.ParsedRequest) string {
	if a.s.dependencies.Fingerprint == nil || !a.c.Present() {
		return ""
	}
	fp, err := a.s.dependencies.Fingerprint.GetOrCreateFingerprint(ctx, a.provider.Record.ID, a.c.RequestHeaders())
	if err != nil || fp == nil {
		return ""
	}
	_, mimic, _ := a.s.dependencies.Settings.GetGatewayForwardingSettings(ctx)
	if mimic {
		return ""
	}
	return metadataUserID(p, a.provider, fp)
}

// NormalizeOAuth applies the current Anthropic OAuth normalization options.
func (a *attempt) NormalizeOAuth(body []byte, o forwardcore.NormalizeOptions) []byte {
	return claude.NormalizeClaudeOAuthRequestBody(body, claude.ClaudeOAuthNormalizeOptions{StripSystemCacheControl: o.StripSystemCacheControl, InjectMetadata: o.InjectMetadata, MetadataUserID: o.MetadataUserID})
}

func (a *attempt) RewriteCache(ctx context.Context, body []byte) []byte {
	return a.s.rewriteCache(ctx, body)
}

func (a *attempt) RewriteTools(body []byte) ([]byte, bool) {
	a.toolRewrite = claude.BuildToolNameRewriteFromBody(body)
	if a.toolRewrite == nil {
		return body, false
	}
	return claude.ApplyToolNameRewriteToBody(body, a.toolRewrite), true
}

func (a *attempt) BindTools() {
	a.state.ToolNames = a.toolRewrite
}

func (a *attempt) ToolsLast(body []byte) []byte {
	return claude.ApplyToolsLastCacheBreakpoint(body)
}

func (a *attempt) NormalizeDateline(ctx context.Context, body []byte) ([]byte, bool) {
	return a.s.normalizeDateline(ctx, a.provider, body)
}

func (a *attempt) CacheLimit(body []byte) []byte {
	return claude.EnforceCacheControlLimit(body)
}

func (a *attempt) PlatformModel(model string) string {
	return gatewayprovider.ExecutionModelPolicy(a.provider).AnthropicUpstream(model)
}

func (a *attempt) InjectTTL(ctx context.Context) bool {
	return a.s.injectTTL(ctx, a.provider)
}

func (a *attempt) CacheTTL(body []byte) []byte {
	return claude.InjectAnthropicCacheControlTTL1h(body)
}

func (a *attempt) Credential(ctx context.Context) error {
	token, kind, e := a.s.dependencies.Credentials.Resolve(ctx, gatewayprovider.ExecutionRecord(a.provider))
	a.token, a.tokenType = token, kind
	return e
}

func (a *attempt) Transport() {
	if a.provider.Record.ProxyID != nil && a.provider.Record.Proxy != nil && (!a.provider.View().IsCustomBaseURLEnabled() || a.provider.View().GetCustomBaseURL() == "") {
		a.proxyURL = a.provider.Record.Proxy.URL()
	}
	a.profile = a.s.requestTLS(a.provider)
	logging.LegacyPrintf("service.gateway", "[Forward] Using provider: ID=%d Name=%s Platform=%s Type=%s TLSFingerprint=%v Proxy=%s", a.provider.Record.ID, a.provider.Record.Name, a.provider.Record.Platform, a.provider.Record.Type, a.profile, a.proxyURL)
}

func (a *attempt) FilterSearchHistory(body []byte, model string) []byte {
	return searchtools.FilterWebSearchHistoryBlocks(body, modelidentity.ResolveThinkingProtocol(model) == modelidentity.ThinkingProtocolPassbackRequired)
}

func (a *attempt) FilterThinking(body []byte, model string) []byte {
	return gatewayprovider.FilterThinkingBlocks(body, model)
}

func (a *attempt) PassbackThinking(model string) bool {
	return modelidentity.ResolveThinkingProtocol(model) == modelidentity.ThinkingProtocolPassbackRequired
}

func (a *attempt) NormalizeThinking(body []byte, model string) ([]byte, bool) {
	return gatewayprovider.NormalizeChineseLLMThinking(body, model)
}

func (a *attempt) ReadErrorBody() ([]byte, error) {
	return a.s.readErrorBody(a.response)
}

func (a *attempt) ResetErrorBody(body []byte) {
	_ = a.response.Body.Close()
	a.response.Body = io.NopCloser(bytes.NewReader(body))
}

func (a *attempt) Health(ctx context.Context, mode string, status int, headers map[string][]string, body []byte, model string) forwardcore.ErrorDecision {
	var d providercore.UpstreamErrorDecision
	switch mode {
	case "retry":
		d = a.s.retryHealth(ctx, a.response, a.provider, model)
	case "failover":
		d = a.s.failoverHealth(ctx, a.response, a.provider, model)
	case "failover_synthetic":
		resp := &http.Response{StatusCode: status, Header: http.Header(headers).Clone(), Body: io.NopCloser(bytes.NewReader(body))}
		d = a.s.failoverHealth(ctx, resp, a.provider, model)
		_ = resp.Body.Close()
	default:
		d = providercore.ErrorDecisionWithoutPersistence(gatewayprovider.ExecutionErrorPolicy(a.provider), status)
		if mode == "persist" && a.s.dependencies.Health != nil {
			d = gatewayprovider.ApplyExecutionHealth(ctx, a.s.dependencies.Health, a.provider, gatewayprovider.HealthObservationFromContext(ctx, status, headers, body, []string{model}))
		}
	}
	return forwardcore.ErrorDecision{
		Generic:           d.ShouldReturnGenericError(),
		Failover:          d.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(a.provider), status, true),
		RetrySameProvider: d.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(a.provider), status),
	}
}

func (a *attempt) HandleError(ctx context.Context, model string, retry bool) (*forwardcore.Result, error) {
	return a.s.handleError(ctx, a.c, a.state, a.provider, a.response, retry, model)
}

func (a *attempt) Observe(n forwardcore.Notice) {
	a.c.Observe(n)
}
func (a *attempt) Failover400(body []byte) bool { return messageFailover400(body) }
func (a *attempt) FailoverError(status int, body []byte, retry bool) error {
	return &forwardcore.UpstreamFailoverError{StatusCode: status, ResponseBody: body, RetryableOnSameProvider: retry}
}

func (a *attempt) IsFailover(err error) bool {
	var v *forwardcore.UpstreamFailoverError
	return errors.As(err, &v)
}

func (a *attempt) Truncate(value string, n int) string {
	return logredact.TruncateUTF8(value, n)
}

func (a *attempt) TruncateBytes(body []byte, n int) string {
	return logredact.TruncateLine(body, n)
}

func (a *attempt) Sanitize(value string) string {
	return logredact.SanitizeUpstreamQueries(value)
}

func (a *attempt) Log(value string) {
	logging.LegacyPrintf("service.gateway", "%s", value)
}

func (a *attempt) Execute(ctx context.Context, in forwardcore.MessageExecution) (upstream.AttemptResult, error) {
	exchange := a.s.exchangeOptions(a.c, a.state, a.provider, a.token, a.tokenType, in.Model, in.Stream, in.Mimic, a.proxyURL, a.profile, in.ReplaceBody)
	target := &claude.Target{
		ProviderID: a.provider.Record.ID, Model: in.Model, Exchange: exchange, Response: a.s.responseOptions(ctx, a.c, a.state, a.provider, in.Model, false), StartedAt: in.StartedAt, MimicClaudeCode: in.Mimic,
		BeforeResponse: func(ctx context.Context, resp *http.Response, wire []byte) (bool, error) {
			a.response = resp
			return in.Hooks.Before(ctx, &forwardcore.ExchangeResponse{StatusCode: resp.StatusCode, Headers: resp.Header, RequestID: resp.Header.Get("x-request-id")}, wire)
		},
		OnWireBody: in.Hooks.Wire, Accepted: in.Hooks.Accepted, BeforeStream: in.Hooks.BeforeStream,
		OnStream: func(r *claude.StreamResult, _ error) {
			if r != nil {
				in.Hooks.Stream(&forwardcore.StreamOutcome{Usage: r.Usage, FirstTokenMs: r.FirstTokenMs, ClientDisconnect: r.ClientDisconnect})
			}
		},
	}
	return (claude.Executor{}).Execute(ctx, upstream.AttemptInput{Protocol: protocolcore.ProtocolAnthropicMessages, Body: in.Body, ResponseModel: in.OriginalModel, Stream: in.Stream, Target: target}, a.c.Sink())
}

func (a *attempt) StreamError(err error) (string, bool) {
	if e, ok := errors.AsType[*claude.StreamErrorEventError](err); ok {
		return e.RawData, true
	}
	return "", false
}
func (a *attempt) Size() int     { return a.c.Size() }
func (a *attempt) Written() bool { return a.c.Written() }
func (a *attempt) GenericError() {
	a.c.GenericError()
}

func (a *attempt) ServiceTier() string {
	return a.c.ServiceTier()
}
