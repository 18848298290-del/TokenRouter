package messageforward

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// Dependencies 包含请求准备和执行共用的凭据、传输和策略依赖。
// Prepare 与执行共用这些实例，动态设置在请求处理中读取。
type Dependencies struct {
	Attributes    *routing.ModelAttributeService
	Credentials   *provider.MessageCredentialSource
	Fingerprint   *anthropic.RequestFingerprint
	Transport     httpclient.UpstreamTransport
	Health        *provideradapter.UpstreamHealth
	TLS           *egressprovider.TLSProfiles
	Settings      *gateway.RuntimeSettings
	Prices        *billing.PriceResolver
	Search        *searchtools.Emulator
	Enter         func() (func(), error)
	Debug         DebugObserver
	ProviderState ProviderState
	Deferred      *provider.DeferredService
	GroupPolicies BedrockGroupPolicies
}

// BedrockGroupPolicies 在准备上游请求时读取分组功能开关。
type BedrockGroupPolicies interface {
	GetGroupPolicy(context.Context, int64) (*routing.GroupPolicyView, error)
}

// ProviderState 定义提供商临时停调状态的写入方法。
type ProviderState interface {
	SetTempUnschedulable(context.Context, int64, time.Time, string) error
}

// DebugObserver 接收本次请求的调试快照，实现方管理文件和日志资源。
type DebugObserver interface {
	Snapshot(string, http.Header, []byte, map[string]string)
	Capture(*http.Request, []byte, *gatewayadapter.ExecutionProvider, string, bool, bool) string
}

// Runtime 包含 Messages 执行依赖，每次尝试分别保存状态、凭据和响应。
type Runtime struct {
	dependencies Dependencies
	options      Options
}

func NewRuntime(dependencies Dependencies, options Options) *Runtime {
	options.URLValidation.AllowedHosts = slices.Clone(options.URLValidation.AllowedHosts)
	return &Runtime{dependencies: dependencies, options: options}
}

// checkBeta 在普通 Messages 的既有准备位置读取并保存结果，空集也代表已查询。
func (r *Runtime) checkBeta(ctx context.Context, state *AttemptState, target *gatewayadapter.ExecutionProvider, header, model string) error {
	result := r.evaluateBeta(ctx, target, header, model)
	if result.BlockErr != nil {
		return result.BlockErr
	}
	state.BetaEvaluated = true
	state.BetaFilters = result.FilterSet
	if state.BetaFilters == nil {
		state.BetaFilters = map[string]struct{}{}
	}
	return nil
}

// betaFilters 优先读取已保存的 Beta 过滤结果，缺少结果时查询设置。
func (r *Runtime) betaFilters(ctx context.Context, state *AttemptState, target *gatewayadapter.ExecutionProvider, model string) map[string]struct{} {
	if state.BetaEvaluated {
		return state.BetaFilters
	}
	return r.evaluateBeta(ctx, target, "", model).FilterSet
}

func (r *Runtime) evaluateBeta(ctx context.Context, target *gatewayadapter.ExecutionProvider, header, model string) anthropic.BetaPolicyResult {
	if r.dependencies.Settings == nil {
		return anthropic.BetaPolicyResult{}
	}
	settings, err := r.dependencies.Settings.GetBetaPolicySettings(ctx)
	if err != nil || settings == nil {
		return anthropic.BetaPolicyResult{}
	}
	return anthropic.EvaluateBetaPolicy(gatewayadapter.AnthropicBetaPolicy(settings), header, target.View().IsOAuth(), target.View().IsBedrock(), model)
}

// validateBaseURL 根据配置执行 URL 格式检查或允许列表校验。
func (r *Runtime) validateBaseURL(raw string) (string, error) {
	var normalized string
	var err error
	if r.options.Configured && !r.options.URLAllowlistEnabled {
		normalized, err = egress.ValidateURLFormat(raw, r.options.AllowInsecureHTTP)
	} else {
		policy := r.options.URLValidation
		policy.RequireAllowlist = true
		normalized, err = egress.ValidateHTTPSURL(raw, policy)
	}
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return normalized, nil
}
