package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

type grokMediaContentUpstreamStub struct {
	request   *http.Request
	requests  []*http.Request
	response  *http.Response
	responses []*http.Response
}

// grokPoolPolicyProviderRepo records provider-state writes from Grok pool error policy.
type grokPoolPolicyProviderRepo struct {
	*grokQuotaProviderRepo
	setErrorCalls            int
	overloadedCalls          int
	modelRateLimitCalls      int
	lastModelRateLimitScope  string
	lastModelRateLimitReason string
}

// grokMediaFixture binds dependencies used by media execution tests.
func grokMediaFixture(transport httpclient.UpstreamTransport) *GrokExecutor {
	return &GrokExecutor{Credentials: gatewaytestkit.RequestCredentials(nil, nil, nil, nil), Transport: transport, Output: &OpenAIResponseOutput{Options: OpenAIResponseOptions{ReadLimit: 128 * 1024 * 1024}}, Health: &provideradapter.GrokHealth{}, Routes: gatewayprovider.GrokRoutes{Validate: (egress.OperatorURLPolicy{}).Validate}, Failure: &UpstreamTransportFailure{}}
}

func (s *grokMediaContentUpstreamStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.request = req
	s.requests = append(s.requests, req)
	if len(s.responses) > 0 {
		resp := s.responses[0]
		s.responses = s.responses[1:]
		return resp, nil
	}
	return s.response, nil
}

func (s *grokMediaContentUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, providerID, providerConcurrency)
}

func grokMediaContentTestProvider() *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 9,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "upstream-key",
				"base_url": "https://relay.example/v1",
			},
		},
	}
}

func grokMediaContentTestContext(method, target string, headers map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, target, nil)
	for name, value := range headers {
		c.Request.Header.Set(name, value)
	}
	return c, recorder
}

func grokMediaContentStatusResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestForwardGrokMediaContentUsesUpstreamCredentialAndStreamsRange(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{
		responses: []*http.Response{grokMediaContentStatusResponse(`{"status":"completed"}`), {
			StatusCode: http.StatusPartialContent,
			Header: http.Header{
				"Content-Type":   []string{"video/mp4"},
				"Content-Length": []string{"13"},
				"Content-Range":  []string{"bytes 0-12/100"},
				"Accept-Ranges":  []string{"bytes"},
				"Content-Disposition": []string{
					`attachment; filename="task-1.mp4"`,
				},
			},
			Body: io.NopCloser(strings.NewReader("video-payload")),
		}},
	}
	svc := grokMediaFixture(upstream)
	c, recorder := grokMediaContentTestContext(http.MethodGet, "https://api.example/v1/videos/task-1/content", map[string]string{
		"Range": "bytes=0-12",
	})

	result, err := svc.ForwardGrokMedia(
		context.Background(), c, grokMediaContentTestProvider(),
		grok.GrokMediaEndpointVideoContent, "task-1", nil, "",
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusPartialContent, recorder.Code)
	require.Equal(t, "video-payload", recorder.Body.String())
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "https://relay.example/v1/videos/task-1", upstream.requests[0].URL.String())
	require.Equal(t, "Bearer upstream-key", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "https://relay.example/v1/videos/task-1/content", upstream.requests[1].URL.String())
	require.Equal(t, "Bearer upstream-key", upstream.requests[1].Header.Get("Authorization"))
	require.Equal(t, "bytes=0-12", upstream.requests[1].Header.Get("Range"))
	require.Equal(t, "*/*", upstream.requests[1].Header.Get("Accept"))
	require.Equal(t, "video/mp4", recorder.Header().Get("Content-Type"))
	require.Equal(t, "13", recorder.Header().Get("Content-Length"))
	require.Equal(t, "bytes 0-12/100", recorder.Header().Get("Content-Range"))
	require.Equal(t, "bytes", recorder.Header().Get("Accept-Ranges"))
	require.Equal(t, `attachment; filename="task-1.mp4"`, recorder.Header().Get("Content-Disposition"))
	require.True(t, IsResponseCommitted(c))
}

func TestForwardGrokMediaContentStreamsFullResponseWithSafeDefaults(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{
		responses: []*http.Response{grokMediaContentStatusResponse(`{"status":"completed"}`), {
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Set-Cookie": []string{"secret=upstream"}, "X-Upstream-Secret": []string{"hidden"}},
			Body:          io.NopCloser(strings.NewReader("full-video")),
			ContentLength: -1,
		}},
	}
	svc := grokMediaFixture(upstream)
	c, recorder := grokMediaContentTestContext(http.MethodGet, "https://api.example/v1/videos/task-1/content", nil)

	_, err := svc.ForwardGrokMedia(
		context.Background(), c, grokMediaContentTestProvider(),
		grok.GrokMediaEndpointVideoContent, "task-1", nil, "",
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "full-video", recorder.Body.String())
	require.Len(t, upstream.requests, 2)
	require.Empty(t, upstream.requests[1].Header.Get("Range"))
	require.Equal(t, "application/octet-stream", recorder.Header().Get("Content-Type"))
	require.Empty(t, recorder.Header().Get("Content-Length"))
	require.Empty(t, recorder.Header().Get("Set-Cookie"))
	require.Empty(t, recorder.Header().Get("X-Upstream-Secret"))
	require.True(t, IsResponseCommitted(c))
}

func TestForwardGrokMediaContentPreservesRangeNotSatisfiable(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{
		responses: []*http.Response{grokMediaContentStatusResponse(`{"status":"completed"}`), {
			StatusCode: http.StatusRequestedRangeNotSatisfiable,
			Header: http.Header{
				"Content-Type":   []string{"text/plain"},
				"Content-Length": []string{"11"},
				"Content-Range":  []string{"bytes */100"},
				"Accept-Ranges":  []string{"bytes"},
			},
			Body: io.NopCloser(strings.NewReader("bad-range!!")),
		}},
	}
	svc := grokMediaFixture(upstream)
	c, recorder := grokMediaContentTestContext(http.MethodGet, "https://api.example/v1/videos/task-1/content", map[string]string{
		"Range": "bytes=500-600",
	})

	_, err := svc.ForwardGrokMedia(
		context.Background(), c, grokMediaContentTestProvider(),
		grok.GrokMediaEndpointVideoContent, "task-1", nil, "",
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusRequestedRangeNotSatisfiable, recorder.Code)
	require.Equal(t, "bad-range!!", recorder.Body.String())
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "bytes=500-600", upstream.requests[1].Header.Get("Range"))
	require.Equal(t, "bytes */100", recorder.Header().Get("Content-Range"))
	require.Equal(t, "bytes", recorder.Header().Get("Accept-Ranges"))
	require.True(t, IsResponseCommitted(c))
}

func TestForwardGrokMediaContentFetchesValidatedSignedURLWithoutCredentials(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{
		responses: []*http.Response{
			grokMediaContentStatusResponse(`{"status":"done","video":{"url":"https://vidgen.x.ai/signed-token/xai-video-task-1.mp4"}}`),
			{
				StatusCode: http.StatusPartialContent,
				Header: http.Header{
					"Content-Type":   []string{"video/mp4"},
					"Content-Length": []string{"13"},
					"Content-Range":  []string{"bytes 0-12/100"},
				},
				Body: io.NopCloser(strings.NewReader("video-payload")),
			},
		},
	}
	provider := grokMediaContentTestProvider()
	provider.Record.Credentials["header_override_enabled"] = true
	provider.Record.Credentials["header_overrides"] = map[string]any{"user-agent": "private-agent"}
	svc := grokMediaFixture(upstream)
	c, recorder := grokMediaContentTestContext(http.MethodGet, "https://api.example/v1/videos/task-1/content", map[string]string{
		"Range": "bytes=0-12",
	})

	_, err := svc.ForwardGrokMedia(
		context.Background(), c, provider,
		grok.GrokMediaEndpointVideoContent, "task-1", nil, "",
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusPartialContent, recorder.Code)
	require.Equal(t, "video-payload", recorder.Body.String())
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "https://relay.example/v1/videos/task-1", upstream.requests[0].URL.String())
	require.Equal(t, "Bearer upstream-key", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "private-agent", upstream.requests[0].Header.Get("User-Agent"))
	require.True(t, upstreamcore.HTTPUpstreamRedirectsDisabled(upstream.requests[0].Context()))
	require.Equal(t, "https://vidgen.x.ai/signed-token/xai-video-task-1.mp4", upstream.requests[1].URL.String())
	require.Empty(t, upstream.requests[1].Header.Get("Authorization"))
	require.Empty(t, upstream.requests[1].Header.Get("User-Agent"))
	require.Equal(t, "bytes=0-12", upstream.requests[1].Header.Get("Range"))
	require.True(t, upstreamcore.HTTPUpstreamRedirectsDisabled(upstream.requests[1].Context()))
}

func TestForwardGrokMediaContentFollowsAuthenticatedTokenRouterRelay(t *testing.T) {
	for _, statusURL := range []string{
		`/v1/videos/task-1/content`,
		`https://relay.example/v1/videos/task-1/content`,
	} {
		t.Run(statusURL, func(t *testing.T) {
			upstream := &grokMediaContentUpstreamStub{
				responses: []*http.Response{
					grokMediaContentStatusResponse(`{"status":"completed","video":{"url":"` + statusURL + `"}}`),
					{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"video/mp4"}},
						Body:       io.NopCloser(strings.NewReader("video-payload")),
					},
				},
			}
			svc := grokMediaFixture(upstream)
			c, recorder := grokMediaContentTestContext(http.MethodGet, "https://api.example/v1/videos/task-1/content", nil)

			_, err := svc.ForwardGrokMedia(
				context.Background(), c, grokMediaContentTestProvider(),
				grok.GrokMediaEndpointVideoContent, "task-1", nil, "",
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "video-payload", recorder.Body.String())
			require.Len(t, upstream.requests, 2)
			require.Equal(t, "https://relay.example/v1/videos/task-1/content", upstream.requests[1].URL.String())
			require.Equal(t, "Bearer upstream-key", upstream.requests[1].Header.Get("Authorization"))
		})
	}
}

func TestForwardGrokMediaContentRejectsUntrustedSignedURL(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{
		responses: []*http.Response{
			grokMediaContentStatusResponse(`{"status":"done","video":{"url":"http://169.` + `254.169.254/latest/meta-data"}}`),
		},
	}
	svc := grokMediaFixture(upstream)
	c, _ := grokMediaContentTestContext(http.MethodGet, "https://api.example/v1/videos/task-1/content", nil)

	_, err := svc.ForwardGrokMedia(
		context.Background(), c, grokMediaContentTestProvider(),
		grok.GrokMediaEndpointVideoContent, "task-1", nil, "",
	)

	require.ErrorContains(t, err, "unsupported video content URL")
	require.Len(t, upstream.requests, 1)
}

func TestGrokMediaSignedVideoContentURLRejectsDeceptiveOrigins(t *testing.T) {
	for _, rawURL := range []string{
		"https://vidgen.x.ai.attacker.invalid/video.mp4",
		"https://vidgen.x.ai" + "@attacker.invalid/video.mp4",
		"https://vidgen.x.ai:444/video.mp4",
		"http://vidgen.x.ai/video.mp4",
	} {
		t.Run(rawURL, func(t *testing.T) {
			_, err := gatewayprovider.GrokMediaCodec().GrokMediaSignedVideoContentURL([]byte(`{"video":{"url":"`+rawURL+`"}}`), "task-1")
			require.ErrorContains(t, err, "unsupported video content URL")
		})
	}
}

func TestGrokMediaSignedVideoContentURLRejectsDifferentRelayTask(t *testing.T) {
	_, err := gatewayprovider.GrokMediaCodec().GrokMediaSignedVideoContentURL(
		[]byte(`{"video":{"url":"/v1/videos/task-2/content"}}`),
		"task-1",
	)

	require.ErrorContains(t, err, "unsupported video content URL")
}

func TestForwardGrokVideoStatusRewritesOnlyProtectedContentURL(t *testing.T) {
	statusBody := `{"id":"task-1","status":"completed","url":"https://relay.example/v1/videos/task-1/content","download_url":"/v1/videos/task-1/content","video_url":"https://vidgen.x.ai/task-1.mp4","counter":9007199254740993}`
	upstream := &grokMediaContentUpstreamStub{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(statusBody)),
		},
	}
	svc := grokMediaFixture(upstream)
	c, recorder := grokMediaContentTestContext(http.MethodGet, "https://api.example/v1/videos/task-1", map[string]string{
		"X-Forwarded-Host":  "malicious.invalid",
		"X-Forwarded-Proto": "https",
	})

	_, err := svc.ForwardGrokMedia(
		context.Background(), c, grokMediaContentTestProvider(),
		grok.GrokMediaEndpointVideoStatus, "task-1", nil, "",
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "/v1/videos/task-1/content", gjson.Get(recorder.Body.String(), "url").String())
	require.Equal(t, "/v1/videos/task-1/content", gjson.Get(recorder.Body.String(), "download_url").String())
	require.Equal(t, "https://vidgen.x.ai/task-1.mp4", gjson.Get(recorder.Body.String(), "video_url").String())
	require.Equal(t, "9007199254740993", gjson.Get(recorder.Body.String(), "counter").String())
	require.NotContains(t, recorder.Body.String(), "malicious.invalid")
}

func TestRewriteGrokMediaVideoContentURLsPreservesOtherIDsAndHandlesNestedEscapedID(t *testing.T) {
	body := []byte(`{"nested":[{"url":"https://relay.example/v1/videos/task%2Fone/content"},{"url":"https://relay.example/v1/videos/task-two/content"}]}`)

	rewritten := rewriteGrokMediaVideoContentURLs(body, "task/one", "/v1/videos/task%2Fone/content")

	require.Equal(t, "/v1/videos/task%2Fone/content", gjson.GetBytes(rewritten, "nested.0.url").String())
	require.Equal(t, "https://relay.example/v1/videos/task-two/content", gjson.GetBytes(rewritten, "nested.1.url").String())
}

func TestRewriteGrokMediaVideoContentURLsRewritesSignedVideoURL(t *testing.T) {
	body := []byte(`{"status":"done","video":{"url":"https://vidgen.x.ai/signed-token/xai-video-request-1.mp4","duration":8}}`)

	rewritten := rewriteGrokMediaVideoContentURLs(body, "request-1", "/v1/videos/request-1/content")

	require.Equal(t, "/v1/videos/request-1/content", gjson.GetBytes(rewritten, "video.url").String())
	require.Equal(t, "8", gjson.GetBytes(rewritten, "video.duration").String())
	require.Equal(t, "done", gjson.GetBytes(rewritten, "status").String())
}

// mediaErrorResponseFixture feeds a response through media execution using the fixture's health service.
func mediaErrorResponseFixture(executor *GrokExecutor, ctx context.Context, resp *http.Response, c *gin.Context, target *gatewayprovider.ExecutionProvider, requestID, model string) (*forwardcore.OpenAIResult, error) {
	local := *executor
	local.Transport = &auxiliaryHTTPRecorder{resp: resp}
	local.Credentials = gatewaytestkit.RequestCredentials(nil, &providercore.OpenAIExecutionCredentials{Grok: func(context.Context, *providercore.Record) (string, error) { return "fixture-token", nil }}, nil, nil)
	local.Credentials.HasGrokTokenSource = true
	if target.Record.Credentials == nil {
		target.Record.Credentials = map[string]any{}
	}
	target.Record.Credentials["api_key"] = "fixture-token"
	resp.Header.Set("x-request-id", requestID)
	return local.ForwardGrokMedia(ctx, c, target, grok.GrokMediaEndpointImagesGenerations, "", []byte(`{"model":"`+model+`","prompt":"fixture"}`), "application/json")
}

func TestApplyGrokImagineImageGeometryMapsOpenAISize(t *testing.T) {
	t.Parallel()

	out, err := gatewayprovider.GrokMediaCodec().ApplyGrokImagineImageGeometry([]byte(`{"model":"grok-imagine-image-2.0","prompt":"hi","size":"1152x1536"}`))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(out, "size").Exists())
	require.Equal(t, "2k", gjson.GetBytes(out, "resolution").String())
	require.Equal(t, "3:4", gjson.GetBytes(out, "aspect_ratio").String())
}

func TestApplyGrokImagineImageGeometryKeepsClientGeometry(t *testing.T) {
	t.Parallel()

	out, err := gatewayprovider.GrokMediaCodec().ApplyGrokImagineImageGeometry([]byte(`{"size":"1024x1024","resolution":"2K","aspect_ratio":"16:9"}`))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(out, "size").Exists())
	require.Equal(t, "2k", gjson.GetBytes(out, "resolution").String())
	require.Equal(t, "16:9", gjson.GetBytes(out, "aspect_ratio").String())
}

func TestSanitizeGrokMediaForwardBodyConvertsImageSize(t *testing.T) {
	t.Parallel()

	out, contentType, err := sanitizeGrokMediaForwardBody(
		grok.GrokMediaEndpointImagesGenerations,
		[]byte(`{"model":"grok-imagine-image","prompt":"hi","size":"1024x1024"}`),
		"application/json",
	)
	require.NoError(t, err)
	require.Equal(t, "application/json", contentType)
	require.False(t, gjson.GetBytes(out, "size").Exists())
	require.Equal(t, "1k", gjson.GetBytes(out, "resolution").String())
	require.Equal(t, "1:1", gjson.GetBytes(out, "aspect_ratio").String())
}

func TestParseGrokMediaRequestKeepsImageResolutionOutOfVideoNormalize(t *testing.T) {
	t.Parallel()

	info := gatewayprovider.GrokMediaCodec().ParseGrokMediaRequest("application/json", []byte(`{"model":"grok-imagine-image-2.0","resolution":"2K","aspect_ratio":"16:9"}`))
	require.Equal(t, "2k", info.ImageResolution)
	require.Equal(t, "16:9", info.AspectRatio)
	require.Equal(t, pricing.VideoBillingResolution480P, info.Resolution)
}

func TestGrokImagineAspectRatioFromSize(t *testing.T) {
	t.Parallel()
	require.Equal(t, "1:1", gatewayprovider.GrokMediaCodec().GrokImagineAspectRatioFromSize("1024x1024"))
	require.Equal(t, "3:4", gatewayprovider.GrokMediaCodec().GrokImagineAspectRatioFromSize("1152x1536"))
	require.Equal(t, "4:3", gatewayprovider.GrokMediaCodec().GrokImagineAspectRatioFromSize("1536x1152"))
	require.Equal(t, "16:9", gatewayprovider.GrokMediaCodec().GrokImagineAspectRatioFromSize("1792x1024"))
}

func TestGrokVideoE2EDurationFromCreatedAt(t *testing.T) {
	t.Parallel()
	created := time.Now().UTC().Add(-45 * time.Second)
	d := media.GrokVideoE2EDuration(created.Format(time.RFC3339Nano), time.Now().UTC())
	require.GreaterOrEqual(t, d, 44*time.Second)
	require.LessOrEqual(t, d, 47*time.Second)

	require.Equal(t, time.Duration(0), media.GrokVideoE2EDuration("", time.Now()))
	require.Equal(t, time.Duration(0), media.GrokVideoE2EDuration("not-a-time", time.Now()))
	// Clamp a future CreatedAt to zero elapsed time to tolerate clock skew.
	require.Equal(t, time.Duration(0), media.GrokVideoE2EDuration(time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), time.Now()))
}

func TestGrokVideoPendingCreatedAtStampOnStoreShape(t *testing.T) {
	t.Parallel()
	// GrokVideoE2EDuration must parse timestamps produced by GrokVideoPendingCreatedAtNow.
	stamp := media.GrokVideoPendingCreatedAtNow()
	require.NotEmpty(t, stamp)
	d := media.GrokVideoE2EDuration(stamp, time.Now().UTC().Add(2*time.Second))
	require.GreaterOrEqual(t, d, time.Second)
	require.LessOrEqual(t, d, 3*time.Second)
}

// TestGrokMediaUsageCarriesTerminalState checks that failed status results reach media completion processing.
func TestGrokMediaUsageCarriesTerminalState(t *testing.T) {
	for _, state := range []string{"failed", "expired", "cancelled", "done", "pending"} {
		body, err := json.Marshal(map[string]string{"status": state})
		require.NoError(t, err)
		meta := grokMediaUsageFromResponse(grok.GrokMediaEndpointVideoStatus, grok.GrokMediaRequestInfo{}, body)
		require.Equal(t, media.GrokVideoRequestState(body), meta.VideoState)
		require.NotEmpty(t, meta.VideoState)
		require.Zero(t, meta.VideoCount)
	}
}

func TestIsGrokVideoStatusBillable(t *testing.T) {
	t.Parallel()
	// Billing requires status=done and a nonempty video.url.
	require.True(t, media.IsGrokVideoStatusBillable([]byte(`{
		"status":"done",
		"model":"grok-imagine-video-1.5",
		"video":{"url":"https://vidgen.x.ai/x.mp4","duration":8,"respect_moderation":true}
	}`)))

	// Incomplete or missing status is not billable.
	require.False(t, media.IsGrokVideoStatusBillable(nil))
	require.False(t, media.IsGrokVideoStatusBillable([]byte(`{"status":"pending"}`)))
	require.False(t, media.IsGrokVideoStatusBillable([]byte(`{"status":"expired"}`)))
	require.False(t, media.IsGrokVideoStatusBillable([]byte(`{"status":"failed"}`)))
	// done without video.url is not billable.
	require.False(t, media.IsGrokVideoStatusBillable([]byte(`{"status":"done"}`)))
	// A URL alone is insufficient evidence of completion.
	require.False(t, media.IsGrokVideoStatusBillable([]byte(`{"url":"https://example.com/v.mp4"}`)))
	require.False(t, media.IsGrokVideoStatusBillable([]byte(`{"download_url":"/v1/videos/task/content"}`)))
	// completed is not the accepted done status.
	require.False(t, media.IsGrokVideoStatusBillable([]byte(`{"status":"completed","video":{"url":"https://vidgen.x.ai/x.mp4"}}`)))
}

func TestExtractGrokVideoBillingFromStatusBodyPrefersUpstreamParams(t *testing.T) {
	t.Parallel()
	pending := &media.GrokVideoPendingBilling{
		Model:                "pending-model",
		BillingModel:         "pending-billing",
		UpstreamModel:        "pending-upstream",
		VideoResolution:      pricing.VideoBillingResolution720P,
		VideoDurationSeconds: 8,
	}
	// This fixture uses the done/video.url completion shape.
	body := []byte(`{
		"status":"done",
		"model":"grok-imagine-video-1.5",
		"video":{"url":"https://vidgen.x.ai/signed.mp4","duration":12,"respect_moderation":true}
	}`)
	result := ExtractGrokVideoBillingFromStatusBody(body, pending, "req-1")
	require.NotNil(t, result)
	require.Equal(t, 1, result.VideoCount)
	require.Equal(t, "grok-imagine-video-1.5", result.Model)
	// With no resolution in the status response, use the saved creation request.
	require.Equal(t, pricing.VideoBillingResolution720P, result.VideoResolution)
	// Use video.duration when the completed response supplies it.
	require.Equal(t, 12, result.VideoDurationSeconds)
}

func TestExtractGrokVideoBillingFromStatusBodyFallsBackToPending(t *testing.T) {
	t.Parallel()
	pending := &media.GrokVideoPendingBilling{
		Model:                "create-model",
		BillingModel:         "create-billing",
		UpstreamModel:        "create-upstream",
		VideoResolution:      pricing.VideoBillingResolution1080P,
		VideoDurationSeconds: 10,
	}
	// The response confirms completion but omits model and duration.
	body := []byte(`{"status":"done","video":{"url":"https://vidgen.x.ai/signed.mp4"}}`)
	result := ExtractGrokVideoBillingFromStatusBody(body, pending, "req-2")
	require.NotNil(t, result)
	require.Equal(t, "create-billing", result.BillingModel)
	require.Equal(t, "create-upstream", result.UpstreamModel)
	require.Equal(t, pricing.VideoBillingResolution1080P, result.VideoResolution)
	require.Equal(t, 10, result.VideoDurationSeconds)
}

func TestExtractGrokVideoBillingRejectsNonDoneStatus(t *testing.T) {
	t.Parallel()
	pending := &media.GrokVideoPendingBilling{Model: "m", VideoDurationSeconds: 8, VideoResolution: "720p"}
	require.Nil(t, ExtractGrokVideoBillingFromStatusBody(
		[]byte(`{"status":"pending","video":{"url":"https://vidgen.x.ai/x.mp4","duration":8}}`),
		pending, "req",
	))
	require.Nil(t, ExtractGrokVideoBillingFromStatusBody(
		[]byte(`{"status":"completed","video":{"url":"https://vidgen.x.ai/x.mp4","duration":8}}`),
		pending, "req",
	))
}

func TestGrokMediaUsageFromResponseVideoCreateDoesNotBill(t *testing.T) {
	t.Parallel()
	info := grok.GrokMediaRequestInfo{Model: "grok-imagine-video", Resolution: "720p", DurationSeconds: 10}
	meta := grokMediaUsageFromResponse(grok.GrokMediaEndpointVideosGenerations, info, []byte(`{"request_id":"v1"}`))
	require.Equal(t, "v1", meta.ResponseID)
	require.Equal(t, 0, meta.VideoCount)
	require.Equal(t, 10, meta.VideoDurationSeconds)
	require.Equal(t, pricing.VideoBillingResolution720P, meta.VideoResolution)
}

func TestGrokMediaUsageFromResponseVideoStatusBillsOnOfficialDone(t *testing.T) {
	t.Parallel()
	meta := grokMediaUsageFromResponse(
		grok.GrokMediaEndpointVideoStatus,
		grok.GrokMediaRequestInfo{},
		[]byte(`{"status":"done","model":"grok-imagine-video-1.5","video":{"url":"https://vidgen.x.ai/a.mp4","duration":9}}`),
	)
	require.Equal(t, 1, meta.VideoCount)
	require.Equal(t, 9, meta.VideoDurationSeconds)
	require.Equal(t, "grok-imagine-video-1.5", meta.Model)

	// Pending status must not generate billable units.
	pendingOnly := grokMediaUsageFromResponse(
		grok.GrokMediaEndpointVideoStatus,
		grok.GrokMediaRequestInfo{},
		[]byte(`{"status":"pending"}`),
	)
	require.Equal(t, 0, pendingOnly.VideoCount)

	// completed is not treated as done.
	completed := grokMediaUsageFromResponse(
		grok.GrokMediaEndpointVideoStatus,
		grok.GrokMediaRequestInfo{},
		[]byte(`{"status":"completed","video":{"url":"https://vidgen.x.ai/a.mp4","duration":9}}`),
	)
	require.Equal(t, 0, completed.VideoCount)
}

func TestExtractGrokMediaModelSupportsJSONAndMultipart(t *testing.T) {
	require.Equal(t, "grok-imagine", gatewayprovider.GrokMediaCodec().ExtractGrokMediaModel("application/json", []byte(`{"model":"grok-imagine"}`)))

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("prompt", "draw a cat"))
	require.NoError(t, writer.WriteField("model", "grok-imagine-edit"))
	require.NoError(t, writer.Close())

	require.Equal(t, "grok-imagine-edit", gatewayprovider.GrokMediaCodec().ExtractGrokMediaModel(writer.FormDataContentType(), buf.Bytes()))
}

func TestForwardGrokMediaImagesGenerationNormalizesImagineAlias(t *testing.T) {
	t.Setenv(grok.EnvAllowUnsafeURLOverrides, "true")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine","prompt":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 61,
			Name:        "grok-4.5",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "api-key",
				"base_url": "https://xai.test/v1",
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-image-req"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://images.test/cat.png"}]}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

	result, err := svc.Grok.ForwardGrokMedia(context.Background(), c, provider, grok.GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, http.MethodPost, upstream.lastReq.Method)
	require.Equal(t, "Bearer api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Grok-Client-Version"))
	require.NotEqual(t, grok.DefaultGrokUpstreamUserAgent(), upstream.lastReq.Header.Get("User-Agent"))
	require.JSONEq(t, `{"model":"grok-imagine","prompt":"draw a cat"}`, string(upstream.lastBody))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"data":[{"url":"https://images.test/cat.png"}]}`, recorder.Body.String())
	require.Equal(t, "xai-image-req", result.RequestID)
	require.Equal(t, "grok-imagine", result.Model)
	require.Equal(t, "grok-imagine", result.BillingModel)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, pricing.ImageBillingSize2K, result.ImageSize)
}

func TestForwardGrokMediaImagesGenerationRejectsEmptySuccessfulResponse(t *testing.T) {
	t.Setenv(grok.EnvAllowUnsafeURLOverrides, "true")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-image","prompt":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 66,
			Name:        "grok-4.5",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "api-key",
				"base_url": "https://xai.test/v1",
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

	result, err := svc.Grok.ForwardGrokMedia(context.Background(), c, provider, grok.GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.JSONEq(t, `{"data":[]}`, string(failoverErr.ResponseBody))
	require.Empty(t, recorder.Body.String())
}

func TestForwardGrokMediaAppliesProviderModelMappingAfterEndpointNormalization(t *testing.T) {
	t.Setenv(grok.EnvAllowUnsafeURLOverrides, "true")

	tests := []struct {
		name             string
		endpoint         grok.GrokMediaEndpoint
		path             string
		body             string
		modelMapping     map[string]any
		wantRequestModel string
		wantBillingModel string
		wantUpstream     string
		wantBody         string
		responseBody     string
	}{
		{
			name:             "image generation maps normalized image alias",
			endpoint:         grok.GrokMediaEndpointImagesGenerations,
			path:             "/v1/images/generations",
			body:             `{"model":"grok-imagine","prompt":"draw a cat"}`,
			modelMapping:     map[string]any{"grok-imagine": "vendor-image-model"},
			wantRequestModel: "grok-imagine",
			wantBillingModel: "vendor-image-model",
			wantUpstream:     "vendor-image-model",
			wantBody:         `{"model":"vendor-image-model","prompt":"draw a cat"}`,
			responseBody:     `{"data":[{"url":"https://images.test/mapped.png"}]}`,
		},
		{
			name:             "video generation maps explicit text-only model",
			endpoint:         grok.GrokMediaEndpointVideosGenerations,
			path:             "/v1/videos/generations",
			body:             `{"model":"grok-imagine-video-1.5","prompt":"waves"}`,
			modelMapping:     map[string]any{"grok-imagine-video-1.5": "grok-image-video"},
			wantRequestModel: "grok-imagine-video-1.5",
			wantBillingModel: "grok-image-video",
			wantUpstream:     "grok-image-video",
			wantBody:         `{"model":"grok-image-video","prompt":"waves"}`,
			responseBody:     `{"request_id":"video-request-mapped"}`,
		},
		{
			name:             "image-to-video preserves then maps the requested model",
			endpoint:         grok.GrokMediaEndpointVideosGenerations,
			path:             "/v1/videos/generations",
			body:             `{"model":"grok-imagine-video-1.5","prompt":"animate","image":{"url":"https://example.com/input.png"}}`,
			modelMapping:     map[string]any{"grok-imagine-video-1.5": "vendor-image-video"},
			wantRequestModel: "grok-imagine-video-1.5",
			wantBillingModel: "vendor-image-video",
			wantUpstream:     "vendor-image-video",
			wantBody:         `{"model":"vendor-image-video","prompt":"animate","image":{"url":"https://example.com/input.png"}}`,
			responseBody:     `{"request_id":"image-video-request-mapped"}`,
		},
		{
			name:             "mapping and image sanitization compose",
			endpoint:         grok.GrokMediaEndpointImagesGenerations,
			path:             "/v1/images/generations",
			body:             `{"model":"grok-imagine","prompt":"draw","size":"1024x1024"}`,
			modelMapping:     map[string]any{"grok-imagine": "vendor-image-model"},
			wantRequestModel: "grok-imagine",
			wantBillingModel: "vendor-image-model",
			wantUpstream:     "vendor-image-model",
			wantBody:         `{"model":"vendor-image-model","prompt":"draw","resolution":"1k","aspect_ratio":"1:1"}`,
			responseBody:     `{"data":[{"url":"https://images.test/mapped.png"}]}`,
		},
		{
			name:             "whitespace mapping target safely preserves normalized model",
			endpoint:         grok.GrokMediaEndpointImagesGenerations,
			path:             "/v1/images/generations",
			body:             `{"model":"grok-imagine","prompt":"draw"}`,
			modelMapping:     map[string]any{"grok-imagine": "   "},
			wantRequestModel: "grok-imagine",
			wantBillingModel: "grok-imagine",
			wantUpstream:     "grok-imagine",
			wantBody:         `{"model":"grok-imagine","prompt":"draw"}`,
			responseBody:     `{"data":[{"url":"https://images.test/mapped.png"}]}`,
		},
		{
			name:             "provider mapping target stays unchanged",
			endpoint:         grok.GrokMediaEndpointImagesGenerations,
			path:             "/v1/images/generations",
			body:             `{"model":"grok-imagine","prompt":"draw"}`,
			modelMapping:     map[string]any{"grok-imagine": "grok-build"},
			wantRequestModel: "grok-imagine",
			wantBillingModel: "grok-build",
			wantUpstream:     "grok-build",
			wantBody:         `{"model":"grok-build","prompt":"draw"}`,
			responseBody:     `{"data":[{"url":"https://images.test/normalized.png"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")

			provider := &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 66,
					Name:        "grok-mapped",
					Platform:    capability.PlatformGrok,
					Type:        capability.ProviderTypeAPIKey,
					Concurrency: 1,
					Credentials: map[string]any{
						"api_key":       "api-key",
						"base_url":      "https://xai.test/v1",
						"model_mapping": tt.modelMapping,
					},
				},
			}
			upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(tt.responseBody)),
			}}
			svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

			result, err := svc.Grok.ForwardGrokMedia(context.Background(), c, provider, tt.endpoint, "", []byte(tt.body), "application/json")

			require.NoError(t, err)
			require.JSONEq(t, tt.wantBody, string(upstream.lastBody))
			require.Equal(t, tt.wantRequestModel, result.Model)
			require.Equal(t, tt.wantBillingModel, result.BillingModel)
			require.Equal(t, tt.wantUpstream, result.UpstreamModel)
		})
	}
}

func TestForwardGrokMediaImagesGenerationStripsUnsupportedSize(t *testing.T) {
	t.Setenv(grok.EnvAllowUnsafeURLOverrides, "true")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-image","prompt":"draw a cat","size":"1024x1024"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 65,
			Name:        "grok-4.5",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":       "api-key",
				"base_url":      "https://xai.test/v1",
				"model_mapping": map[string]any{"grok-imagine-edit": "vendor-image-edit"},
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://images.test/cat.png"}]}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

	result, err := svc.Grok.ForwardGrokMedia(context.Background(), c, provider, grok.GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"grok-imagine-image","prompt":"draw a cat","resolution":"1k","aspect_ratio":"1:1"}`, string(upstream.lastBody))
	require.False(t, gjson.GetBytes(upstream.lastBody, "size").Exists())
	require.Equal(t, pricing.ImageBillingSize1K, result.ImageSize)
	require.Equal(t, "1024x1024", result.ImageInputSize)
}

func TestForwardGrokMediaImagesEditMultipartConvertsToJSON(t *testing.T) {
	t.Setenv(grok.EnvAllowUnsafeURLOverrides, "true")

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("model", "grok-imagine-edit"))
	require.NoError(t, writer.WriteField("prompt", "edit this private image"))
	partHeader := textproto.MIMEHeader{}
	partHeader.Set("Content-Disposition", `form-data; name="image"; filename="input.png"`)
	partHeader.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(partHeader)
	require.NoError(t, err)
	_, err = part.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(buf.Bytes()))
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 62,
			Name:        "grok-4.5",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":       "api-key",
				"base_url":      "https://xai.test/v1",
				"model_mapping": map[string]any{"grok-imagine-edit": "vendor-image-edit"},
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://images.test/edited.png"}]}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

	result, err := svc.Grok.ForwardGrokMedia(context.Background(), c, provider, grok.GrokMediaEndpointImagesEdits, "", buf.Bytes(), writer.FormDataContentType())
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/images/edits", upstream.lastReq.URL.String())
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
	require.True(t, json.Valid(upstream.lastBody))
	require.Equal(t, "vendor-image-edit", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "edit this private image", gjson.GetBytes(upstream.lastBody, "prompt").String())
	require.True(t, strings.HasPrefix(gjson.GetBytes(upstream.lastBody, "image.url").String(), "data:image/png;base64,"))
	require.False(t, gjson.GetBytes(upstream.lastBody, "image.image_url").Exists())
	require.Equal(t, "vendor-image-edit", result.BillingModel)
	require.Equal(t, "vendor-image-edit", result.UpstreamModel)
}

func TestForwardGrokMediaVideoGenerationReturnsUsageAndResponseID(t *testing.T) {
	t.Setenv(grok.EnvAllowUnsafeURLOverrides, "true")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-video-1.5","prompt":"waves","resolution":"720p","duration":10}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 63,
			Name:        "grok-4.5",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "api-key",
				"base_url": "https://xai.test/v1",
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-video-generate-req"},
		},
		Body: io.NopCloser(strings.NewReader(`{"request_id":"video-request-123","usage":{"prompt_tokens":3,"completion_tokens":4}}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

	result, err := svc.Grok.ForwardGrokMedia(context.Background(), c, provider, grok.GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/videos/generations", upstream.lastReq.URL.String())
	require.JSONEq(t, `{"model":"grok-imagine-video-1.5","prompt":"waves","resolution":"720p","duration":10}`, string(upstream.lastBody))
	require.Equal(t, "video-request-123", result.ResponseID)
	require.Equal(t, "grok-imagine-video-1.5", result.BillingModel)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	// Creation accepts the task; VideoCount stays zero until completion includes video.url.
	require.Equal(t, 0, result.ImageCount)
	require.Empty(t, result.ImageSize)
	require.Equal(t, 0, result.VideoCount)
	require.Equal(t, pricing.VideoBillingResolution720P, result.VideoResolution)
	require.Equal(t, 10, result.VideoDurationSeconds)
}

func TestForwardGrokMediaVideoGenerationReturnsTaskIDAsResponseID(t *testing.T) {
	t.Setenv(grok.EnvAllowUnsafeURLOverrides, "true")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-video","prompt":"waves"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 63,
			Name:        "grok-4.5",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "api-key",
				"base_url": "https://xai.test/v1",
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"task_id":"video-task-123"}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

	result, err := svc.Grok.ForwardGrokMedia(context.Background(), c, provider, grok.GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "video-task-123", result.ResponseID)
}

func TestForwardGrokMediaVideoGenerationPreservesImageToVideoModel(t *testing.T) {
	t.Setenv(grok.EnvAllowUnsafeURLOverrides, "true")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-video-1.5","prompt":"animate","image":{"image_url":"data:image/png;base64,aW1n"}}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 63,
			Name:        "grok-4.5",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "api-key",
				"base_url": "https://xai.test/v1",
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"request_id":"video-request-456"}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

	result, err := svc.Grok.ForwardGrokMedia(context.Background(), c, provider, grok.GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/videos/generations", upstream.lastReq.URL.String())
	require.JSONEq(t, `{"model":"grok-imagine-video-1.5","prompt":"animate","image":{"url":"data:image/png;base64,aW1n"}}`, string(upstream.lastBody))
	require.Equal(t, "video-request-456", result.ResponseID)
	require.Equal(t, "grok-imagine-video-1.5", result.BillingModel)
	// An omitted duration uses the eight-second billing default.
	require.Equal(t, pricing.VideoBillingDefaultDurationSeconds, result.VideoDurationSeconds)
}

func TestForwardGrokMediaOAuthImageToVideoUsesOfficialAPIForLargeBody(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	imageData := strings.Repeat("A", 2*1024*1024)
	body := []byte(`{"model":"grok-imagine-video-1.5","prompt":"animate","image":{"image_url":"data:image/png;base64,` + imageData + `"}}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 66,
			Name:        "grok-oauth",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":  "oauth-access-token",
				"refresh_token": "oauth-refresh-token",
				"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
				"base_url":      grok.DefaultCLIBaseURL,
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"request_id":"video-request-oauth"}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(nil, nil), transport: upstream})

	_, err := svc.Grok.ForwardGrokMedia(context.Background(), c, provider, grok.GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, grok.DefaultBaseURL+"/videos/generations", upstream.lastReq.URL.String())
	require.Empty(t, upstream.lastReq.Header.Get("X-XAI-Token-Auth"))
	require.Empty(t, upstream.lastReq.Header.Get("x-grok-client-version"))
	require.Equal(t, "data:image/png;base64,"+imageData, gjson.GetBytes(upstream.lastBody, "image.url").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "image.image_url").Exists())
}

func TestForwardGrokMediaVideoStatusUsesGETWithoutBody(t *testing.T) {
	t.Setenv(grok.EnvAllowUnsafeURLOverrides, "true")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/request-123", nil)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 62,
			Name:        "grok-4.5",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "api-key",
				"base_url": "https://xai.test/v1",
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-video-req"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"request-123","status":"completed"}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

	result, err := svc.Grok.ForwardGrokMedia(context.Background(), c, provider, grok.GrokMediaEndpointVideoStatus, "request-123", nil, "")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/videos/request-123", upstream.lastReq.URL.String())
	require.Equal(t, http.MethodGet, upstream.lastReq.Method)
	require.Equal(t, "Bearer api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Grok-Client-Version"))
	require.NotEqual(t, grok.DefaultGrokUpstreamUserAgent(), upstream.lastReq.Header.Get("User-Agent"))
	require.Empty(t, upstream.lastReq.Header.Get("Content-Type"))
	require.Empty(t, upstream.lastBody)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"id":"request-123","status":"completed"}`, recorder.Body.String())
	require.Equal(t, "xai-video-req", result.RequestID)
}

func TestForwardGrokMediaVideoMutationEndpoints(t *testing.T) {
	t.Setenv(grok.EnvAllowUnsafeURLOverrides, "true")

	tests := []struct {
		name     string
		endpoint grok.GrokMediaEndpoint
		path     string
	}{
		{name: "edit", endpoint: grok.GrokMediaEndpointVideosEdits, path: "/videos/edits"},
		{name: "extension", endpoint: grok.GrokMediaEndpointVideosExtensions, path: "/videos/extensions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := []byte(`{"model":"grok-imagine-video","prompt":"continue","video":{"url":"https://example.com/in.mp4"},"duration":6}`)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1"+tt.path, bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			provider := &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 71, Name: "grok-4.5", Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey, Concurrency: 1,
					Credentials: map[string]any{
						"api_key":       "api-key",
						"base_url":      "https://xai.test/v1",
						"model_mapping": map[string]any{"grok-imagine-video": "vendor-video-mutation"},
					},
				},
			}
			upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"request_id":"video-mutation-123"}`)),
			}}
			svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

			result, err := svc.Grok.ForwardGrokMedia(context.Background(), c, provider, tt.endpoint, "", body, "application/json")
			require.NoError(t, err)
			require.Equal(t, "https://xai.test/v1"+tt.path, upstream.lastReq.URL.String())
			require.Equal(t, http.MethodPost, upstream.lastReq.Method)
			require.JSONEq(t, `{"model":"vendor-video-mutation","prompt":"continue","video":{"url":"https://example.com/in.mp4"},"duration":6}`, string(upstream.lastBody))
			require.Equal(t, "video-mutation-123", result.ResponseID)
			require.Equal(t, 0, result.VideoCount)
			require.Equal(t, 6, result.VideoDurationSeconds)
			require.Equal(t, "vendor-video-mutation", result.BillingModel)
			require.Equal(t, "vendor-video-mutation", result.UpstreamModel)
		})
	}
}

func TestForwardGrokMedia429ReconcilesRateLimitBeforeCustomErrorBypass(t *testing.T) {
	t.Setenv(grok.EnvAllowUnsafeURLOverrides, "true")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine","prompt":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 64,
			Name:        "grok-4.5",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":                    "api-key",
				"base_url":                   "https://xai.test/v1",
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusBadRequest)},
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-error-req"},
			"Retry-After":    []string{"45"},
		},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"do not expose this upstream detail"}}`)),
	}}
	repo := &grokQuotaProviderRepo{}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream, providers: repo})

	result, err := svc.Grok.ForwardGrokMedia(context.Background(), c, provider, grok.GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Upstream gateway error")
	require.NotContains(t, recorder.Body.String(), "do not expose")
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Zero(t, repo.tempUnschedCalls)
	require.True(t, httpFixtureRuntimeBlocked(svc, provider))
}

func TestGrokMedia429FailoverPreservesRetryAfter(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 641, Name: "grok-oauth", Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth,
			Status: billing.StatusActive, Schedulable: true,
			Credentials: map[string]any{
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusTooManyRequests)},
			},
		},
	}
	repo := &grokQuotaProviderRepo{}
	svc := newResponsesFixture(responsesFixtureInputs{providers: repo})
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"45"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}

	result, err := mediaErrorResponseFixture(svc.Grok, context.Background(), resp, c, provider, "request-id", "grok-imagine")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Equal(t, "45", http.Header(failoverErr.ResponseHeaders).Get("Retry-After"))
}

func (r *grokPoolPolicyProviderRepo) SetError(_ context.Context, _ int64, _ string) error {
	r.setErrorCalls++
	return nil
}

func (r *grokPoolPolicyProviderRepo) SetOverloaded(_ context.Context, _ int64, _ time.Time) error {
	r.overloadedCalls++
	return nil
}

func (r *grokPoolPolicyProviderRepo) SetModelRateLimit(_ context.Context, _ int64, scope string, _ time.Time, reason ...string) error {
	r.modelRateLimitCalls++
	r.lastModelRateLimitScope = scope
	if len(reason) > 0 {
		r.lastModelRateLimitReason = reason[0]
	}
	return nil
}

// newGrokPoolPolicyGateway creates a Grok fixture with shared error-policy handling.
func newGrokPoolPolicyGateway(provider *gatewayprovider.ExecutionProvider) (*OpenAIResponsesExecutor, *grokPoolPolicyProviderRepo) {
	baseRepo := &grokFixtureProviders{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
	}
	repo := &grokPoolPolicyProviderRepo{
		grokQuotaProviderRepo: &grokQuotaProviderRepo{grokFixtureProviders: baseRepo},
	}
	cfg := &responsesFixtureOptions{}
	var svc *OpenAIResponsesExecutor

	healthObserver := newHTTPHealthFixture(repo, cfg, nil, providercore.HealthOptions{Block: func(v *providercore.Record, until time.Time, reason string) {
		svc.Output.Health.Runtime.BlockProviderScheduling(v, until, reason)
	}}, nil)

	svc = newResponsesFixture(responsesFixtureInputs{providers: repo, health: healthObserver, options: cfg})
	healthObserver.Limits.RetryOpenAI = func(v *providercore.Record, h http.Header, body []byte) bool {
		return provideradapter.CanRetryOpenAI429(svc.Output.Health.Runtime, v, h, body)
	}

	return svc, repo
}

// newGrokPoolProvider returns a Grok API key provider configured in pool mode.
func newGrokPoolProvider(id int64) *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: id,
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"pool_mode": true},
		},
	}
}

func TestGrokMediaPoolModeRetryFlagFollowsExplicitPolicies(t *testing.T) {
	t.Run("default 429 remains retryable without local cooldown", func(t *testing.T) {
		provider := newGrokPoolProvider(630)
		svc, repo := newGrokPoolPolicyGateway(provider)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		resp := &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"60"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
		}

		result, err := mediaErrorResponseFixture(svc.Grok, context.Background(), resp, c, provider, "request-id", "grok-imagine")

		require.Nil(t, result)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.True(t, failoverErr.RetryableOnSameProvider)
		require.Zero(t, repo.rateLimitedCalls)
		require.False(t, httpFixtureRuntimeBlocked(svc, provider))
	})

	t.Run("explicit 401 policy stops same provider retry", func(t *testing.T) {
		provider := newGrokPoolProvider(631)
		provider.Record.Credentials["custom_error_codes_enabled"] = true
		provider.Record.Credentials["custom_error_codes"] = []any{float64(http.StatusUnauthorized)}
		svc, repo := newGrokPoolPolicyGateway(provider)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		resp := &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid api key"}}`)),
		}

		result, err := mediaErrorResponseFixture(svc.Grok, context.Background(), resp, c, provider, "request-id", "grok-imagine")

		require.Nil(t, result)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.False(t, failoverErr.RetryableOnSameProvider)
		require.Equal(t, 1, repo.setErrorCalls)
		require.True(t, httpFixtureRuntimeBlocked(svc, provider))
	})

	t.Run("mapped model is used by temporary policy", func(t *testing.T) {
		t.Setenv(grok.EnvAllowUnsafeURLOverrides, "true")
		provider := newGrokPoolProvider(632)
		provider.Record.Credentials["api_key"] = "api-key"
		provider.Record.Credentials["base_url"] = "https://xai.test/v1"
		provider.Record.Credentials["model_mapping"] = map[string]any{"image-alias": "vendor-image-model"}
		provider.Record.Credentials["temp_unschedulable_enabled"] = true
		provider.Record.Credentials["temp_unschedulable_rules"] = []any{
			map[string]any{
				"error_code":       float64(http.StatusServiceUnavailable),
				"keywords":         []any{"maintenance"},
				"duration_minutes": float64(30),
			},
		}
		svc, repo := newGrokPoolPolicyGateway(provider)
		upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"maintenance in progress"}}`)),
		}}
		svc.Requests.Transport = upstream
		if svc.Grok != nil {
			svc.Grok.Transport = svc.Requests.Transport
		}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		body := []byte(`{"model":"image-alias","prompt":"draw a cat"}`)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		result, err := svc.Grok.ForwardGrokMedia(
			context.Background(),
			c,
			provider,
			grok.GrokMediaEndpointImagesGenerations,
			"",
			body,
			"application/json",
		)

		require.Nil(t, result)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, 1, repo.modelRateLimitCalls)
		require.Equal(t, "vendor-image-model", repo.lastModelRateLimitScope)
		require.Equal(t, "vendor-image-model", gjson.GetBytes(upstream.lastBody, "model").String())
	})
}
