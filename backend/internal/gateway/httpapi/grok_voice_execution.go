package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	mediaprovider "github.com/TokenFlux/TokenRouter/internal/gateway/media/provider"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Grok Voice endpoints return audio or resource JSON with their own response
// headers. HTTP execution passes those representations through shared provider
// transport and health handling, while Realtime connection adapters preserve
// frame bytes for the platform relay and its observed-audio billing decision.

// grokUpstreamFrames adapts the shared JSON WebSocket connection to Realtime
// frames.
type grokUpstreamFrames struct {
	conn openai.WSClientConn // active upstream WebSocket
}

// ForwardGrokVoice executes one selected Grok TTS, STT or custom-voice HTTP
// resource. It validates platform and endpoint before credentials, uses the
// inbound method when supplied or POST otherwise, and delegates body cleanup
// and audio observation to GrokVoice.
func (s *GrokExecutor) ForwardGrokVoice(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, endpoint string, body []byte, contentType string) (*forwardcore.OpenAIResult, error) {
	if s == nil || provider == nil {
		return nil, fmt.Errorf("grok voice service/provider is required")
	}
	if provider.Record.Platform != capability.PlatformGrok {
		return nil, fmt.Errorf("provider platform %s is not supported for grok voice", provider.Record.Platform)
	}
	var err error
	endpoint, baseEndpoint, err := grok.ValidateVoiceEndpoint(endpoint)
	if err != nil {
		return nil, err
	}

	token, _, err := s.Credentials.Resolve(ctx, RequestCredentialBudget(c), CredentialObserver{Context: c}, provider)
	if err != nil {
		return nil, err
	}
	targetURL, err := s.Routes.Voice(provider, endpoint)
	if err != nil {
		return nil, err
	}
	// Platform execution retains request values and server-abort state while
	// detaching client cancellation; its result still reports observed audio usage.
	upstreamCtx, release := gatewayprovider.DetachUpstreamContext(ctx)
	defer release()
	upstreamCtx = upstreamcore.WithHTTPUpstreamProfile(upstreamCtx, upstreamcore.HTTPUpstreamProfileGrok)
	method := http.MethodPost
	if c != nil && c.Request != nil && strings.TrimSpace(c.Request.Method) != "" {
		method = c.Request.Method
	}
	req, err := grok.BuildVoiceRequest(upstreamCtx, method, targetURL, token, contentType, body, func(headers http.Header) {
		if provider.View().IsGrokOAuth() && isGrokCLIProxyTarget(targetURL) {
			grok.ApplyCLIHeaders(headers)
		}
		provideradapter.ApplyProviderHeaderOverrides(gatewayprovider.ExecutionProtocolRecord(provider), headers)
	})
	if err != nil {
		return nil, err
	}
	proxyURL := ""
	if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}
	var handledResult *forwardcore.OpenAIResult
	handled := false
	target := &mediaprovider.GrokVoiceOptions{
		ProviderID:   provider.Record.ID,
		Endpoint:     endpoint,
		BaseEndpoint: baseEndpoint,
		ContentType:  contentType,
		Request:      req,
		Enter:        s.Enter,
		Do: func(req *http.Request) (*http.Response, error) {
			return s.Transport.Do(req, proxyURL, provider.Record.ID, provider.Record.Concurrency)
		},
		AfterExchange: func(elapsed time.Duration, err error) error {
			// Ops stores whole milliseconds, discarding submillisecond exchange timing.
			SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, elapsed.Milliseconds())
			if err != nil {
				return s.Failure.Handle(ctx, c, provider, err, false)
			}
			return nil
		},
		BeforeResponse: func(resp *http.Response) (bool, error) {
			if resp.StatusCode < 400 {
				return false, nil
			}
			handled = true
			var err error
			handledResult, err = s.handleGrokMediaErrorResponse(ctx, resp, c, provider, resp.Header.Get("x-request-id"), endpoint)
			return true, err
		},
		ReadBody: func(reader io.Reader) ([]byte, error) {
			return ReadUpstreamResponseBody(reader, s.Output.Options.ReadLimit, c, OpenAIResponseTooLarge)
		},
		CopyHeaders: func(dst, src http.Header) {
			WriteOpenAIPassthroughResponseHeaders(dst, src, s.Output.Headers)
		},
	}
	var sink upstreamcore.OutputSink
	if c != nil {
		sink = ResponseSink{Writer: c.Writer}
	}
	proto := protocol.ProtocolCustomVoices
	switch baseEndpoint {
	case "tts":
		proto = protocol.ProtocolTTS
	case "stt":
		proto = protocol.ProtocolSTT
	}
	result, err := (mediaprovider.GrokVoice{Options: *target}).Execute(upstreamCtx, upstreamcore.AttemptInput{Protocol: proto, Body: body}, sink)
	if handled {
		return handledResult, err
	}
	if err != nil {
		return nil, err
	}
	return &forwardcore.OpenAIResult{
		RequestID:       gatewayprovider.StableAudioBillingRequestID(result.RequestID),
		UpstreamHeaders: result.UpstreamHeaders,
		Model:           result.Model,
		UpstreamModel:   result.UpstreamModel,
		Duration:        result.Duration,
		AudioUsage:      result.AudioUsage,
	}, nil
}

// OpenGrokRealtime opens the selected Grok provider's Voice WebSocket using
// supplied token and model. The returned session owns the connection and
// activity release; callers close it after relay or preparation failure.
func (s *GrokExecutor) OpenGrokRealtime(ctx context.Context, provider *gatewayprovider.ExecutionProvider, token, model string) (*grok.RealtimeSession, error) {
	if s == nil || provider == nil || provider.Record.Platform != capability.PlatformGrok {
		return nil, fmt.Errorf("grok realtime provider is required")
	}
	base, err := s.Routes.Voice(provider, "realtime")
	if err != nil {
		return nil, err
	}
	return mediaprovider.DialRealtime(ctx, s.grokRealtimeOptions(provider, base, token, model))
}

// HandleGrokRealtimeUpstreamError observes a failed Realtime handshake before
// downstream upgrade. Nonpositive status becomes 502, and this hook records
// provider health while leaving the caller to present the handshake failure.
func (s *GrokExecutor) HandleGrokRealtimeUpstreamError(ctx context.Context, provider *gatewayprovider.ExecutionProvider, statusCode int, body []byte) {
	if statusCode <= 0 {
		statusCode = http.StatusBadGateway
	}
	// The caller already owns the failed handshake's response and retry decision.
	// This observation hook discards the returned policy after recording provider health.
	_ = gatewayprovider.ApplyGrokExecutionHealth(ctx, s.Health, provider, statusCode, nil, body, "")
}

// ReadFrame reads the shared connection's message bytes and labels them as
// text. The interface supplies no incoming frame-kind distinction, and the
// connection's error is returned for relay shutdown.
func (c grokUpstreamFrames) ReadFrame(ctx context.Context) (upstreamcore.FrameKind, []byte, error) {
	data, err := c.conn.ReadMessage(ctx)
	return upstreamcore.FrameText, data, err
}

// WriteFrame sends the supplied bytes as raw JSON through the shared
// connection. It ignores FrameKind, so relay callers supply JSON message data
// that the connection can encode and deliver.
func (c grokUpstreamFrames) WriteFrame(ctx context.Context, _ upstreamcore.FrameKind, data []byte) error {
	return c.conn.WriteJSON(ctx, json.RawMessage(data))
}

// Close releases the adapted upstream connection through its own close method.
func (c grokUpstreamFrames) Close() error { return c.conn.Close() }

// grokRealtimeOptions binds provider route, proxy, TLS and headers to the
// shared WebSocket dialer. OAuth additionally supplies CLI headers; the
// resulting options preserve selected-provider execution for the platform's
// Realtime session.
func (s *GrokExecutor) grokRealtimeOptions(provider *gatewayprovider.ExecutionProvider, base, token, model string) mediaprovider.RealtimeOptions {
	proxyURL := ""
	if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
		proxyURL = provider.Record.Proxy.URL()
	}
	options := mediaprovider.RealtimeOptions{
		BaseURL:      base,
		Token:        token,
		Model:        model,
		ApplyHeaders: gatewayprovider.BindExecutionHeaders(provider),
		Enter:        s.Enter,
		Dial: func(ctx context.Context, target string, headers http.Header) (upstreamcore.FrameConn, int, error) {
			conn, status, _, err := s.Dialer.Dial(ctx, target, headers, proxyURL, s.TLSProfile(provider))
			if conn == nil {
				return nil, status, err
			}
			return grokUpstreamFrames{conn}, status, err
		},
	}
	if provider.View().IsGrokOAuth() {
		options.CLIHeaders = grok.ApplyCLIHeaders
	}
	return options
}

// RelayGrokRealtimeFrames delegates bidirectional relay and its audio-observed
// result to Grok. Callers own inbound upgrade, concurrency slots and closing
// the supplied frame connections after the relay returns.
func (s *GrokExecutor) RelayGrokRealtimeFrames(ctx context.Context, client, server upstreamcore.FrameConn) (bool, error) {
	return grok.RelayRealtime(ctx, client, server)
}
