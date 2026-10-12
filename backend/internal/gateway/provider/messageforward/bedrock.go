package messageforward

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/bedrock"
)

// bedrock 转发请求到 AWS Bedrock。
func (r *Runtime) bedrock(
	ctx context.Context,
	output HTTPBoundary, state *AttemptState,
	provider *gatewayprovider.ExecutionProvider,
	parsed *requeststate.ParsedRequest,
	startTime time.Time,
) (*forward.Result, error) {
	reqModel := parsed.Model
	reqStream := parsed.Stream
	body := parsed.Body.Bytes()

	route, err := gatewayprovider.ExecutionModelPolicy(provider).BedrockRoute(reqModel)
	if err != nil {
		logging.LegacyPrintf("service.gateway", "[Bedrock] %s", bedrock.BedrockRoutingDiagnostic(err))
		return nil, err
	}
	region, mappedModel := route.SourceRegion, route.ModelID
	attributes, attributesErr := gatewayprovider.ResolveModelAttributes(ctx, provider, mappedModel, r.dependencies.Attributes, "anthropic_messages")
	if attributesErr != nil {
		return nil, attributesErr
	}
	body, err = gatewayprovider.ApplyModelAttributeParameters(body, attributes)
	if err != nil {
		return nil, err
	}
	if mappedModel != reqModel {
		logging.LegacyPrintf("service.gateway", "[Bedrock] Model mapping: %s -> %s (provider: %s)", reqModel, mappedModel, provider.Record.Name)
	}

	betaHeader := ""
	if output.RequestPresent() {
		betaHeader = output.RequestHeaders().Get("anthropic-beta")
	}

	// 准备请求体（注入 anthropic_version/anthropic_beta，移除 Bedrock 不支持的字段，清理 cache_control）
	betaTokens, err := r.bedrockBetaTokens(ctx, provider, betaHeader, body, mappedModel, attributes.Rules)
	if err != nil {
		return nil, err
	}

	bedrockBody, err := bedrock.PrepareBedrockRequestBodyWithTokens(body, mappedModel, betaTokens, false, attributes.Rules)
	if err != nil {
		return nil, fmt.Errorf("prepare bedrock request body: %w", err)
	}

	proxyURL := ""
	if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}

	logging.LegacyPrintf("service.gateway", "[Bedrock] 命中 Bedrock 分支: provider=%d name=%s model=%s->%s stream=%v",
		provider.Record.ID, provider.Record.Name, reqModel, mappedModel, reqStream)

	// 根据提供商类型选择认证方式
	var signer *bedrock.BedrockSigner
	var bedrockAPIKey string
	if provider.View().IsBedrockAPIKey() {
		bedrockAPIKey = provider.View().GetCredential("api_key")
		if bedrockAPIKey == "" {
			return nil, fmt.Errorf("api_key not found in bedrock credentials")
		}
	} else {
		signer, err = provideradapter.NewBedrockSignerFromProvider(gatewayprovider.ExecutionRecord(provider))
		if err != nil {
			return nil, fmt.Errorf("create bedrock signer: %w", err)
		}
	}

	options, policy := r.bedrockOptions(ctx, output, state, provider, mappedModel, region, reqStream, signer, bedrockAPIKey, proxyURL)
	streamOptions := bedrock.StreamOptions{ProviderID: provider.Record.ID}
	if r.options.StreamInterval > 0 {
		streamOptions.Interval = r.options.StreamInterval
	}
	if r.dependencies.Health != nil {
		streamOptions.OnTimeout = func(ctx context.Context, model string) {
			r.dependencies.Health.Core.HandleStreamTimeout(ctx, gatewayprovider.ExecutionRecord(provider), model)
		}
	}
	readLimit := r.options.ResponseReadLimit
	hadHTTPError := false
	var errorResult *forward.Result
	target := &bedrock.Target{ProviderID: provider.Record.ID, Request: options, Retry: policy, Stream: streamOptions, StartedAt: startTime, Enter: r.dependencies.Enter, Accepted: parsed.OnUpstreamAccepted, ReadBody: func(r io.Reader) ([]byte, error) {
		return output.ReadResponseBody(r, readLimit, MessagesBody)
	}, HTTPError: func(ctx context.Context, resp *http.Response) (upstream.AttemptResult, error) {
		hadHTTPError = true
		var err error
		errorResult, err = r.bedrockError(ctx, resp, output, state, provider, mappedModel)
		return upstream.AttemptResult{}, err
	}}
	result, err := (bedrock.Executor{}).Execute(ctx, upstream.AttemptInput{Protocol: protocol.ProtocolAnthropicMessages, Body: bedrockBody, Stream: reqStream, ResponseModel: reqModel, Target: target}, output.Sink())
	if hadHTTPError {
		return errorResult, err
	}
	if err != nil {
		return nil, err
	}
	message := forward.MessagesFromAttempt(result)
	converted := (*forward.Result)(message)
	converted.UpstreamHeaders = result.UpstreamHeaders
	return converted, nil
}

// bedrockError 处理 Bedrock 上游 4xx/5xx 错误（failover + 错误响应）。
func (r *Runtime) bedrockError(
	ctx context.Context,
	resp *http.Response,
	output HTTPBoundary, state *AttemptState,
	provider *gatewayprovider.ExecutionProvider,
	mappedModel string,
) (*forward.Result, error) {
	// 同提供商重试耗尽后判断是否切换提供商。
	if forward.ShouldRetry(provider.View().IsOAuth(), resp.StatusCode) {
		if forward.ShouldFailover(resp.StatusCode) {
			respBody, _ := r.readErrorBody(resp)
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))

			logging.LegacyPrintf("service.gateway", "[Bedrock] Upstream error (retry exhausted, failover): Provider=%d(%s) Status=%d Body=%s",
				provider.Record.ID, provider.Record.Name, resp.StatusCode, logredact.TruncateUTF8(string(respBody), 1000))

			decision := r.retryHealth(ctx, resp, provider, mappedModel)
			if decision.ShouldReturnGenericError() {
				return r.handleError(ctx, output, state, provider, resp, false, mappedModel)
			}
			output.Observe(forward.Notice{
				Platform:           provider.Record.Platform,
				ProviderID:         provider.Record.ID,
				ProviderName:       provider.Record.Name,
				UpstreamStatusCode: resp.StatusCode,
				Kind:               "retry_exhausted_failover",
				Message:            upstream.ExtractErrorMessage(respBody),
			})
			return nil, &forward.UpstreamFailoverError{
				StatusCode:              resp.StatusCode,
				ResponseBody:            respBody,
				RetryableOnSameProvider: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode),
			}
		}
		return r.handleError(ctx, output, state, provider, resp, true, mappedModel)
	}

	// 无法在当前提供商重试时，返回换号决定。
	if forward.ShouldFailover(resp.StatusCode) {
		respBody, _ := r.readErrorBody(resp)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))

		decision := r.failoverHealth(ctx, resp, provider, mappedModel)
		if decision.ShouldReturnGenericError() {
			return r.handleError(ctx, output, state, provider, resp, false, mappedModel)
		}
		output.Observe(forward.Notice{
			Platform:           provider.Record.Platform,
			ProviderID:         provider.Record.ID,
			ProviderName:       provider.Record.Name,
			UpstreamStatusCode: resp.StatusCode,
			Kind:               "failover",
			Message:            upstream.ExtractErrorMessage(respBody),
		})
		return nil, &forward.UpstreamFailoverError{
			StatusCode:              resp.StatusCode,
			ResponseBody:            respBody,
			RetryableOnSameProvider: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode),
		}
	}

	// 其余错误按原协议输出。
	return r.handleError(ctx, output, state, provider, resp, false, mappedModel)
}

func (r *Runtime) bedrockOptions(ctx context.Context, output HTTPBoundary, state *AttemptState, provider *gatewayprovider.ExecutionProvider, modelID, region string, stream bool, signer *bedrock.BedrockSigner, apiKey, proxyURL string) (bedrock.RequestOptions, bedrock.RetryPolicy) {
	options := bedrock.RequestOptions{ModelID: modelID, Region: region, Stream: stream, Signer: signer, APIKey: apiKey, APIKeyMode: provider.View().IsBedrockAPIKey(), Do: func(req *http.Request) (*http.Response, error) {
		return r.dependencies.Transport.DoWithTLS(req, proxyURL, provider.Record.ID, provider.Record.Concurrency, nil)
	}}
	policy := bedrock.RetryPolicy{MaxAttempts: maxRetryAttempts, MaxElapsed: maxRetryElapsed, Delay: forward.RetryDelay, ShouldRetry: func(status int) bool { return forward.ShouldRetry(provider.View().IsOAuth(), status) }, ReadErrorBody: r.readErrorBody, TransportError: func(err error, url string) error {
		return r.transportError(ctx, output, provider, err, forward.Notice{UpstreamURL: logredact.SafeUpstreamURL(url)})
	}, ObserveRetry: func(resp *http.Response, body []byte, url string, attempt int, delay time.Duration) {
		detail := ""
		if r.options.LogErrorBody {
			detail = logredact.TruncateUTF8(string(body), r.options.LogErrorBodyMaxBytes)
		}
		output.Observe(forward.Notice{Platform: provider.Record.Platform, ProviderID: provider.Record.ID, ProviderName: provider.Record.Name, UpstreamStatusCode: resp.StatusCode, UpstreamURL: logredact.SafeUpstreamURL(url), Kind: "retry", Message: upstream.ExtractErrorMessage(body), Detail: detail})
		logging.LegacyPrintf("service.gateway", "[Bedrock] provider %d: upstream error %d, retry %d/%d after %v", provider.Record.ID, resp.StatusCode, attempt, maxRetryAttempts, delay)
	}}
	return options, policy
}

// bedrockBetaTokens 依次校验 Header、转换平台 token，并再次校验转换结果。
func (r *Runtime) bedrockBetaTokens(ctx context.Context, target *gatewayprovider.ExecutionProvider, header string, body []byte, model string, rules ...modelcatalog.ExecutionRules) ([]string, error) {
	policy := r.evaluateBeta(ctx, target, header, model)
	if policy.BlockErr != nil {
		return nil, policy.BlockErr
	}
	tokens := bedrock.ResolveBedrockBetaTokens(header, body, model, rules...)
	if err := r.checkBetaTokens(ctx, tokens, target, model); err != nil {
		return nil, err
	}
	return anthropic.FilterBetaTokens(tokens, policy.FilterSet), nil
}

// PrepareBedrockCompatibility 在分组映射后清理请求体，并原地更新 Header。
func (r *Runtime) PrepareBedrockCompatibility(ctx context.Context, headers http.Header, body []byte, model string, target *gatewayprovider.ExecutionProvider, groupID *int64) []byte {
	if groupID == nil || r.dependencies.GroupPolicies == nil {
		return body
	}
	policy, err := r.dependencies.GroupPolicies.GetGroupPolicy(ctx, *groupID)
	if err != nil || policy == nil || !policy.IsBedrockCCCompatEnabled(target.Record.Platform) {
		return body
	}
	body = bedrock.SanitizeBedrockCCFields(body)
	body = bedrock.SanitizeBedrockThinking(body, model)
	body = bedrock.SanitizeBedrockToolUseIDs(body)
	body = bedrock.SanitizeBedrockCCBetaTokens(body, model)
	if beta := headers.Get("anthropic-beta"); beta != "" {
		if filtered := bedrock.ResolveBedrockBetaTokens(beta, body, model); len(filtered) > 0 {
			headers.Set("anthropic-beta", strings.Join(filtered, ", "))
		} else {
			headers.Del("anthropic-beta")
		}
	}
	return body
}
