package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

const (
	openAIWSClientReadLimitBytesDefault     int64 = 64 * 1024 * 1024
	openAIWSHTTPBridgeThresholdBytesDefault int64 = 15 * 1024 * 1024
	openAIWSHTTPBridgeErrorBodyLimitBytes         = 64 * 1024
)

// openAIWSToolCallReplayCollector 收集上游输出里的工具调用上下文，供后续 bridge turn 重放。
type openAIWSToolCallReplayCollector struct {
	items    []json.RawMessage
	seen     map[string]struct{}
	allItems []json.RawMessage
	allSeen  map[string]struct{}
}

func decodeOpenAIWSHTTPBridgeLoweredTools(raw json.RawMessage) []any {
	if len(raw) == 0 {
		return nil
	}
	var tools []any
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil
	}
	return tools
}

func openAIWSHTTPBridgeRawField(body []byte, name string) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, false
	}
	raw, present := fields[name]
	return append(json.RawMessage(nil), raw...), present
}

func openAIWSHTTPBridgeToolUpstreamName(provider *gatewayprovider.ExecutionProvider) string {
	if provider != nil && provider.Record.Platform == capability.PlatformGrok {
		return "Grok WS HTTP bridge"
	}
	return "OpenAI WS HTTP bridge"
}

// ResolveOpenAIWSClientFirstMessageTimeout 返回生效的客户端入站首消息截止时间。
func ResolveOpenAIWSClientFirstMessageTimeout(cfg *OpenAIWSOptions) time.Duration {
	seconds := 30
	if cfg != nil && cfg.ClientFirstMessageTimeoutSeconds > 0 {
		seconds = cfg.ClientFirstMessageTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// ResolveOpenAIWSClientReadLimitBytes 返回入站客户端 WS 单帧读取上限。
func ResolveOpenAIWSClientReadLimitBytes(cfg *OpenAIWSOptions) int64 {
	if cfg == nil || cfg.ClientReadLimitBytes <= 0 {
		return openAIWSClientReadLimitBytesDefault
	}
	return cfg.ClientReadLimitBytes
}

// openAIWSHTTPBridgeEnabled 判断是否允许过大首帧走 HTTP bridge。
func (s *OpenAIWebSocketExecutor) openAIWSHTTPBridgeEnabled() bool {
	return s != nil
}

// openAIWSHTTPBridgeThresholdBytes 返回触发 HTTP bridge 的 payload 阈值。
func (s *OpenAIWebSocketExecutor) openAIWSHTTPBridgeThresholdBytes() int64 {
	if s == nil || s.Options == nil || s.Options.HTTPBridgeThresholdBytes <= 0 {
		return openAIWSHTTPBridgeThresholdBytesDefault
	}
	return s.Options.HTTPBridgeThresholdBytes
}

// shouldBridgeOpenAIWSHTTP 判断当前 WS 首帧是否应改用 HTTP Responses 上游。
func (s *OpenAIWebSocketExecutor) shouldBridgeOpenAIWSHTTP(provider *gatewayprovider.ExecutionProvider, payloadBytes int, previousResponseID string) bool {
	if provider != nil && provider.Record.Platform == capability.PlatformGrok {
		return true
	}
	if !s.openAIWSHTTPBridgeEnabled() {
		return false
	}
	if strings.TrimSpace(previousResponseID) != "" {
		return false
	}
	threshold := s.openAIWSHTTPBridgeThresholdBytes()
	return threshold > 0 && int64(payloadBytes) >= threshold
}

// shouldBridgeOpenAIWSPassthroughFirstMessage 判断透传首帧是否应切换到 HTTP bridge。
func (s *OpenAIWebSocketExecutor) shouldBridgeOpenAIWSPassthroughFirstMessage(provider *gatewayprovider.ExecutionProvider, payload []byte) bool {
	if provider != nil && provider.Record.Platform == capability.PlatformGrok {
		return true
	}
	if !s.openAIWSHTTPBridgeEnabled() || int64(len(payload)) < s.openAIWSHTTPBridgeThresholdBytes() {
		return false
	}
	if !json.Valid(payload) {
		return false
	}

	i := skipOpenAIWSJSONSpace(payload, 0)
	if i >= len(payload) || payload[i] != '{' {
		return false
	}
	i++
	eventType := "response.create"
	previousResponseID := ""
	typeSeen, previousResponseIDSeen := false, false
	for {
		i = skipOpenAIWSJSONSpace(payload, i)
		if payload[i] == '}' {
			break
		}
		keyStart := i
		keyEnd := scanOpenAIWSJSONString(payload, keyStart)
		i = skipOpenAIWSJSONSpace(payload, keyEnd)
		i++ // json.Valid 已保证此处为冒号。
		i = skipOpenAIWSJSONSpace(payload, i)
		valueStart := i
		i = skipOpenAIWSJSONValue(payload, i)

		key := ""
		// 这些字段解码后最多约 20 字节，检查编码长度后再分配键的内存。
		if keyEnd-keyStart <= 128 {
			_ = json.Unmarshal(payload[keyStart:keyEnd], &key)
		}
		switch key {
		case "type":
			if typeSeen {
				return false
			}
			typeSeen = true
			var value *string
			if err := json.Unmarshal(payload[valueStart:i], &value); err != nil {
				return false
			}
			if value == nil || strings.TrimSpace(*value) == "" {
				eventType = "response.create"
			} else {
				eventType = strings.TrimSpace(*value)
			}
		case "previous_response_id":
			if previousResponseIDSeen {
				return false
			}
			previousResponseIDSeen = true
			var value *string
			if err := json.Unmarshal(payload[valueStart:i], &value); err != nil {
				return false
			}
			if value != nil {
				previousResponseID = strings.TrimSpace(*value)
			}
		}
		i = skipOpenAIWSJSONSpace(payload, i)
		if payload[i] == ',' {
			i++
		}
	}
	return eventType == "response.create" && previousResponseID == ""
}

func skipOpenAIWSJSONSpace(payload []byte, i int) int {
	for i < len(payload) {
		switch payload[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

func scanOpenAIWSJSONString(payload []byte, i int) int {
	for i++; i < len(payload); i++ {
		switch payload[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(payload)
}

func skipOpenAIWSJSONValue(payload []byte, i int) int {
	if payload[i] == '"' {
		return scanOpenAIWSJSONString(payload, i)
	}
	if payload[i] != '{' && payload[i] != '[' {
		for i < len(payload) && payload[i] != ',' && payload[i] != '}' {
			i++
		}
		return i
	}
	depth := 0
	for ; i < len(payload); i++ {
		switch payload[i] {
		case '"':
			i = scanOpenAIWSJSONString(payload, i) - 1
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(payload)
}

// prepareOpenAIWSHTTPBridgeBody 将 response.create WS payload 转成 HTTP Responses body。
func prepareOpenAIWSHTTPBridgeBody(provider *gatewayprovider.ExecutionProvider, payload []byte) ([]byte, error) {
	var body map[string]any
	if err := wirejson.DecodeUseNumber(payload, &body); err != nil {
		return nil, err
	}
	if body == nil {
		return nil, errors.New("response.create payload must be a JSON object")
	}
	delete(body, "type")
	delete(body, "generate")
	delete(body, "previous_response_id")
	body["stream"] = true
	return json.Marshal(body)
}

// AddEvent 从上游事件中提取可重放的 function_call 项。
func (c *openAIWSToolCallReplayCollector) AddEvent(eventType string, message []byte) {
	switch strings.TrimSpace(eventType) {
	case "response.output_item.done":
		item := gjson.GetBytes(message, "item")
		c.addAllItem(item)
		c.addItem(item)
	case "response.completed", "response.done":
		output := gjson.GetBytes(message, "response.output")
		if !output.IsArray() {
			return
		}
		for _, item := range output.Array() {
			c.addAllItem(item)
			c.addItem(item)
		}
	}
}

// Items 返回已收集工具调用上下文的拷贝。
// Items/AllItems 返回浅拷贝头数组；正文由 collector 独立分配且此后不可变，
// 调用方按 replay 所有权不变式共享持有。
func (c *openAIWSToolCallReplayCollector) Items() []json.RawMessage {
	return slices.Clone(c.items)
}

// AllItems 返回完整输出项，供提供商切换时重建当前回合上下文。
func (c *openAIWSToolCallReplayCollector) AllItems() []json.RawMessage {
	return slices.Clone(c.allItems)
}

func (c *openAIWSToolCallReplayCollector) addAllItem(item gjson.Result) {
	if !item.Exists() || item.Type != gjson.JSON {
		return
	}
	raw := strings.TrimSpace(item.Raw)
	if raw == "" || !strings.HasPrefix(raw, "{") || strings.TrimSpace(item.Get("type").String()) == "" {
		return
	}
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		key = strings.TrimSpace(item.Get("call_id").String())
	}
	if key == "" {
		key = raw
	}
	if c.allSeen == nil {
		c.allSeen = make(map[string]struct{})
	}
	if _, ok := c.allSeen[key]; ok {
		return
	}
	c.allSeen[key] = struct{}{}
	c.allItems = append(c.allItems, json.RawMessage(raw))
}

func (c *openAIWSToolCallReplayCollector) addItem(item gjson.Result) {
	if !item.Exists() || item.Type != gjson.JSON {
		return
	}
	raw := strings.TrimSpace(item.Raw)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return
	}
	if !openai.IsCodexToolCallContextItemType(item.Get("type").String()) {
		return
	}
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		key = strings.TrimSpace(item.Get("call_id").String())
	}
	if key == "" {
		key = raw
	}
	if c.seen == nil {
		c.seen = make(map[string]struct{})
	}
	if _, ok := c.seen[key]; ok {
		return
	}
	c.seen[key] = struct{}{}
	c.items = append(c.items, json.RawMessage(raw))
}

func buildOpenAIWSHTTPBridgeErrorEvent(statusCode int, message string) []byte {
	message = strings.TrimSpace(message)
	if message == "" {
		message = http.StatusText(statusCode)
	}
	if message == "" {
		message = "upstream request failed"
	}
	event := map[string]any{
		"type":   "error",
		"status": statusCode,
		"error": map[string]any{
			"type":    "upstream_error",
			"message": message,
		},
	}
	body, err := json.Marshal(event)
	if err != nil {
		return []byte(`{"type":"error","error":{"type":"upstream_error","message":"upstream request failed"}}`)
	}
	return body
}

// buildOpenAIWSHTTPBridgeFailedEvent 将没有正式 response.failed 事件的上游错误
// 封装成客户端可消费的 Responses 终止事件，并保留上游错误码和消息。
func buildOpenAIWSHTTPBridgeFailedEvent(responseID, model string, source []byte, fallbackMessage string) []byte {
	errorType := strings.TrimSpace(gjson.GetBytes(source, "error.type").String())
	if errorType == "" {
		errorType = strings.TrimSpace(gjson.GetBytes(source, "response.error.type").String())
	}
	code := strings.TrimSpace(gjson.GetBytes(source, "error.code").String())
	if code == "" {
		code = strings.TrimSpace(gjson.GetBytes(source, "response.error.code").String())
	}
	if code == "" {
		code = "upstream_error"
	}
	message := upstreamopenai.ExtractOpenAISSEErrorMessage(source)
	if message == "" {
		message = strings.TrimSpace(fallbackMessage)
	}
	if message == "" {
		message = "Upstream response failed"
	}
	errorBody := map[string]any{"code": code, "message": message}
	if errorType != "" {
		errorBody["type"] = errorType
	}
	response := map[string]any{
		"id": responseID, "object": "response", "status": "failed",
		"output": []any{}, "error": errorBody,
	}
	if model = strings.TrimSpace(model); model != "" {
		response["model"] = model
	}
	body, err := json.Marshal(map[string]any{"type": "response.failed", "response": response})
	if err != nil {
		return []byte(`{"type":"response.failed","response":{"status":"failed","output":[],"error":{"code":"upstream_error","message":"Upstream response failed"}}}`)
	}
	return body
}

// proxyOpenAIWSHTTPBridgeTurn 使用 HTTP Responses 上游完成一个 WS ingress turn，并把 SSE 事件转回 WS 消息。
func (s *OpenAIWebSocketExecutor) proxyOpenAIWSHTTPBridgeTurn(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	token string,
	payload []byte,
	payloadBytes int,
	originalModel string,
	args ...any,
) (*forward.OpenAIResult, error) {
	responseModelObserver := gatewayhttp.BeginUpstreamResponseModelObservation(c)
	var routingModel, imageBillingModel, imageSizeTier, imageInputSize, grokCacheIdentity string
	var turn int
	var writeClientMessage func([]byte) error
	var routerMatch []egress.TLSFingerprintRouterMatchResult
	var stringArgs []string
	for _, arg := range args {
		switch value := arg.(type) {
		case string:
			stringArgs = append(stringArgs, value)
		case int:
			turn = value
		case func([]byte) error:
			writeClientMessage = value
		case egress.TLSFingerprintRouterMatchResult:
			routerMatch = append(routerMatch, value)
		case []egress.TLSFingerprintRouterMatchResult:
			routerMatch = append(routerMatch, value...)
		}
	}
	if len(stringArgs) >= 5 {
		routingModel, imageBillingModel, imageSizeTier, imageInputSize, grokCacheIdentity = stringArgs[0], stringArgs[1], stringArgs[2], stringArgs[3], stringArgs[4]
	} else if len(stringArgs) == 4 {
		imageBillingModel, imageSizeTier, imageInputSize, grokCacheIdentity = stringArgs[0], stringArgs[1], stringArgs[2], stringArgs[3]
	} else {
		return nil, errors.New("invalid websocket HTTP bridge arguments")
	}
	if s == nil {
		return nil, errors.New("service is nil")
	}
	if provider == nil {
		return nil, errors.New("provider is nil")
	}
	if writeClientMessage == nil {
		return nil, errors.New("client websocket writer is nil")
	}
	body, err := prepareOpenAIWSHTTPBridgeBody(provider, payload)
	if err != nil {
		return nil, fmt.Errorf("prepare http bridge body: %w", err)
	}
	grokIntentSourceBody := append([]byte(nil), body...)
	_, grokExplicitToolsField := openAIWSHTTPBridgeRawField(grokIntentSourceBody, "tools")
	grokExplicitToolIntent := provider.Record.Platform == capability.PlatformGrok && grok.HasGrokResponsesToolIntent(grokIntentSourceBody)
	var clientToolMapping bridge.ResponsesClientToolMapping
	functionToolUpstream := (provider.Record.Platform == capability.PlatformOpenAI && provider.Record.Type == capability.ProviderTypeAPIKey) || provider.Record.Platform == capability.PlatformGrok
	if functionToolUpstream {
		if provider.Record.Platform == capability.PlatformGrok {
			body, err = gatewayprovider.GrokBodyCodec().SanitizeGrokResponsesInput(body)
			if err != nil {
				return nil, fmt.Errorf("sanitize Grok WS HTTP bridge input: %w", err)
			}
		}
		inheritedState, _ := gatewayhttp.OpenAIWSHTTPBridgeToolStateFromContext(c)
		inheritedLoweredTools := decodeOpenAIWSHTTPBridgeLoweredTools(inheritedState.LoweredTools)
		body, clientToolMapping, err = bridge.AdaptResponsesClientToolsJSONWithMapping(
			body,
			openAIWSHTTPBridgeToolUpstreamName(provider),
			inheritedState.ClientMapping,
			inheritedLoweredTools,
		)
		if err != nil {
			return nil, fmt.Errorf("adapt %s client tools: %w", openAIWSHTTPBridgeToolUpstreamName(provider), err)
		}
		if provider.Record.Platform == capability.PlatformGrok && !grokExplicitToolsField && !grokExplicitToolIntent && len(inheritedLoweredTools) > 0 && grok.HasGrokResponsesToolIntent(body) {
			// 本轮省略 tools 时，缓存路由使用继承后的声明识别客户端函数。
			grokIntentSourceBody = append(grokIntentSourceBody[:0], body...)
		}
		loweredTools := inheritedState.LoweredTools
		if currentTools, present := openAIWSHTTPBridgeRawField(body, "tools"); present {
			loweredTools = currentTools
		}
		gatewayhttp.SetOpenAIWSHTTPBridgeToolState(c, requeststate.WSBridgeTools{
			ClientMapping: clientToolMapping,
			LoweredTools:  loweredTools,
		})
	}
	// 预热先保存工具声明和名称映射，后续省略 tools 的轮次据此续接。
	if openai.IsWSWarmupPayload(payload) {
		return completeHTTPBridgeWarmup(originalModel, writeClientMessage)
	}
	if s.Requests == nil || s.Requests.Transport == nil {
		return nil, errors.New("openai http upstream is nil")
	}
	responsesLite := provider.Record.Platform == capability.PlatformOpenAI && gatewayprovider.ImageIntent().IsOpenAIResponsesLiteWebSocketPayload(payload)
	if responsesLite {
		liteBody, changed, liteErr := gatewayprovider.NormalizeResponsesLiteForProvider(provider.View(), body)
		if liteErr != nil {
			return nil, fmt.Errorf("normalize http bridge Lite body: %w", liteErr)
		}
		if changed {
			body = liteBody
		}
	}
	billingModel := ""
	mappedModel := ""
	if provider.Record.Platform == capability.PlatformGrok {
		billingModel, mappedModel = resolveGrokWSModels(provider, body, routingModel)
	} else if routingModel != "" {
		billingModel = gatewayprovider.ExecutionModelPolicy(provider).Mapped(routingModel)
		mappedModel = gatewayprovider.ExecutionModelPolicy(provider).NormalizeOpenAI(billingModel)
	}
	// 客户端提供模型时回写该值，缺省时保留默认模型。
	needModelReplace := routingModel != "" && mappedModel != "" && mappedModel != originalModel
	var mappedModelBytes []byte
	if needModelReplace {
		mappedModelBytes = []byte(mappedModel)
	}

	if provider.Record.Platform == capability.PlatformGrok {
		upstreamModel := resolveGrokWSUpstreamModel(provider, body, originalModel)
		body, err = gatewayprovider.GrokBodyCodec().PatchGrokResponsesBody(body, upstreamModel)
		if err != nil {
			return nil, err
		}
		grokMixedCacheIntentBody := append([]byte(nil), body...)
		body, err = grok.ApplyGrokResponsesCacheIdentity(body, grokIntentSourceBody, grokCacheIdentity, provider.View().IsGrokOAuth())
		if err != nil {
			return nil, fmt.Errorf("apply grok prompt cache identity: %w", err)
		}
		body, err = gatewayhttp.ApplyGrokFreeRequestToolCacheRoute(c, body, grokMixedCacheIntentBody, provider, grokCacheIdentity)
		if err != nil {
			return nil, fmt.Errorf("apply grok Free function-tool cache route: %w", err)
		}
	}
	actualModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if actualModel == "" {
		actualModel = gatewayprovider.ExecutionModelPolicy(provider).CanonicalSchedulingModel(originalModel)
	}
	if actualModel != "" {
		mappedModel = actualModel
	}
	gatewayhttp.SetOpsUpstreamModel(c, actualModel)
	needModelReplace = originalModel != "" && mappedModel != "" && mappedModel != originalModel
	if needModelReplace {
		mappedModelBytes = []byte(mappedModel)
	}

	buildUpstreamRequest := func(requestBody []byte) (*http.Request, error) {
		upstreamCtx, releaseUpstreamCtx := gatewayprovider.DetachUpstreamContext(ctx)
		defer releaseUpstreamCtx()
		var upstreamReq *http.Request
		var buildErr error
		if provider.Record.Platform == capability.PlatformGrok {
			upstreamReq, buildErr = s.Grok.BuildResponsesRequest(upstreamCtx, c, provider, requestBody, token, grokCacheIdentity, true)
		} else {
			upstreamReq, buildErr = s.Requests.BuildPassthrough(upstreamCtx, c, provider, requestBody, token, routerMatch...)
		}
		if buildErr != nil {
			return nil, buildErr
		}
		if provider.Record.Platform != capability.PlatformGrok && responsesLite {
			upstreamReq.Header.Set(media.ResponsesLiteHeader, "true")
		}
		return upstreamReq, nil
	}

	proxyURL := ""
	if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}
	if c != nil {
		c.Set("openai_passthrough", true)
		c.Set("openai_ws_http_bridge", true)
	}

	turnStart := time.Now()
	rejectedFieldRetryState := upstreamopenai.NewOpenAIResponsesRejectedFieldRetryState(body)
	var resp *http.Response
	for {
		upstreamReq, buildErr := buildUpstreamRequest(body)
		if buildErr != nil {
			return nil, buildErr
		}
		resp, err = s.Requests.Transport.DoWithTLS(upstreamReq, proxyURL, provider.Record.ID, provider.Record.Concurrency, s.Requests.TLSProfile(provider, routerMatch...))
		if err != nil {
			if turn == 1 {
				return nil, s.Requests.Failure.Handle(ctx, c, provider, err, true)
			}
			safeErr := logredact.SanitizeUpstreamQueries(err.Error())
			clientError := buildOpenAIWSHTTPBridgeErrorEvent(http.StatusBadGateway, "Upstream request failed")
			if writeErr := writeClientMessage(clientError); writeErr == nil {
				markOpenAIWSClientVisibleFailure(c, "error", clientError)
			}
			return nil, fmt.Errorf("upstream http bridge request failed: %s", safeErr)
		}
		if resp.StatusCode < 400 {
			break
		}

		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, openAIWSHTTPBridgeErrorBodyLimitBytes))
		_ = resp.Body.Close()
		gatewayhttp.MarkOpenAICyberPolicyEvent(c, respBody, resp.StatusCode, nil)
		if resp.StatusCode == http.StatusBadRequest &&
			upstream.ExtractErrorCode(respBody) == gatewayhttp.OpenAIInvalidEncryptedContentReason {
			s.Lineage.MarkPayload(
				c, body, "ingress_ws_http_bridge_invalid_encrypted_lineage_mark", provider.Record.ID, turn,
			)
		}
		retryBody, retryReason, changed, retryErr := upstreamopenai.NormalizeOpenAIResponsesRejectedFieldRetryBody(resp.StatusCode, body, respBody)
		if retryErr != nil {
			return nil, fmt.Errorf("normalize websocket http bridge rejected field retry: %w", retryErr)
		}
		if changed && rejectedFieldRetryState.Allow(retryBody) {
			gatewayprovider.LogOpenAIWSModeInfo(
				"ingress_ws_http_bridge_rejected_field_retry provider_id=%d turn=%d reason=%s",
				provider.Record.ID,
				turn, gatewayprovider.TruncateOpenAIWSLogValue(retryReason, gatewayprovider.OpenAIWSLogValueMaxLen),
			)
			body = retryBody
			payloadBytes = len(body)
			continue
		}

		upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(respBody)))
		if upstreamMsg == "" {
			upstreamMsg = http.StatusText(resp.StatusCode)
		}
		requestScopedError := gatewayprovider.OpenAIWSHTTPBridgeRequestScopedError(provider, resp.StatusCode, upstreamMsg, respBody)
		decision := providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyNone}
		defaultFailover := gatewayprovider.ShouldFailoverOpenAIResponse(resp.StatusCode, upstreamMsg, respBody)
		if provider.Record.Platform == capability.PlatformGrok {
			defaultFailover = gatewayprovider.ShouldFailoverGrokResponse(resp.StatusCode, respBody)
		}
		if !requestScopedError {
			if provider.Record.Platform == capability.PlatformGrok {
				decision = gatewayprovider.ApplyGrokExecutionHealth(ctx, s.Output.GrokHealth, provider, resp.StatusCode, resp.Header, respBody, "", mappedModel)
			} else {
				decision = gatewayprovider.ApplyOpenAIResponseHealth(ctx, s.Output.Health, provider, resp.StatusCode, resp.Header, respBody, false, mappedModel)
			}
		}
		if decision.ShouldReturnGenericError() {
			_ = writeClientMessage(buildOpenAIWSHTTPBridgeErrorEvent(http.StatusInternalServerError, "Upstream gateway error"))
			return nil, fmt.Errorf("upstream http bridge error: status=%d (not in custom error codes)", resp.StatusCode)
		}
		if !requestScopedError && decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode, defaultFailover) &&
			(turn == 1 || resp.StatusCode == http.StatusTooManyRequests) {
			return nil, gatewayprovider.NewOpenAIUpstreamFailure(
				resp.StatusCode,
				resp.Header,
				respBody,
				upstreamMsg,
				decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode),
			)
		}
		clientError := buildOpenAIWSHTTPBridgeErrorEvent(resp.StatusCode, upstreamMsg)
		if writeErr := writeClientMessage(clientError); writeErr == nil {
			markOpenAIWSClientVisibleFailure(c, "error", clientError)
		}
		return nil, fmt.Errorf("upstream http bridge error: status=%d message=%s", resp.StatusCode, upstreamMsg)
	}
	defer func() { _ = resp.Body.Close() }()
	stopCancelBody := context.AfterFunc(ctx, func() { _ = resp.Body.Close() })
	defer stopCancelBody()
	if provider.Record.Platform == capability.PlatformGrok {
		s.Output.GrokHealth.ObserveResponse(ctx, provider.View(), resp.Header, resp.StatusCode, resolveGrokWSUpstreamModel(provider, body, originalModel))
	}

	responseID := ""
	usage := openai.ForwardUsage{}
	imageCounter := openai.NewOpenAIImageOutputCounter()
	var firstTokenMs *int
	reqStream := openai.WSPayloadBoolFromRaw(body, "stream", true)
	eventCount := 0
	tokenEventCount := 0
	terminalEventCount := 0
	replayCollector := &openAIWSToolCallReplayCollector{}
	firstEventType := ""
	lastEventType := ""
	upstreamTerminalEvent := ""
	sawDone := false
	wroteDownstream := false
	pendingClientMessages := make([][]byte, 0, 4)
	pendingClientMessageBytes := int64(0)
	capacityFailoverSuppressedLogged := false
	clientDisconnected := false
	officialOpenAIResponses := provider != nil && provider.Record.Platform == capability.PlatformOpenAI
	bareErrorPending := false
	var bareErrorPayload []byte
	bareErrorMessage := ""
	failureProviderSideEffectsApplied := false
	resultWithUsage := func() *forward.OpenAIResult {
		imageCount := imageCounter.Count()
		result := &forward.OpenAIResult{
			RequestID:                   responseID,
			ResponseID:                  responseID,
			Usage:                       usage,
			Model:                       originalModel,
			BillingModel:                billingModel,
			UpstreamModel:               mappedModel,
			UpstreamResponseServiceTier: responseModelObserver.ServiceTier(),
			UpstreamResponseModel:       responseModelObserver.Model(),
			ServiceTier:                 gatewayhttp.ResolvedOpenAIUpstreamServiceTierFromObserver(responseModelObserver, requeststate.ExtractOpenAIServiceTierFromBody(body)),
			ReasoningEffort:             gatewayprovider.ApplyThinkingEnabledFallback(requeststate.ExtractOpenAIReasoningEffortFromBody(body), body, mappedModel),
			RequestedReasoningEffort:    requeststate.CanonicalRequestedReasoningEffort(body),
			Stream:                      reqStream,
			OpenAIWSMode:                true,
			UpstreamTerminalEvent:       upstreamTerminalEvent,
			ResponseHeaders:             upstream.CloneHeader(resp.Header),
			Duration:                    time.Since(turnStart),
			FirstTokenMs:                firstTokenMs,
		}
		if replayInput := replayCollector.Items(); len(replayInput) > 0 {
			result.SetWSReplayInput(replayInput, true)
		}
		result.SetWSProviderFailoverReplayInput(replayCollector.AllItems())
		if imageCount > 0 {
			result.ImageCount = imageCount
			result.ImageSize = imageSizeTier
			result.ImageInputSize = imageInputSize
			result.ImageOutputSizes = imageCounter.Sizes()
			result.BillingModel = imageBillingModel
		}
		return result
	}

	maxLineSize := gatewayhttp.OpenAIResponseDefaultMaxLineSize
	if s.Options != nil && s.Output.Options.MaxLineSize > 0 {
		maxLineSize = s.Output.Options.MaxLineSize
	}
	if gatewayhttp.HasOpenAIResponsesClientToolMapping(clientToolMapping) {
		resp.Body = upstream.NewResponsesClientToolStreamBody(resp.Body, clientToolMapping, maxLineSize)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanBuf := httpclient.GetSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	defer httpclient.PutSSEScannerBuf64K(scanBuf)

	pendingSSEEventType := ""
	finalizeBareError := func() error {
		if !bareErrorPending {
			return nil
		}
		if !failureProviderSideEffectsApplied {
			failureProviderSideEffectsApplied = s.handleOpenAIWSFailureProviderSideEffects(ctx, provider, mappedModel, resp.Header, bareErrorPayload)
		}
		upstreamTerminalEvent = "response.failed"
		if clientDisconnected {
			return nil
		}
		clientMessage := buildOpenAIWSHTTPBridgeFailedEvent(responseID, originalModel, bareErrorPayload, bareErrorMessage)
		if rewritten, changed := upstreamopenai.SanitizeOpenAICapacityShedErrorCodeForClient(clientMessage); changed {
			clientMessage = rewritten
		}
		messages := append(pendingClientMessages, clientMessage)
		pendingClientMessages = nil
		pendingClientMessageBytes = 0
		for _, message := range messages {
			if err := writeClientMessage(message); err != nil {
				if gatewayprovider.IsOpenAIWSClientDisconnectError(err) {
					clientDisconnected = true
					return nil
				}
				return fmt.Errorf("write synthesized websocket response.failed: %w", err)
			}
			wroteDownstream = true
		}
		markOpenAIWSClientVisibleFailure(c, "response.failed", clientMessage)
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if eventType, ok := openai.ExtractSSEEventLine(line); ok {
			pendingSSEEventType = eventType
			continue
		}
		if strings.TrimSpace(line) == "" {
			pendingSSEEventType = ""
			continue
		}
		data, ok := openai.ExtractSSEDataLine(line)
		if !ok {
			continue
		}
		trimmedData := strings.TrimSpace(data)
		if trimmedData == "" {
			continue
		}
		if trimmedData == "[DONE]" {
			sawDone = true
			continue
		}

		upstreamMessage := []byte(openai.OpenAICompatPayloadWithEventType(trimmedData, pendingSSEEventType))
		if normalized, changed := openai.NormalizeCompletedImageGenerationStatus(upstreamMessage); changed {
			upstreamMessage = normalized
		}
		eventType, eventResponseID, _ := openai.ParseWSEventEnvelope(upstreamMessage)
		responseModelObserver.ObserveOpenAI(upstreamMessage, eventType)
		if responseID == "" && eventResponseID != "" {
			responseID = eventResponseID
		}
		if eventType != "" {
			eventCount++
			if firstEventType == "" {
				firstEventType = eventType
			}
			lastEventType = eventType
		}
		if openai.IsWSTokenEvent(eventType) {
			tokenEventCount++
			if firstTokenMs == nil {
				ms := int(time.Since(turnStart).Milliseconds())
				firstTokenMs = &ms
			}
		}
		if openai.WSMessageShouldParseUsage(eventType, upstreamMessage) {
			openai.ParseWSResponseUsageFromCompletedEvent(upstreamMessage, &usage)
		}
		if eventType == "error" || eventType == "response.failed" {
			gatewayhttp.MarkOpenAICyberPolicyEvent(c, upstreamMessage, http.StatusOK, &usage)
		}
		imageCounter.AddSSEData(upstreamMessage)

		if needModelReplace && len(mappedModelBytes) > 0 && openai.WSEventMayContainModel(eventType) && strings.Contains(trimmedData, mappedModel) {
			upstreamMessage = openai.ReplaceWSMessageModel(upstreamMessage, mappedModel, originalModel)
		}
		if s.Output.Corrector != nil && openai.WSEventMayContainToolCalls(eventType) && openai.WSMessageLikelyContainsToolCalls(upstreamMessage) {
			if corrected, changed := s.Output.Corrector.CorrectToolCallsInSSEBytes(upstreamMessage); changed {
				upstreamMessage = corrected
			}
		}
		replayCollector.AddEvent(eventType, upstreamMessage)

		var upstreamEventErr error
		if officialOpenAIResponses && bareErrorPending && (eventType == "response.completed" || eventType == "response.done") {
			// 成功终态覆盖此前可恢复的裸错误，并清除该错误的副作用状态。
			bareErrorPending = false
			bareErrorPayload = nil
			bareErrorMessage = ""
		}
		suppressClientMessage := bareErrorPending && eventType != "response.failed"
		requestScopedCapacity := provider.Record.Platform == capability.PlatformOpenAI &&
			(eventType == "error" || eventType == "response.failed") &&
			upstreamopenai.IsOpenAIUpstreamCapacityShedEvent(upstreamMessage)
		terminalPolicy := openAIWSTerminalPolicyDecision{
			TerminalEvent: normalizeOpenAIWSTerminalEvent(eventType),
			Decision:      providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyNone},
		}
		if openai.IsWSTerminalEvent(eventType) && !requestScopedCapacity &&
			(eventType != "response.failed" || !failureProviderSideEffectsApplied) {
			terminalPolicy = s.handleOpenAIWSTerminalTransientFailure(
				ctx,
				provider,
				mappedModel,
				resp.Header,
				upstreamMessage,
			)
		}
		if eventType == "response.failed" {
			errMessage := upstreamopenai.ExtractOpenAISSEErrorMessage(upstreamMessage)
			if errMessage == "" {
				errMessage = "upstream error event"
			}
			shouldFailover := requestScopedCapacity
			if terminalPolicy.Decision.ShouldReturnGenericError() && !requestScopedCapacity {
				upstreamMessage = buildOpenAIWSHTTPBridgeErrorEvent(http.StatusInternalServerError, "Upstream gateway error")
				upstreamEventErr = errors.New("upstream response failed with status not in custom error codes")
			} else if !requestScopedCapacity {
				shouldFailover = terminalPolicy.Decision.ShouldFailoverWithDefaults(
					gatewayprovider.ExecutionErrorPolicy(provider),
					terminalPolicy.StatusCode,
					false,
					s.shouldFailoverOpenAIWSError(provider, terminalPolicy.StatusCode, upstreamMessage),
				)
			}
			statusCode := terminalPolicy.StatusCode
			if statusCode == 0 {
				statusCode = http.StatusServiceUnavailable
			}
			if !wroteDownstream && shouldFailover &&
				(turn == 1 || statusCode == http.StatusTooManyRequests) {
				retrySame := requestScopedCapacity || terminalPolicy.Decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), statusCode)
				if !requestScopedCapacity {
					// 终止事件策略已执行，错误构造器消费其一次性状态，模型限流写入一次。
					gatewayhttp.MarkOpenAIResponseFailureEffects(c, statusCode, terminalPolicy.Decision.StopScheduling)
				}
				return nil, s.Output.NewStreamPolicyFailureWithModel(c, provider, true, resp.Header.Get("x-request-id"), resp.Header, statusCode, upstreamMessage, errMessage, retrySame, mappedModel)
			}
			if wroteDownstream && requestScopedCapacity && !capacityFailoverSuppressedLogged {
				gatewayhttp.LogOpenAICapacityFailoverSuppressed(ctx, provider, "ws_http_bridge", resp.Header.Get("x-request-id"), eventType)
				capacityFailoverSuppressedLogged = true
			}
		}
		if eventType == "error" {
			errCodeRaw, errTypeRaw, _ := openai.ParseWSErrorEventFields(upstreamMessage)
			if reason, _ := upstreamopenai.ClassifyWSErrorEventFromRaw(errCodeRaw, errTypeRaw, upstreamopenai.ExtractOpenAISSEErrorMessage(upstreamMessage)); reason == gatewayhttp.OpenAIInvalidEncryptedContentReason {
				s.Lineage.MarkPayload(
					c, body, "ingress_ws_http_bridge_invalid_encrypted_lineage_mark", provider.Record.ID, turn,
				)
			}
			_, _, errMsgRaw := openai.ParseWSErrorEventFields(upstreamMessage)
			errMessage := strings.TrimSpace(errMsgRaw)
			if errMessage == "" {
				errMessage = "upstream error event"
			}
			statusCode := openAIWSErrorPolicyStatus(upstreamMessage)
			policyStatus := statusCode
			requestScopedError := gatewayprovider.OpenAIWSHTTPBridgeRequestScopedError(provider, statusCode, errMessage, upstreamMessage)
			decision := providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyNone}
			defaultFailover := s.shouldFailoverOpenAIWSError(provider, policyStatus, upstreamMessage)
			if requestScopedCapacity {
				requestScopedError = true
				defaultFailover = true
			} else if provider.Record.Platform == capability.PlatformGrok {
				// SSE 错误事件不携带 HTTP 状态码，本地映射会把未知 xAI 错误码
				// （例如 new_sensitive）默认映射为 502；应用基于状态码的故障转移或
				// 提供商状态变更前，先按请求级 403 内容拒绝检查响应体。
				if grok.IsGrokContentPolicyRejection(http.StatusForbidden, upstreamMessage) {
					requestScopedError = true
					defaultFailover = false
				} else {
					defaultFailover = gatewayprovider.ShouldFailoverGrokResponse(statusCode, upstreamMessage)
					decision = gatewayprovider.ApplyGrokExecutionHealth(ctx, s.Output.GrokHealth, provider, statusCode, resp.Header, upstreamMessage, "", mappedModel)
				}
			} else if !requestScopedError {
				defaultFailover = s.shouldFailoverOpenAIWSError(provider, policyStatus, upstreamMessage)
				semanticHeaders := resp.Header
				if policyStatus == http.StatusTooManyRequests {
					semanticHeaders = gatewayprovider.OpenAISemantic429Headers(provider, mappedModel, semanticHeaders)
				}
				decision = gatewayprovider.ApplyOpenAIResponseHealth(ctx, s.Output.Health, provider, policyStatus, semanticHeaders, upstreamMessage, false, mappedModel)
			}
			if decision.StopScheduling {
				failureProviderSideEffectsApplied = true
			}
			if !requestScopedError && provider.Record.Platform == capability.PlatformOpenAI &&
				(policyStatus == http.StatusUnauthorized || policyStatus == http.StatusTooManyRequests || policyStatus == 529 ||
					(policyStatus == http.StatusForbidden && upstreamopenai.OpenAIStream403ProviderFailure(upstreamMessage, errMessage))) {
				// error 和 response.failed 成对出现时，前者执行提供商状态更新，后者写出客户端事件，限流状态更新一次。
				failureProviderSideEffectsApplied = true
			}
			if decision.ShouldReturnGenericError() && !requestScopedCapacity {
				upstreamMessage = buildOpenAIWSHTTPBridgeErrorEvent(http.StatusInternalServerError, "Upstream gateway error")
				upstreamEventErr = errors.New("upstream error not in custom error codes")
			} else if !wroteDownstream && (requestScopedCapacity || (!requestScopedError && decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), policyStatus, defaultFailover))) &&
				(turn == 1 || policyStatus == http.StatusTooManyRequests) {
				retrySame := requestScopedCapacity || decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), policyStatus)
				if c != nil && !requestScopedCapacity && !requestScopedError {
					gatewayhttp.MarkOpenAIResponseFailureEffects(c, policyStatus, decision.StopScheduling)
				}
				return nil, s.Output.NewStreamPolicyFailureWithModel(c, provider, true, resp.Header.Get("x-request-id"), resp.Header, policyStatus, upstreamMessage, errMessage, retrySame, mappedModel)
			}
			if wroteDownstream && requestScopedCapacity && !capacityFailoverSuppressedLogged {
				gatewayhttp.LogOpenAICapacityFailoverSuppressed(ctx, provider, "ws_http_bridge", resp.Header.Get("x-request-id"), eventType)
				capacityFailoverSuppressedLogged = true
			}
			if provider.Record.Platform == capability.PlatformGrok {
				upstreamEventErr = errors.New(errMessage)
			} else {
				bareErrorPending = true
				bareErrorPayload = append(bareErrorPayload[:0], upstreamMessage...)
				bareErrorMessage = errMessage
				suppressClientMessage = true
			}
		}
		if eventType == "response.failed" {
			bareErrorPending = false
		}

		// 客户端写出副本改写容量降载码：Codex 对 error/response.failed 中的
		// server_is_overloaded / slow_down 判致命并终止会话，改写后走客户端内置
		// 重试。提供商状态与终止事件判定（下方 handleOpenAIWSTerminalTransientFailure）
		// 仍使用未改写的 upstreamMessage。
		clientMessage := upstreamMessage
		if eventType == "error" || eventType == "response.failed" {
			if rewritten, changed := upstreamopenai.SanitizeOpenAICapacityShedErrorCodeForClient(clientMessage); changed {
				clientMessage = rewritten
			}
		}
		if !clientDisconnected && !suppressClientMessage {
			stageBeforeSemanticOutput := turn == 1 && provider.Record.Platform == capability.PlatformOpenAI && !wroteDownstream
			commitStagedMessages := !stageBeforeSemanticOutput ||
				upstreamopenai.OpenAIStreamDataStartsClientOutput(string(clientMessage), eventType) || openai.IsWSTerminalEvent(eventType)
			if stageBeforeSemanticOutput && !commitStagedMessages {
				if pendingClientMessageBytes+int64(len(clientMessage)) > upstreamopenai.OpenAIFirstOutputStageMaxBytes {
					return nil, s.Output.NewStreamPolicyFailure(
						c,
						provider,
						true,
						resp.Header.Get("x-request-id"),
						resp.Header,
						http.StatusBadGateway,
						nil,
						"OpenAI WS HTTP bridge first-output staging limit exceeded",
						false,
					)
				}
				pendingClientMessages = append(pendingClientMessages, append([]byte(nil), clientMessage...))
				pendingClientMessageBytes += int64(len(clientMessage))
			} else {
				messages := append(pendingClientMessages, clientMessage)
				pendingClientMessages = nil
				pendingClientMessageBytes = 0
				for _, message := range messages {
					if err := writeClientMessage(message); err != nil {
						if gatewayprovider.IsOpenAIWSClientDisconnectError(err) {
							clientDisconnected = true
							closeStatus, closeReason := gatewayprovider.SummarizeOpenAIWSReadCloseError(err)
							gatewayprovider.LogOpenAIWSModeInfo(
								"ingress_ws_http_bridge_client_disconnected_drain provider_id=%d turn=%d close_status=%s close_reason=%s",
								provider.Record.ID,
								turn,
								closeStatus, gatewayprovider.TruncateOpenAIWSLogValue(closeReason, gatewayprovider.OpenAIWSHeaderValueMaxLen),
							)
							break
						}
						return nil, ws.WrapIngressTurnError(
							"write_client",
							fmt.Errorf("write client websocket event: %w", err),
							wroteDownstream,
						)
					}
					wroteDownstream = true
				}
			}
		}
		if !clientDisconnected && !suppressClientMessage {
			markOpenAIWSClientVisibleFailure(c, eventType, upstreamMessage)
		}

		if upstreamEventErr != nil {
			return resultWithUsage(), upstreamEventErr
		}
		if openai.IsWSTerminalEvent(eventType) && !bareErrorPending {
			upstreamTerminalEvent = terminalPolicy.TerminalEvent
			terminalEventCount++
			firstTokenMsValue := -1
			if firstTokenMs != nil {
				firstTokenMsValue = *firstTokenMs
			}
			gatewayprovider.LogOpenAIWSModeInfo(
				"ingress_ws_http_bridge_turn_completed provider_id=%d turn=%d response_id=%s payload_bytes=%d duration_ms=%d events=%d token_events=%d terminal_events=%d first_event=%s last_event=%s first_token_ms=%d client_disconnected=%v",
				provider.Record.ID,
				turn, gatewayprovider.TruncateOpenAIWSLogValue(responseID, gatewayprovider.OpenAIWSIDValueMaxLen), payloadBytes,
				time.Since(turnStart).Milliseconds(),
				eventCount,
				tokenEventCount,
				terminalEventCount, gatewayprovider.TruncateOpenAIWSLogValue(firstEventType, gatewayprovider.OpenAIWSLogValueMaxLen), gatewayprovider.TruncateOpenAIWSLogValue(lastEventType, gatewayprovider.OpenAIWSLogValueMaxLen), firstTokenMsValue,
				clientDisconnected,
			)
			return resultWithUsage(), nil
		}
	}
	if bareErrorPending {
		if finalizeErr := finalizeBareError(); finalizeErr != nil {
			return resultWithUsage(), finalizeErr
		}
		if scanErr := scanner.Err(); scanErr != nil {
			return resultWithUsage(), fmt.Errorf("read upstream http bridge stream after error event: %w", scanErr)
		}
		return resultWithUsage(), errors.New(bareErrorMessage)
	}
	if err := scanner.Err(); err != nil {
		streamErr := fmt.Errorf("read upstream http bridge stream: %w", err)
		if turn == 1 && !wroteDownstream {
			return nil, s.Requests.Failure.Handle(ctx, c, provider, streamErr, true)
		}
		return resultWithUsage(), streamErr
	}
	terminalErr := errors.New("upstream http bridge stream ended before terminal event")
	if sawDone {
		terminalErr = errors.New("upstream http bridge stream sent [DONE] before terminal event")
	}
	if turn == 1 && !wroteDownstream {
		return nil, s.Requests.Failure.Handle(ctx, c, provider, terminalErr, true)
	}
	return resultWithUsage(), terminalErr
}

func resolveGrokWSCacheIdentity(c *gin.Context, provider *gatewayprovider.ExecutionProvider, payload []byte, routingModel string) (string, error) {
	body, err := prepareOpenAIWSHTTPBridgeBody(provider, payload)
	if err != nil {
		return "", err
	}
	upstreamModel := resolveGrokWSUpstreamModel(provider, body, routingModel)
	body, err = gatewayprovider.GrokBodyCodec().PatchGrokResponsesBody(body, upstreamModel)
	if err != nil {
		return "", err
	}
	return gatewayhttp.ResolveGrokCacheIdentity(c, body, "", upstreamModel), nil
}

func resolveGrokWSUpstreamModel(provider *gatewayprovider.ExecutionProvider, body []byte, originalModel string) string {
	_, upstreamModel := resolveGrokWSModels(provider, body, originalModel)
	return upstreamModel
}

// resolveGrokWSModels 只解析一次提供商映射与 Grok 平台规范化，供请求、错误状态和结果记录复用。
func resolveGrokWSModels(provider *gatewayprovider.ExecutionProvider, body []byte, originalModel string) (string, string) {
	requestedModel := strings.TrimSpace(originalModel)
	if requestedModel == "" {
		requestedModel = strings.TrimSpace(gjson.GetBytes(body, "model").String())
	}
	billingModel := requestedModel
	if provider != nil {
		billingModel = providercore.ResolveForwardMappedModel(gatewayprovider.ExecutionRecord(provider), requestedModel, provideradapter.ModelDefaults())
	}
	upstreamModel := gatewayprovider.ExecutionModelPolicy(provider).NormalizeOpenAI(billingModel)
	if upstreamModel == "" {
		upstreamModel = grok.DefaultResponsesModel
	}
	return billingModel, upstreamModel
}

// completeHTTPBridgeWarmup 在本地完成预热，入站会话继续保存输入供下一轮重放。
func completeHTTPBridgeWarmup(model string, write func([]byte) error) (*forward.OpenAIResult, error) {
	started := time.Now()
	responseID := "resp_ws_warmup_" + uuid.NewString()
	events, err := openai.WSWarmupEvents(responseID, model, started.Unix())
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if err := write(event); err != nil {
			return nil, err
		}
	}
	return &forward.OpenAIResult{
		LocalWarmup: true, RequestID: responseID, ResponseID: responseID, Model: model,
		Stream: true, OpenAIWSMode: true, UpstreamTerminalEvent: "response.completed", Duration: time.Since(started),
	}, nil
}
