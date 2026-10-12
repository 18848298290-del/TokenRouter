package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaymedia "github.com/TokenFlux/TokenRouter/internal/gateway/media"
	mediaprovider "github.com/TokenFlux/TokenRouter/internal/gateway/media/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

type grokMediaUsageMetadata struct {
	ResponseID           string
	Usage                openai.ForwardUsage
	Model                string
	BillingModel         string
	ImageCount           int
	ImageSize            string
	ImageInputSize       string
	ImageOutputSizes     []string
	VideoCount           int
	VideoResolution      string
	VideoDurationSeconds int
	VideoState           string
}

// xAI 异步视频状态的官方成功结构如下（docs.x.ai Video Generation）：
//
//	示例：{"status":"done","model":"grok-imagine-video-1.5","video":{"url":"...","duration":8,"respect_moderation":true}}
//
// 请求可以包含分辨率（"480p"、"720p" 或 "1080p"），完成状态不会返回该字段，
// 因此计费分辨率取自创建任务时保存的请求快照。

func ExtractGrokVideoBillingFromStatusBody(statusBody []byte, pending *gatewaymedia.GrokVideoPendingBilling, requestID string) *forwardcore.OpenAIResult {
	value := gatewaymedia.ExtractGrokVideoBillingFromStatusBody(statusBody, pending, requestID, extractGrokMediaVideoRequestID(statusBody))
	if value == nil {
		return nil
	}
	return &forwardcore.OpenAIResult{ResponseID: value.ResponseID, Model: value.Model, BillingModel: value.BillingModel, UpstreamModel: value.UpstreamModel, VideoCount: value.VideoCount, VideoResolution: value.VideoResolution, VideoDurationSeconds: value.VideoDurationSeconds}
}

func (s *GrokExecutor) ForwardGrokMedia(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	endpoint grok.GrokMediaEndpoint,
	requestID string,
	body []byte,
	contentType string,
) (*forwardcore.OpenAIResult, error) {
	startTime := time.Now()
	if provider == nil {
		return nil, fmt.Errorf("grok provider is required")
	}
	if provider.Record.Platform != capability.PlatformGrok {
		return nil, fmt.Errorf("provider platform %s is not supported for grok media", provider.Record.Platform)
	}

	token, _, err := s.Credentials.Resolve(ctx, RequestCredentialBudget(c), CredentialObserver{Context: c}, provider)
	if err != nil {
		return nil, err
	}
	if endpoint == grok.GrokMediaEndpointVideoContent {
		return s.forwardGrokMediaVideoContent(ctx, c, provider, token, requestID, startTime)
	}
	targetURL, err := s.Routes.Media(provider, endpoint, requestID)
	if err != nil {
		return nil, err
	}

	body, contentType, err = prepareGrokMediaForwardBody(endpoint, body, contentType)
	if err != nil {
		return nil, err
	}
	body, contentType, err = normalizeGrokMediaForwardBody(endpoint, body, contentType)
	if err != nil {
		return nil, err
	}
	requestInfo := gatewayprovider.GrokMediaCodec().ParseGrokMediaRequest(contentType, body)
	billingModel := requestInfo.Model
	upstreamModel := billingModel
	if endpoint.RequiresRequestBody() {
		if mappedModel := strings.TrimSpace(gatewayprovider.ExecutionModelPolicy(provider).Mapped(requestInfo.Model)); mappedModel != "" {
			billingModel = mappedModel
		}
		upstreamModel = gatewayprovider.ExecutionModelPolicy(provider).NormalizeOpenAI(billingModel)
		if upstreamModel != requestInfo.Model {
			body, contentType, err = gatewayprovider.GrokMediaCodec().RewriteGrokMediaRequestModel(body, contentType, upstreamModel)
			if err != nil {
				return nil, fmt.Errorf("rewrite grok media provider mapped model: %w", err)
			}
		}
		resolved, err := gatewayprovider.ResolveModelAttributes(ctx, provider, upstreamModel, s.Attributes)
		if err != nil {
			return nil, err
		}
		provider.Capabilities = &resolved
		modeltrace.RegisterStage(ctx, upstreamModel)
	}
	body, contentType, err = sanitizeGrokMediaForwardBody(endpoint, body, contentType)
	if err != nil {
		return nil, err
	}

	upstreamCtx, releaseUpstreamCtx := gatewayprovider.DetachUpstreamContext(ctx)
	defer releaseUpstreamCtx()
	var cliHeaders func(http.Header)
	if provider.View().IsGrokOAuth() && isGrokCLIProxyTarget(targetURL) {
		cliHeaders = grok.ApplyCLIHeaders
	}
	req, err := grok.BuildMediaRequest(upstreamCtx, endpoint, targetURL, token, contentType, body, cliHeaders, gatewayprovider.BindExecutionHeaders(provider))
	if err != nil {
		return nil, err
	}
	proxyURL := ""
	if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}
	handled := false
	var handledResult *forwardcore.OpenAIResult
	target := &mediaprovider.GrokMediaOptions{
		ProviderID: provider.Record.ID,
		Endpoint:   endpoint,
		Request:    req,
		StartedAt:  startTime,
		Enter:      s.Enter,
		Do: func(req *http.Request) (*http.Response, error) {
			return s.Transport.Do(req, proxyURL, provider.Record.ID, provider.Record.Concurrency)
		},
		AfterExchange: func(elapsed time.Duration, err error) error {
			SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, elapsed.Milliseconds())
			if err != nil {
				return s.Failure.Handle(ctx, c, provider, err, false)
			}
			return nil
		},
		BeforeResponse: func(resp *http.Response) (bool, error) {
			if resp.StatusCode >= 400 {
				handled = true
				var err error
				handledResult, err = s.handleGrokMediaErrorResponse(ctx, resp, c, provider, requeststate.FirstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")), upstreamModel)
				return true, err
			}
			s.Health.ObserveResponse(ctx, provider.View(), resp.Header, resp.StatusCode, requestInfo.Model)
			return false, nil
		},
		ReadBody: func(reader io.Reader) ([]byte, error) {
			return ReadUpstreamResponseBody(reader, s.Output.Options.ReadLimit, c, OpenAIResponseTooLarge)
		},
		CountImages: openai.CountOpenAIResponseImageOutputsFromJSONBytes,
		TransformBody: func(data []byte) []byte {
			if endpoint == grok.GrokMediaEndpointVideoStatus {
				return rewriteGrokMediaVideoContentURLs(data, requestID, GrokMediaContentProxyURL(c, requestID))
			}
			return data
		},
		CopyHeaders: func(dst, src http.Header) {
			WriteOpenAIPassthroughResponseHeaders(dst, src, s.Output.Headers)
		},
	}
	var sink upstream.OutputSink
	if c != nil {
		sink = ResponseSink{Writer: c.Writer}
	}
	protocols := map[grok.GrokMediaEndpoint]protocol.ProtocolID{
		grok.GrokMediaEndpointImagesGenerations: protocol.ProtocolImagesGenerations,
		grok.GrokMediaEndpointImagesEdits:       protocol.ProtocolImagesEdits,
		grok.GrokMediaEndpointVideosGenerations: protocol.ProtocolVideosGenerations,
		grok.GrokMediaEndpointVideosEdits:       protocol.ProtocolVideosEdits,
		grok.GrokMediaEndpointVideosExtensions:  protocol.ProtocolVideosExtensions,
		grok.GrokMediaEndpointVideoStatus:       protocol.ProtocolVideosGenerations,
	}
	result, err := (mediaprovider.GrokMedia{Options: *target}).Execute(upstreamCtx, upstream.AttemptInput{Protocol: protocols[endpoint], Body: body, ResponseModel: requestInfo.Model}, sink)
	if handled {
		return handledResult, err
	}
	if err != nil {
		if missing, ok := errors.AsType[*grok.MissingImageOutput](err); ok {
			SetOpsUpstreamError(c, http.StatusBadGateway, missing.Error(), logredact.TruncateUTF8(string(missing.Body), 512))
			return nil, &forwardcore.UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: missing.Body, ResponseHeaders: missing.Headers}
		}
		return nil, err
	}
	respBody := result.MediaBody

	usage := grokMediaUsageFromResponse(endpoint, requestInfo, respBody)
	resultModel := requestInfo.Model
	resultBillingModel := billingModel
	if endpoint == grok.GrokMediaEndpointVideoStatus {
		// 状态请求不含请求体模型，满足计费条件时使用上游状态字段。
		if m := strings.TrimSpace(usage.Model); m != "" {
			resultModel = m
		}
		if m := strings.TrimSpace(usage.BillingModel); m != "" {
			resultBillingModel = m
		}
	}
	return &forwardcore.OpenAIResult{
		RequestID: result.RequestID,

		UpstreamHeaders: result.UpstreamHeaders,

		ResponseID: usage.ResponseID,

		Usage: usage.Usage,

		Model: resultModel,

		BillingModel: resultBillingModel,

		UpstreamModel: upstreamModel,

		ResponseHeaders: result.UpstreamHeaders.Clone(),

		Duration: time.Since(startTime),

		ImageCount: usage.ImageCount,

		ImageSize: usage.ImageSize,

		ImageInputSize: usage.ImageInputSize,

		ImageOutputSizes: usage.ImageOutputSizes,

		VideoCount: usage.VideoCount,
		VideoState: usage.VideoState,

		VideoResolution: usage.VideoResolution,

		VideoDurationSeconds: usage.VideoDurationSeconds,
	}, nil
}

func (s *GrokExecutor) forwardGrokMediaVideoContent(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	token, requestID string,
	startTime time.Time,
) (*forwardcore.OpenAIResult, error) {
	statusURL, err := s.Routes.Media(provider, grok.GrokMediaEndpointVideoStatus, requestID)
	if err != nil {
		return nil, err
	}

	upstreamCtx, releaseUpstreamCtx := gatewayprovider.DetachUpstreamContext(ctx)
	defer releaseUpstreamCtx()
	proxyURL := ""
	if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}
	rangeHeader := ""
	if c != nil {
		rangeHeader = c.GetHeader("Range")
	}
	handled := false
	var handledResult *forwardcore.OpenAIResult
	resource, err := mediaprovider.OpenVideoContent(upstreamCtx, mediaprovider.VideoContentOptions{
		StatusURL: statusURL,
		RequestID: requestID,
		Token:     token,
		Range:     rangeHeader,
		Context:   upstream.WithHTTPUpstreamRedirectsDisabled,
		ContentURL: func() (string, error) {
			return s.Routes.Media(provider, grok.GrokMediaEndpointVideoContent, requestID)
		},
		ApplyHeaders: func(headers http.Header, target string) {
			if provider.View().IsGrokOAuth() && isGrokCLIProxyTarget(target) {
				grok.ApplyCLIHeaders(headers)
			}
			provideradapter.ApplyProviderHeaderOverrides(gatewayprovider.ExecutionProtocolRecord(provider), headers)
		},
		Do: func(req *http.Request) (*http.Response, error) {
			return s.Transport.Do(req, proxyURL, provider.Record.ID, provider.Record.Concurrency)
		},
		ReadStatus: func(reader io.Reader) ([]byte, error) {
			return ReadUpstreamResponseBody(reader, s.Output.Options.ReadLimit, c, OpenAIResponseTooLarge)
		},
		Latency: func(elapsed time.Duration) {
			SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, elapsed.Milliseconds())
		},
		TransportError: func(err error) error { return s.Failure.Handle(ctx, c, provider, err, false) },
		HTTPError: func(resp *http.Response, id string) error {
			handled = true
			var err error
			handledResult, err = s.handleGrokMediaErrorResponse(ctx, resp, c, provider, id, "")
			return err
		},
		Enter: s.Enter,
	})
	if handled {
		return handledResult, err
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = resource.Close() }()
	contentResp := &http.Response{StatusCode: resource.StatusCode, ContentLength: resource.ContentLength, Header: resource.Headers, Body: resource}
	contentRequestID := resource.RequestID
	statusBody := resource.StatusBody

	s.Health.ObserveResponse(ctx, provider.View(), contentResp.Header, contentResp.StatusCode, "")
	if err := WriteGrokMediaContentResponse(c, contentResp, func() { MarkResponseCommitted(c) }); err != nil {
		return nil, err
	}
	// 内容下载也是完成观测入口：状态体满足官方 done 和 video.url 条件时附加计费单位，
	// 使处理器能够按与状态轮询相同的路径领取一次计费；待计费快照由处理器合并。
	result := &forwardcore.OpenAIResult{
		RequestID:  contentRequestID,
		VideoState: gatewaymedia.GrokVideoRequestState(statusBody),

		UpstreamHeaders: contentResp.Header,

		ResponseHeaders: contentResp.Header.Clone(),

		Duration: time.Since(startTime),
	}
	if billed := ExtractGrokVideoBillingFromStatusBody(statusBody, nil, requestID); billed != nil {
		result.ResponseID = requeststate.FirstNonEmpty(billed.ResponseID, strings.TrimSpace(requestID))
		result.Model = billed.Model
		result.BillingModel = billed.BillingModel
		result.UpstreamModel = billed.UpstreamModel
		result.VideoCount = billed.VideoCount
		result.VideoResolution = billed.VideoResolution
		result.VideoDurationSeconds = billed.VideoDurationSeconds
	}
	return result, nil
}

func isGrokCLIProxyTarget(rawURL string) bool {
	return gatewayprovider.GrokMediaCodec().IsGrokCLIProxyTarget(rawURL)
}

func prepareGrokMediaForwardBody(endpoint grok.GrokMediaEndpoint, body []byte, contentType string) ([]byte, string, error) {
	return gatewayprovider.GrokMediaCodec().PrepareGrokMediaForwardBody(endpoint, body, contentType)
}

func normalizeGrokMediaForwardBody(endpoint grok.GrokMediaEndpoint, body []byte, contentType string) ([]byte, string, error) {
	return gatewayprovider.GrokMediaCodec().NormalizeGrokMediaForwardBody(endpoint, body, contentType)
}

func sanitizeGrokMediaForwardBody(endpoint grok.GrokMediaEndpoint, body []byte, contentType string) ([]byte, string, error) {
	return gatewayprovider.GrokMediaCodec().SanitizeGrokMediaForwardBody(endpoint, body, contentType)
}

func grokMediaUsageFromResponse(endpoint grok.GrokMediaEndpoint, requestInfo grok.GrokMediaRequestInfo, responseBody []byte) grokMediaUsageMetadata {
	usage, _ := openai.ExtractOpenAIUsageFromJSONBytes(responseBody)
	meta := grokMediaUsageMetadata{Usage: usage}
	switch endpoint {
	case grok.GrokMediaEndpointImagesGenerations, grok.GrokMediaEndpointImagesEdits:
		meta.ImageCount = openai.CountOpenAIResponseImageOutputsFromJSONBytes(responseBody)
		meta.ImageSize = requestInfo.SizeTier
		meta.ImageInputSize = requestInfo.Size
		meta.ImageOutputSizes = openai.CollectOpenAIResponseImageOutputSizesFromJSONBytes(responseBody)
	case grok.GrokMediaEndpointVideosGenerations, grok.GrokMediaEndpointVideosEdits, grok.GrokMediaEndpointVideosExtensions:
		// 异步视频创建阶段只保留任务 ID 和计价参数，完成轮询时再设置可计费数量。
		meta.ResponseID = extractGrokMediaVideoRequestID(responseBody)
		meta.VideoResolution = requestInfo.Resolution
		meta.VideoDurationSeconds = requestInfo.DurationSeconds
	case grok.GrokMediaEndpointVideoStatus:
		meta.VideoState = gatewaymedia.GrokVideoRequestState(responseBody)
		// 只有官方完成状态且返回视频地址时，才生成待结算的视频用量。
		if billed := ExtractGrokVideoBillingFromStatusBody(responseBody, nil, ""); billed != nil {
			meta.ResponseID = billed.ResponseID
			meta.Model = billed.Model
			meta.BillingModel = billed.BillingModel
			meta.VideoCount = billed.VideoCount
			meta.VideoResolution = billed.VideoResolution
			meta.VideoDurationSeconds = billed.VideoDurationSeconds
		}
	}
	return meta
}

func extractGrokMediaVideoRequestID(body []byte) string {
	return gatewayprovider.GrokMediaCodec().ExtractGrokMediaVideoRequestID(body)
}

func (s *GrokExecutor) handleGrokMediaErrorResponse(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	requestIDHeader string,
	requestedModel string,
) (*forwardcore.OpenAIResult, error) {
	body := s.Output.ReadErrorBody(resp)
	// 透传返回前应用提供商策略，池模式默认记录上游观测，冷却时间保持原样。
	decision := gatewayprovider.ApplyGrokExecutionHealth(ctx, s.Health, provider, resp.StatusCode, resp.Header, body, "", requestedModel)
	upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(body)))
	if upstreamMsg == "" {
		upstreamMsg = fmt.Sprintf("xAI upstream returned status %d", resp.StatusCode)
	}

	upstreamDetail := ""
	if s.Output.Options.LogUpstreamErrorBody {
		maxBytes := s.Output.Options.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = logredact.TruncateUTF8(string(body), maxBytes)
	}
	SetOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	return nil, gatewaymedia.ResolveGrokFailure(resp.StatusCode, upstreamMsg, gatewaymedia.GrokFailurePorts{
		ContentRejection: func() (bool, string) {
			if !grok.IsGrokContentPolicyRejection(resp.StatusCode, body) {
				return false, ""
			}
			return true, gatewayprovider.GrokContentPolicyClientMessage(body)
		},
		Generic: decision.ShouldReturnGenericError,
		Failover: func() bool {
			return decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode, gatewayprovider.ShouldFailoverGrokResponse(resp.StatusCode, body))
		},
		Retry: func() gatewaymedia.GrokRetry {
			retryable, delay, deadline, maximum := gatewayprovider.GrokSameProviderRetryMetadata(provider, resp.StatusCode, body)
			return gatewaymedia.GrokRetry{Retryable: retryable, PolicyRetryable: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode), Delay: delay, Deadline: deadline, Maximum: maximum}
		},
		Observe: func(kind, message string) {
			AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{Platform: provider.Record.Platform, ProviderID: provider.Record.ID, ProviderName: provider.Record.Name, UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: requestIDHeader, Kind: kind, Message: message, Detail: upstreamDetail})
		},
		Rewrite: func() (gatewaymedia.ErrorResponse, bool) {
			status, typ, message, matched := ApplyErrorPassthroughRule(c, provider.Record.Platform, resp.StatusCode, body, http.StatusBadGateway, "upstream_error", "Upstream request failed")
			return gatewaymedia.ErrorResponse{Status: status, Type: typ, Message: message}, matched
		},
		Write: func(response gatewaymedia.ErrorResponse) {
			MarkResponseCommitted(c)
			WriteGrokMediaErrorResponse(c, response.Status, response.Type, response.Message)
		},
		NewFailover: func(retry gatewaymedia.GrokRetry, transient bool) error {
			return &forwardcore.UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: body, ResponseHeaders: resp.Header.Clone(), RetryableOnSameProvider: retry.Retryable || retry.PolicyRetryable, RequestScopedTransient: transient, SameProviderRetryDelay: retry.Delay, SameProviderRetryDeadline: retry.Deadline, SameProviderRetryMax: retry.Maximum}
		},
	})
}

func rewriteGrokMediaVideoContentURLs(body []byte, requestID, proxyURL string) []byte {
	return gatewaymedia.RewriteVideoContentURLs(body, requestID, proxyURL, gatewayprovider.GrokMediaCodec().IsGrokMediaVideoContentURL)
}
