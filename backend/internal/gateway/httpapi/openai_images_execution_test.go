package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
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

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providerimages "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	settingstestkit "github.com/TokenFlux/TokenRouter/internal/settings/testkit"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openAIOAuthImageActualSizeTestRun retains execution and HTTP observations
// from one dimension fixture.
type openAIOAuthImageActualSizeTestRun struct {
	// result, recorder and upstream describe one completed fixture exchange.
	// They retain execution output, delivered bytes and captured request respectively,
	// so dimension assertions compare the same image across those representations.
	result   *forwardcore.OpenAIResult  // retained image execution observations
	recorder *httptest.ResponseRecorder // captured client response bytes
	upstream *auxiliaryHTTPRecorder     // captured upstream request fixture
}

// failingOpenAIImageWriter injects client write failure after a configured
// number of delegations.
type failingOpenAIImageWriter struct {
	gin.ResponseWriter // wrapped client response writer
	// failAfter bounds delegated Write calls, and writes counts those delegations
	// before the underlying writer returns. Nonpositive allowance fails immediately;
	// the counter does not measure accepted bytes or successful output.
	failAfter int // permitted delegated write calls
	writes    int // observed delegated write calls
}

// openAIImageTestSSEEvent pairs one fixture event label with its final data
// line.
type openAIImageTestSSEEvent struct {
	// Name and Data are paired within one blank-line chunk by the fixture parser.
	// Data retains the final matching data line, leaving multiline protocol
	// reconstruction to the native response reader under test.
	Name string // captured SSE event label
	Data string // captured final data line
}

// TestOpenAIImagesJSONKeepalive_HeartbeatBeforeForwardStillFailsOver commits
// JSON heartbeat padding before an OAuth server error. The attempt returns a
// classified 502 failover while the client contains only whitespace and Ops
// retains its failure identity.
func TestOpenAIImagesJSONKeepalive_HeartbeatBeforeForwardStillFailsOver(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_heartbeat_failover"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000021}}\n\n" +
					"data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"The image service is temporarily unavailable.\"}}\n\n",
			)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	// Commit padding before this attempt so a later failure must distinguish
	// physical HTTP commitment from actual image output when deciding recovery.
	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	defer stop()
	waitForImageExecutionKeepalive(t, c)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 22,
			Name:     "openai-oauth-heartbeat-failover",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "temporarily unavailable")
	require.Empty(t, strings.TrimSpace(rec.Body.String()), "only heartbeat whitespace may reach the client")

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*ops.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "failover", events[0].Kind)
	require.Equal(t, provider.Record.ID, events[0].ProviderID)
	require.Equal(t, http.StatusBadGateway, events[0].UpstreamStatusCode)
}

// waitForImageExecutionKeepalive waits up to one second for the synchronized
// writer to report heartbeat commitment. It requires installed heartbeat
// state and fails the fixture if that observation does not arrive.
func waitForImageExecutionKeepalive(t *testing.T, c *gin.Context) {
	t.Helper()
	require.True(t, OpenAIImagesJSONKeepalivePresent(c))
	require.Eventually(t, c.Writer.Written, time.Second, time.Millisecond)
}

// TestDetectOpenAIImageResultSize compares encoded PNG, JPEG and three WebP
// header forms with invalid data. Dimension detection handles the tested
// Base64 variants and returns empty size for malformed or unrecognized input.
func TestDetectOpenAIImageResultSize(t *testing.T) {
	pngEncoded := encodeOpenAIImageTestPNG(t, 1672, 941)
	jpegEncoded := encodeOpenAIImageTestJPEG(t, 640, 360)
	webpVP8XEncoded := encodeOpenAIImageTestWebPVP8X(1920, 1080)
	webpVP8Encoded := encodeOpenAIImageTestWebPVP8(1280, 720)
	webpVP8LEncoded := encodeOpenAIImageTestWebPVP8L(640, 480)

	require.Equal(t, "1672x941", upstreamopenai.DetectOpenAIImageResultSize(pngEncoded))
	require.Equal(t, "1672x941", upstreamopenai.DetectOpenAIImageResultSize(strings.TrimRight(pngEncoded, "=")))
	require.Equal(t, "1672x941", upstreamopenai.DetectOpenAIImageResultSize("data:image/png;base64,"+pngEncoded))
	require.Equal(t, "640x360", upstreamopenai.DetectOpenAIImageResultSize(jpegEncoded))
	require.Equal(t, "1920x1080", upstreamopenai.DetectOpenAIImageResultSize(webpVP8XEncoded))
	require.Equal(t, "1280x720", upstreamopenai.DetectOpenAIImageResultSize(webpVP8Encoded))
	require.Equal(t, "640x480", upstreamopenai.DetectOpenAIImageResultSize(webpVP8LEncoded))
	require.Empty(t, upstreamopenai.DetectOpenAIImageResultSize("data:image/png;base64"))
	require.Empty(t, upstreamopenai.DetectOpenAIImageResultSize("not-image-data"))
}

// TestOpenAIGatewayServiceForwardImages_OAuthUsesDecodedOutputDimensions
// requests large low-quality output while upstream reports auto metadata and
// different actual bytes. Nonstreaming response size and retained dimensions
// follow the decoded image, while request options remain supplied.
func TestOpenAIGatewayServiceForwardImages_OAuthUsesDecodedOutputDimensions(t *testing.T) {
	run := runOpenAIOAuthImageActualSizeTest(t, false)

	require.Equal(t, "3840x2160", gjson.GetBytes(run.upstream.lastBody, "tools.0.size").String())
	require.Equal(t, "low", gjson.GetBytes(run.upstream.lastBody, "tools.0.quality").String())
	require.Equal(t, "1672x941", gjson.Get(run.recorder.Body.String(), "size").String())
	require.Equal(t, "auto", gjson.Get(run.recorder.Body.String(), "quality").String())
	require.Equal(t, []string{"1672x941"}, run.result.ImageOutputSizes)
}

// TestOpenAIGatewayServiceForwardImages_OAuthStreamingUsesDecodedOutputDimensions
// supplies actual encoded dimensions differing from requested size and auto
// metadata. The streaming completion event and retained result dimensions
// identify the decoded output.
func TestOpenAIGatewayServiceForwardImages_OAuthStreamingUsesDecodedOutputDimensions(t *testing.T) {
	run := runOpenAIOAuthImageActualSizeTest(t, true)

	events := parseOpenAIImageTestSSEEvents(run.recorder.Body.String())
	completed, ok := findOpenAIImageTestSSEEvent(events, "image_generation.completed")
	require.True(t, ok)
	require.Equal(t, "1672x941", gjson.Get(completed.Data, "size").String())
	require.Equal(t, "auto", gjson.Get(completed.Data, "quality").String())
	require.Equal(t, []string{"1672x941"}, run.result.ImageOutputSizes)
}

// runOpenAIOAuthImageActualSizeTest executes the requested streaming mode
// with an encoded image whose actual dimensions differ from request and auto
// metadata. It returns response, request and execution observations, allowing
// callers to assert each representation separately.
func runOpenAIOAuthImageActualSizeTest(t *testing.T, stream bool) openAIOAuthImageActualSizeTestRun {
	t.Helper()

	body := []byte(fmt.Sprintf(`{"model":"gpt-image-2","prompt":"draw a test chart","size":"3840x2160","quality":"low","output_format":"png","stream":%t}`, stream))
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set("api_key", &apikey.APIKey{ID: 42})

	encoded := encodeOpenAIImageTestPNG(t, 1672, 941)
	upstreamBody := fmt.Sprintf(
		"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000000,\"tools\":[{\"type\":\"image_generation\",\"model\":\"gpt-image-2\",\"size\":\"auto\",\"quality\":\"auto\",\"output_format\":\"png\"}]}}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"tools\":[{\"type\":\"image_generation\",\"model\":\"gpt-image-2\",\"size\":\"auto\",\"quality\":\"auto\",\"output_format\":\"png\"}],\"output\":[{\"id\":\"ig_actual_size\",\"type\":\"image_generation_call\",\"result\":%q}]}}\n\n"+
			"data: [DONE]\n\n",
		encoded,
	)
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"req_img_actual_size"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := newImagesFixture(imagesFixtureInputs{transport: upstream})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token":       "token-123",
				"chatgpt_account_id": "acct-123",
			},
		},
	}
	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	return openAIOAuthImageActualSizeTestRun{result: result, recorder: rec, upstream: upstream}
}

// encodeOpenAIImageTestPNG generates Base64 PNG fixture bytes with the
// supplied positive dimensions. Encoding failures stop the test, and the
// resulting image headers expose the requested canvas dimensions to the
// native detector.
func encodeOpenAIImageTestPNG(t *testing.T, width, height int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	img.SetNRGBA(0, 0, color.NRGBA{R: 0xff, A: 0xff})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// encodeOpenAIImageTestJPEG generates Base64 JPEG fixture bytes with the
// supplied positive dimensions. Encoding failures stop the test, while the
// default encoder settings supply ordinary JPEG headers for dimension
// detection.
func encodeOpenAIImageTestJPEG(t *testing.T, width, height int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	img.SetNRGBA(0, 0, color.NRGBA{G: 0xff, A: 0xff})
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// encodeOpenAIImageTestWebPVP8X builds Base64 header bytes consumed by the
// native dimension detector. Callers supply positive dimensions that fit the
// twenty-four-bit zero-based canvas fields; this fixture constructs a header
// and no compressed pixel data.
//
// RIFF, WEBP and VP8X markers select the detector branch. Width and height
// are decremented before their three little-endian bytes are placed at the
// canvas offsets, matching the decoder's one-based reconstruction. Values
// outside the caller's range are truncated by byte conversion because the
// helper performs no validation.
func encodeOpenAIImageTestWebPVP8X(width, height int) string {
	// The thirty-byte prefix reaches the VP8X canvas fields consumed by
	// dimension detection.
	header := make([]byte, 30)
	copy(header[0:4], "RIFF")
	copy(header[8:12], "WEBP")
	copy(header[12:16], "VP8X")
	// Canvas dimensions are stored minus one; the detector adds that unit back
	// after reconstructing the packed fields from this format-specific header.
	width--
	height--
	header[24], header[25], header[26] = byte(width), byte(width>>8), byte(width>>16)
	header[27], header[28], header[29] = byte(height), byte(height>>8), byte(height>>16)
	return base64.StdEncoding.EncodeToString(header)
}

// encodeOpenAIImageTestWebPVP8 builds Base64 VP8 header bytes for dimension
// detection. Callers supply positive dimensions within fourteen bits because
// the native decoder masks frame dimensions to that width. The fixture
// contains the expected RIFF/WebP and frame-start markers but no compressed
// image pixels.
//
// Little-endian sixteen-bit writes place width and height at the decoder's
// frame offsets. The detector reads their low fourteen bits and requires
// positive values, so this helper's narrow input condition keeps its encoded
// dimension observation equal to the caller's test canvas.
func encodeOpenAIImageTestWebPVP8(width, height int) string {
	// The thirty-byte prefix reaches both VP8 frame dimensions consumed by
	// detection.
	header := make([]byte, 30)
	copy(header[0:4], "RIFF")
	copy(header[8:12], "WEBP")
	copy(header[12:16], "VP8 ")
	copy(header[23:26], "\x9d\x01\x2a")
	// Store frame dimensions at the native detector's little-endian offsets; its
	// fourteen-bit masks make larger caller values unsuitable for this fixture.
	binary.LittleEndian.PutUint16(header[26:28], uint16(width))
	binary.LittleEndian.PutUint16(header[28:30], uint16(height))
	return base64.StdEncoding.EncodeToString(header)
}

// encodeOpenAIImageTestWebPVP8L builds Base64 lossless-header bytes for the
// native dimension detector. Callers supply positive dimensions no greater
// than the fourteen-bit canvas capacity; the helper builds the format prefix
// rather than encoded image pixels.
//
// Width and height are decremented, then packed across four bytes. The second
// byte combines upper width bits with the lowest height bits, matching the
// detector's inverse shifts and one-based reconstruction. Masks retain only
// supported bits, so out-of-range inputs would lose information and remain a
// caller error in this fixture.
func encodeOpenAIImageTestWebPVP8L(width, height int) string {
	// The twenty-five-byte prefix reaches all packed VP8L canvas fields consumed
	// by detection.
	header := make([]byte, 25)
	copy(header[0:4], "RIFF")
	copy(header[8:12], "WEBP")
	copy(header[12:16], "VP8L")
	header[20] = 0x2f
	// Canvas dimensions are stored minus one; the detector adds that unit back
	// after reconstructing the packed fields from this format-specific header.
	width--
	height--
	header[21] = byte(width)
	header[22] = byte(width>>8)&0x3f | byte(height&0x03)<<6
	header[23] = byte(height >> 2)
	header[24] = byte(height>>10) & 0x0f
	return base64.StdEncoding.EncodeToString(header)
}

// TestOpenAIGatewayServiceForwardImages_APIKeyBackfillsB64JSONFromURL returns
// a URL image from the configured Images endpoint, then downloads it.
// Delivered Base64 retains URL, revised prompt and creation metadata, with
// separate generation and public-host download requests.
func TestOpenAIGatewayServiceForwardImages_APIKeyBackfillsB64JSONFromURL(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","size":"1024x1024"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set("api_key", &apikey.APIKey{ID: 42})

	svc := newImagesFixture(imagesFixtureInputs{})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	upstream := &auxiliaryHTTPRecorder{
		responses: []*http.Response{
			{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"X-Request-Id": []string{"req_img_url"},
				},
				Body: io.NopCloser(strings.NewReader(
					`{"created":1710000000,"data":[{"url":"https://cdn.example.com/cat.png","revised_prompt":"a cat"}],"usage":{"input_tokens":10,"output_tokens":20}}`,
				)),
			},
			b64BackfillImageResponse(http.StatusOK, "image/png", b64BackfillPNGBytes),
		},
	}
	svc.Requests.Transport = upstream

	result, err := svc.ForwardImages(context.Background(), c, b64BackfillProvider(true), body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)
	require.False(t, result.Stream)

	require.Len(t, upstream.requests, 2)
	require.Equal(t, http.MethodPost, upstream.requests[0].Method)
	require.Equal(t, "https://relay.example.com/v1/images/generations", upstream.requests[0].URL.String())
	require.Equal(t, http.MethodGet, upstream.requests[1].Method)
	require.Equal(t, "https://cdn.example.com/cat.png", upstream.requests[1].URL.String())
	require.True(t, upstreamcore.HTTPUpstreamPublicHostsOnly(upstream.requests[1].Context()))

	require.Equal(t, http.StatusOK, rec.Code)
	wantB64 := base64.StdEncoding.EncodeToString(b64BackfillPNGBytes)
	require.Equal(t, wantB64, gjson.Get(rec.Body.String(), "data.0.b64_json").String())
	require.Equal(t, "https://cdn.example.com/cat.png", gjson.Get(rec.Body.String(), "data.0.url").String())
	require.Equal(t, "a cat", gjson.Get(rec.Body.String(), "data.0.revised_prompt").String())
	require.Equal(t, int64(1710000000), gjson.Get(rec.Body.String(), "created").Int())
}

// TestOpenAIGatewayServiceForwardImages_APIKeyLeavesURLOnlyResponseWhenDisabled
// supplies a URL image with provider backfill disabled. Execution retains one
// image observation and original response bytes, issuing only the generation
// request.
func TestOpenAIGatewayServiceForwardImages_APIKeyLeavesURLOnlyResponseWhenDisabled(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","size":"1024x1024"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set("api_key", &apikey.APIKey{ID: 42})

	svc := newImagesFixture(imagesFixtureInputs{})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	upstreamBody := `{"created":1710000000,"data":[{"url":"https://cdn.example.com/cat.png"}]}`
	upstream := &auxiliaryHTTPRecorder{
		responses: []*http.Response{
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(upstreamBody)),
			},
			b64BackfillImageResponse(http.StatusOK, "image/png", b64BackfillPNGBytes),
		},
	}
	svc.Requests.Transport = upstream

	result, err := svc.ForwardImages(context.Background(), c, b64BackfillProvider(false), body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)

	require.Len(t, upstream.requests, 1)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, upstreamBody, rec.Body.String())
}

// TestOpenAIGatewayService_HandleOpenAIProviderUpstreamError_ImageRateLimitDoesNotBlockWholeProvider
// observes an image-input rate-limit error. Health writes the
// image-generation scope and leaves the tested whole-provider scheduling
// block absent for the supplied image model.
func TestOpenAIGatewayService_HandleOpenAIProviderUpstreamError_ImageRateLimitDoesNotBlockWholeProvider(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{}
	svc := newImagesFixture(imagesFixtureInputs{observer: gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: repo, Options: providerimages.HealthOptions{}})})
	provider := &gatewayprovider.ExecutionProvider{Record: providerimages.Record{LoadLocation: time.LoadLocation, ID: 203, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	body := []byte(`{"error":{"type":"rate_limit_exceeded","message":"Rate limit reached for gpt-image-2-codex (for limit gpt-image) on input-images per min. Please try again in 1s."}}`)

	disabled := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusTooManyRequests, http.Header{}, body, false, "gpt-image-2").StopScheduling

	require.False(t, disabled)
	require.Len(t, repo.ModelRateLimitCalls, 1)
	require.Equal(t, providerimages.OpenAIImageGenerationRateLimitKey, repo.ModelRateLimitCalls[0].Scope)
	require.False(t, svc.Output.Health.Runtime.Blocked(provider.Record.ID, func() string {
		return providerimages.RefreshCredentialIdentity(gatewayprovider.ExecutionRecord(provider))
	}))
}

// TestOpenAIGatewayServiceForwardImages_ImageRateLimitReturnsFailoverAndCoolsCapability
// receives an OAuth image-input rate limit during forwarding. The attempt
// returns the original 429 failover evidence and writes the provider
// image-generation cooldown scope.
func TestOpenAIGatewayServiceForwardImages_ImageRateLimitReturnsFailoverAndCoolsCapability(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{}
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat"}`)
	errorBody := `{"error":{"type":"rate_limit_exceeded","message":"Rate limit reached for gpt-image-2-codex (for limit gpt-image) in organization org on input-images per min: Limit 4000, Used 4000. Please try again in 1s."}}`

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{observer: gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: repo, Options: providerimages.HealthOptions{}}), transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"X-Request-Id": []string{"req_img_rate_limited"}},
			Body:       io.NopCloser(strings.NewReader(errorBody)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 204,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "input-images per min")
	require.Len(t, repo.ModelRateLimitCalls, 1)
	require.Equal(t, providerimages.OpenAIImageGenerationRateLimitKey, repo.ModelRateLimitCalls[0].Scope)
}

// TestOpenAIGatewayServiceForwardImages_TextFallbackDoesNotCoolImageCapability
// receives a completed response containing ordinary text and no image. The
// attempt permits another-provider recovery with 502 and writes no
// image-scope cooldown.
func TestOpenAIGatewayServiceForwardImages_TextFallbackDoesNotCoolImageCapability(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{}
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat"}`)
	upstreamSSE := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"model\":\"gpt-5.4-mini\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Here's a polished image prompt for your request.\"}]}]}}\n\n"

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{store: repo, transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 205,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")

	require.Nil(t, result)
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.False(t, failoverErr.RetryableOnSameProvider)
	// The text-only result permits this request to retry on another provider.
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	// No image cooldown is written for the text-only result.
	require.Empty(t, repo.ModelRateLimitCalls,
		"模型回文字只说明这一轮没出图，不构成提供商 30 分钟不可用的证据")
}

// TestOpenAIGatewayServiceForwardImages_StructuredUnavailableCoolsImageCapability
// receives an explicit failed response naming unavailable image generation.
// The attempt writes one image-scope cooldown with its expected reason and
// default reset interval.
func TestOpenAIGatewayServiceForwardImages_StructuredUnavailableCoolsImageCapability(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{}
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat"}`)
	upstreamSSE := "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"r\",\"error\":" +
		"{\"type\":\"upstream_error\",\"code\":\"image_generation_unavailable\"," +
		"\"message\":\"image generation tool is not available for this provider\"}}}\n\n"

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{store: repo, transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 206,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	before := time.Now()
	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")

	require.Nil(t, result)
	require.Error(t, err)
	require.Len(t, repo.ModelRateLimitCalls, 1)
	call := repo.ModelRateLimitCalls[0]
	require.Equal(t, provider.Record.ID, call.ProviderID)
	require.Equal(t, providerimages.OpenAIImageGenerationRateLimitKey, call.Scope)
	require.Equal(t, upstreamopenai.OpenAIImagesOAuthUnavailableReason, call.Reason)
	// Allow scheduling overhead between fixture clock capture and default
	// cooldown application.
	require.WithinDuration(t, before.Add(upstreamopenai.OpenAIImagesOAuthUnavailableDefaultCooldown), call.ResetAt, time.Second)
}

// TestOpenAIGatewayService_CoolOpenAIImagesOAuthToolUsesConfiguredCooldown
// loads a seven-minute runtime setting before confirmed tool cooldown. The
// provider write uses that configured reset time within the test's scheduling
// tolerance.
func TestOpenAIGatewayService_CoolOpenAIImagesOAuthToolUsesConfiguredCooldown(t *testing.T) {
	providerRepo := &gatewaytestkit.ModelHealthStore{}
	settingRepo := settingstestkit.NewMemory()
	settingRepo.Data[providerimages.SettingKeyOpenAIImagesOAuthUnavailableCooldownSettings] = `{"cooldown_minutes":7}`
	svc := newImagesFixture(imagesFixtureInputs{store: providerRepo})
	svc.Cooldown.Settings = providerimages.NewRuntimeSettings(settingRepo, settingscore.ErrSettingNotFound).GetOpenAIImagesOAuthUnavailableCooldownSettings

	before := time.Now()
	svc.Cooldown.Apply(context.Background(), &providerimages.Record{LoadLocation: time.LoadLocation, ID: 206, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth})

	require.Len(t, providerRepo.ModelRateLimitCalls, 1)
	// Allow scheduling overhead when comparing the configured cooldown against
	// fixture start time.
	require.WithinDuration(t, before.Add(7*time.Minute), providerRepo.ModelRateLimitCalls[0].ResetAt, time.Second)
}

// TestOpenAIGatewayServiceForwardImages_CapabilityLossCoolsImageScope
// receives a missing image-tool-choice error during generation. Health writes
// one image scope with the capability-loss reason and its expected reset
// interval.
func TestOpenAIGatewayServiceForwardImages_CapabilityLossCoolsImageScope(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{}
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat"}`)
	errorBody := `{"error":{"message":"Tool choice 'image_generation' not found in 'tools' parameter.","param":"tool_choice","type":"invalid_request_error"}}`

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{observer: gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: repo, Options: providerimages.HealthOptions{}}), transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"X-Request-Id": []string{"req_img_capability_lost"}},
			Body:       io.NopCloser(strings.NewReader(errorBody)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 205,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	before := time.Now()
	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")

	require.Nil(t, result)
	require.Error(t, err)
	require.Len(t, repo.ModelRateLimitCalls, 1)
	call := repo.ModelRateLimitCalls[0]
	require.Equal(t, provider.Record.ID, call.ProviderID)
	require.Equal(t, providerimages.OpenAIImageGenerationRateLimitKey, call.Scope)
	require.Equal(t, providerimages.OpenAIImageCapabilityLossReason, call.Reason)
	// Allow scheduling overhead between fixture start and capability-loss
	// cooldown application.
	require.WithinDuration(t, before.Add(providerimages.OpenAIImageCapabilityLossCooldown), call.ResetAt, time.Second)
}

// TestOpenAIGatewayServiceHandleUpstreamError_PassthroughCapabilityLossDoesNotCool
// applies the same tool-choice error to a text model. Health leaves image
// cooldown and the tested provider-wide block absent for that model scope.
func TestOpenAIGatewayServiceHandleUpstreamError_PassthroughCapabilityLossDoesNotCool(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{}
	svc := newImagesFixture(imagesFixtureInputs{observer: gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: repo, Options: providerimages.HealthOptions{}})})
	provider := &gatewayprovider.ExecutionProvider{Record: providerimages.Record{LoadLocation: time.LoadLocation, ID: 206, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	body := []byte(`{"error":{"message":"Tool choice 'image_generation' not found in 'tools' parameter.","param":"tool_choice","type":"invalid_request_error"}}`)

	disabled := gatewayprovider.ApplyOpenAIResponseHealth(context.Background(), svc.Output.Health, provider, http.StatusBadRequest, http.Header{}, body, false, "gpt-5.5").StopScheduling

	require.False(t, disabled)
	require.Empty(t, repo.ModelRateLimitCalls)
	require.False(t, svc.Output.Health.Runtime.Blocked(provider.Record.ID, func() string {
		return providerimages.RefreshCredentialIdentity(gatewayprovider.ExecutionRecord(provider))
	}))
}

// TestOpenAISetupTokenImagesUsesOAuthResponsesPath dispatches a setup-token
// provider and receives status 429. The request reaches the Codex Responses
// endpoint and returns same-provider retry evidence with a deadline.
func TestOpenAISetupTokenImagesUsesOAuthResponsesPath(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}}
	svc := newImagesFixture(imagesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 73,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeSetupToken,
			Credentials: map[string]any{"access_token": "setup-token"},
		},
	}
	parsed := &media.ImageRequest{
		Endpoint:       upstreamcore.OpenAIImagesGenerationsEndpoint,
		Model:          "gpt-image-2",
		Prompt:         "draw a square",
		N:              1,
		ResponseFormat: "b64_json",
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, nil, parsed, "")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameProvider)
	require.False(t, failoverErr.SameProviderRetryDeadline.IsZero())
	require.Contains(t, upstream.lastReq.URL.String(), "/backend-api/codex/responses")
}

// Write delegates while its configured write-call allowance remains and
// otherwise returns the injected client error. The counter increments before
// the underlying writer returns, so it counts delegations even if that writer
// fails.
func (w *failingOpenAIImageWriter) Write(p []byte) (int, error) {
	if w.writes >= w.failAfter {
		return 0, errors.New("write failed: client disconnected")
	}
	w.writes++
	return w.ResponseWriter.Write(p)
}

// TestOpenAIGatewayServiceParseOpenAIImagesRequest_JSON parses explicit
// generation model, dimensions, quality and stream mode. The result retains
// those fields, classifies its billing size, and requires native image
// capability.
func TestOpenAIGatewayServiceParseOpenAIImagesRequest_JSON(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","size":"1024x1024","quality":"high","stream":true}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "/v1/images/generations", parsed.Endpoint)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, "draw a cat", parsed.Prompt)
	require.True(t, parsed.Stream)
	require.Equal(t, "1024x1024", parsed.Size)
	require.Equal(t, "1K", parsed.SizeTier)
	require.Equal(t, providerimages.OpenAIImagesCapabilityNative, parsed.RequiredCapability)
	require.False(t, parsed.Multipart)
}

// TestOpenAIGatewayServiceParseOpenAIImagesRequest_MultipartEdit parses
// uploaded source bytes beside form prompt, model and size. Editing retains
// the upload, multipart flag, normalized size tier and native capability
// requirement.
func TestOpenAIGatewayServiceParseOpenAIImagesRequest_MultipartEdit(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace background"))
	require.NoError(t, writer.WriteField("size", "1536x1024"))
	part, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("fake-image-bytes"))
	require.NoError(t, err)
	// Finalize the multipart boundary before parsing the buffer so source upload
	// bytes and form values belong to one complete client request.
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body.Bytes(), true)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "/v1/images/edits", parsed.Endpoint)
	require.True(t, parsed.Multipart)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, "replace background", parsed.Prompt)
	require.Equal(t, "1536x1024", parsed.Size)
	require.Equal(t, "2K", parsed.SizeTier)
	require.Len(t, parsed.Uploads, 1)
	require.Equal(t, providerimages.OpenAIImagesCapabilityNative, parsed.RequiredCapability)
}

// TestOpenAIImagesRequestModerationBody_JSONEditIncludesInputImageURLs
// supplies source and mask URLs with an editing prompt. Moderation extraction
// retains the prompt and both media identities from the parsed request
// representation.
func TestOpenAIImagesRequestModerationBody_JSONEditIncludesInputImageURLs(t *testing.T) {
	parsed := &media.ImageRequest{
		Endpoint:       upstreamcore.OpenAIImagesEditsEndpoint,
		Prompt:         "replace background",
		InputImageURLs: []string{"https://example.com/source.png"},
		MaskImageURL:   "https://example.com/mask.png",
	}

	input := moderation.ExtractContentModerationInput(moderation.ContentModerationProtocolOpenAIImages, parsed.ModerationBody())

	require.Equal(t, "replace background", input.Text)
	require.Equal(t, []string{"https://example.com/source.png", "https://example.com/mask.png"}, input.Images)
}

// TestOpenAIGatewayServiceParseOpenAIImagesRequest_NormalizesOfficialAndCustomSizes
// compares standard, custom, portrait and auto dimensions. Parsing retains
// the supplied size spelling while assigning each tested request its expected
// billing tier.
func TestOpenAIGatewayServiceParseOpenAIImagesRequest_NormalizesOfficialAndCustomSizes(t *testing.T) {
	tests := []struct {
		size     string // supplied image dimension spelling
		wantTier string // expected image billing tier
	}{
		{size: "1024x1024", wantTier: "1K"},
		{size: "1536x1024", wantTier: "2K"},
		{size: "1024x1536", wantTier: "2K"},
		{size: "2048x2048", wantTier: "2K"},
		{size: "2048x1152", wantTier: "2K"},
		{size: "3840x2160", wantTier: "4K"},
		{size: "2160x3840", wantTier: "4K"},
		{size: "1024X768", wantTier: "1K"},
		{size: "1280x768", wantTier: "2K"},
		{size: "2560x1440", wantTier: "4K"},
		{size: "2560x1600", wantTier: "4K"},
		{size: "auto", wantTier: "2K"},
	}

	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","size":"` + tt.size + `"}`)

			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req

			parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
			require.NoError(t, err)
			require.NotNil(t, parsed)
			require.Equal(t, tt.size, parsed.Size)
			require.Equal(t, tt.wantTier, parsed.SizeTier)
		})
	}
}

// TestOpenAIGatewayServiceParseOpenAIImagesRequest_UnknownSizesDoNotBlockPassthrough
// supplies unusual, malformed and overflowing dimension spellings. The parser
// retains each value for forwarding and assigns its tested recognized or
// default tier.
func TestOpenAIGatewayServiceParseOpenAIImagesRequest_UnknownSizesDoNotBlockPassthrough(t *testing.T) {
	tests := []struct {
		size     string // supplied image dimension spelling
		wantTier string // expected image billing tier
	}{
		{size: "2048x1153", wantTier: "2K"},
		{size: "4096x1024", wantTier: "4K"},
		{size: "3840x1024", wantTier: "4K"},
		{size: "512x512", wantTier: "1K"},
		{size: "invalid", wantTier: "2K"},
		{size: "999999999999999999999999999x2", wantTier: "2K"},
	}

	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","size":"` + tt.size + `"}`)

			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req

			parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
			require.NoError(t, err)
			require.NotNil(t, parsed)
			require.Equal(t, tt.size, parsed.Size)
			require.Equal(t, tt.wantTier, parsed.SizeTier)
		})
	}
}

// TestOpenAIGatewayServiceParseOpenAIImagesRequest_LegacyImageModelUnknownSizePassthrough
// uses a legacy image model with custom dimensions. Parsing retains that
// requested size and reports the expected two-K billing tier.
func TestOpenAIGatewayServiceParseOpenAIImagesRequest_LegacyImageModelUnknownSizePassthrough(t *testing.T) {
	body := []byte(`{"model":"gpt-image-1.5","prompt":"draw a cat","size":"2048x1152"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "2048x1152", parsed.Size)
	require.Equal(t, "2K", parsed.SizeTier)
}

// TestOpenAIGatewayServiceParseOpenAIImagesRequest_MultipartEditWithMaskAndNativeOptions
// supplies uploaded source and mask plus format, fidelity, compression and
// partial count. Parsed editing retains those options and its native
// capability requirement.
func TestOpenAIGatewayServiceParseOpenAIImagesRequest_MultipartEditWithMaskAndNativeOptions(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace foreground"))
	require.NoError(t, writer.WriteField("output_format", "png"))
	require.NoError(t, writer.WriteField("input_fidelity", "high"))
	require.NoError(t, writer.WriteField("output_compression", "80"))
	require.NoError(t, writer.WriteField("partial_images", "2"))

	imageHeader := make(textproto.MIMEHeader)
	imageHeader.Set("Content-Disposition", `form-data; name="image"; filename="source.png"`)
	imageHeader.Set("Content-Type", "image/png")
	imagePart, err := writer.CreatePart(imageHeader)
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("source-image-bytes"))
	require.NoError(t, err)

	maskHeader := make(textproto.MIMEHeader)
	maskHeader.Set("Content-Disposition", `form-data; name="mask"; filename="mask.png"`)
	maskHeader.Set("Content-Type", "image/png")
	maskPart, err := writer.CreatePart(maskHeader)
	require.NoError(t, err)
	_, err = maskPart.Write([]byte("mask-image-bytes"))
	require.NoError(t, err)

	// Finalize the multipart boundary before parsing so source and mask uploads
	// remain associated with the same native editing options in this request.
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body.Bytes(), true)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Len(t, parsed.Uploads, 1)
	require.NotNil(t, parsed.MaskUpload)
	require.True(t, parsed.HasMask)
	require.Equal(t, "png", parsed.OutputFormat)
	require.Equal(t, "high", parsed.InputFidelity)
	require.NotNil(t, parsed.OutputCompression)
	require.Equal(t, 80, *parsed.OutputCompression)
	require.NotNil(t, parsed.PartialImages)
	require.Equal(t, 2, *parsed.PartialImages)
	require.Equal(t, providerimages.OpenAIImagesCapabilityNative, parsed.RequiredCapability)
}

// TestOpenAIGatewayServiceParseOpenAIImagesRequest_PromptOnlyDefaultsRemainBasic
// parses a generation prompt without explicit model or size. The default
// image model is retained while the request requires basic capability.
func TestOpenAIGatewayServiceParseOpenAIImagesRequest_PromptOnlyDefaultsRemainBasic(t *testing.T) {
	body := []byte(`{"prompt":"draw a cat"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, providerimages.OpenAIImagesCapabilityBasic, parsed.RequiredCapability)
}

// TestOpenAIGatewayServiceParseOpenAIImagesRequest_ExplicitSizeRequiresNativeCapability
// supplies dimensions alongside a prompt and no explicit model. Parsing
// treats that option as native image intent rather than the prompt-only basic
// case.
func TestOpenAIGatewayServiceParseOpenAIImagesRequest_ExplicitSizeRequiresNativeCapability(t *testing.T) {
	body := []byte(`{"prompt":"draw a cat","size":"1024x1024"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, providerimages.OpenAIImagesCapabilityNative, parsed.RequiredCapability)
}

// TestOpenAIGatewayServiceParseOpenAIImagesRequest_RejectsNonImageModel
// parses a text-model request with image validation enabled. The parser
// returns no request and includes the rejected model in its tested
// image-family error.
func TestOpenAIGatewayServiceParseOpenAIImagesRequest_RejectsNonImageModel(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","prompt":"draw a cat"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.Nil(t, parsed)
	require.ErrorContains(t, err, `images endpoint requires an image model, got "gpt-5.4"`)
}

// TestOpenAIGatewayServiceParseOpenAIImagesRequestForRouting parses an
// unresolved image alias before group routing validation. A valid mapped
// image model updates capability, while a mapped text model returns the
// tested family error.
func TestOpenAIGatewayServiceParseOpenAIImagesRequestForRouting(t *testing.T) {
	body := []byte(`{"model":"draw-alias","prompt":"draw a cat"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, false)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "draw-alias", parsed.Model)
	require.NoError(t, parsed.ValidateRoutingModel("gpt-image-1"))
	require.Equal(t, providerimages.OpenAIImagesCapabilityNative, parsed.RequiredCapability)
	require.ErrorContains(t, parsed.ValidateRoutingModel("gpt-5.4"), `images endpoint requires an image model, got "gpt-5.4"`)
}

// TestOpenAIGatewayServiceParseOpenAIImagesRequest_AllowsGrokImageModels
// checks the tested Grok generation and editing model spellings through image
// parsing. Each name remains intact and carries the expected native
// capability requirement.
func TestOpenAIGatewayServiceParseOpenAIImagesRequest_AllowsGrokImageModels(t *testing.T) {
	for _, model := range []string{"grok-imagine", "grok-imagine-image", "grok-imagine-image-quality", "grok-imagine-edit"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw a cat","response_format":"b64_json"}`, model))
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req

			parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
			require.NoError(t, err)
			require.NotNil(t, parsed)
			require.Equal(t, model, parsed.Model)
			require.Equal(t, providerimages.OpenAIImagesCapabilityNative, parsed.RequiredCapability)
		})
	}
}

// TestOpenAIGatewayServiceParseOpenAIImagesRequest_JSONEditURLs parses
// source/mask URLs beside editing options. It retains those references, mask
// presence, compression and partial count under native capability
// classification.
func TestOpenAIGatewayServiceParseOpenAIImagesRequest_JSONEditURLs(t *testing.T) {
	body := []byte(`{
		"model":"gpt-image-2",
		"prompt":"replace the background",
		"images":[{"image_url":"https://example.com/source.png"}],
		"mask":{"image_url":"https://example.com/mask.png"},
		"input_fidelity":"high",
		"output_compression":90,
		"partial_images":2,
		"response_format":"url"
	}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, []string{"https://example.com/source.png"}, parsed.InputImageURLs)
	require.Equal(t, "https://example.com/mask.png", parsed.MaskImageURL)
	require.Equal(t, "high", parsed.InputFidelity)
	require.NotNil(t, parsed.OutputCompression)
	require.Equal(t, 90, *parsed.OutputCompression)
	require.NotNil(t, parsed.PartialImages)
	require.Equal(t, 2, *parsed.PartialImages)
	require.True(t, parsed.HasMask)
	require.Equal(t, providerimages.OpenAIImagesCapabilityNative, parsed.RequiredCapability)
}

// TestProviderSupportsOpenAIImageCapability_OAuthSupportsNative queries an
// ordinary OpenAI OAuth provider view. The tested view accepts both basic and
// native Images requirements for that credential identity.
func TestProviderSupportsOpenAIImageCapability_OAuthSupportsNative(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
		},
	}

	require.True(t, provider.View().SupportsOpenAIImageCapability(providerimages.OpenAIImagesCapabilityBasic))
	require.True(t, provider.View().SupportsOpenAIImageCapability(providerimages.OpenAIImagesCapabilityNative))
}

// TestProviderSupportsOpenAIImageCapability_SetupTokenSupportsNative queries
// an OpenAI setup-token provider view. Both image requirements qualify, while
// the same tested identity does not qualify for Embeddings.
func TestProviderSupportsOpenAIImageCapability_SetupTokenSupportsNative(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeSetupToken,
		},
	}

	require.True(t, provider.View().SupportsOpenAIImageCapability(providerimages.OpenAIImagesCapabilityBasic))
	require.True(t, provider.View().SupportsOpenAIImageCapability(providerimages.OpenAIImagesCapabilityNative))
	require.False(t, provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(provider), providerimages.OpenAIEndpointCapabilityEmbeddings))
}

// TestProviderSupportsOpenAIImageCapability_EmptyRequirementDoesNotRejectGrok
// compares absent image requirement with an explicit basic requirement on
// Grok OAuth. The empty requirement passes, while the explicit tested OpenAI
// capability rejects that identity.
func TestProviderSupportsOpenAIImageCapability_EmptyRequirementDoesNotRejectGrok(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Type: capability.ProviderTypeOAuth,
		},
	}

	require.True(t, provider.View().SupportsOpenAIImageCapability(""))
	require.False(t, provider.View().SupportsOpenAIImageCapability(providerimages.OpenAIImagesCapabilityBasic))
}

// TestBuildOpenAIImagesURL_HandlesVersionedBaseURL combines generation or
// edit paths with bare, versioned and already complete bases. Endpoint
// construction retains the tested version prefixes and produces the expected
// operation URL.
func TestBuildOpenAIImagesURL_HandlesVersionedBaseURL(t *testing.T) {
	require.Equal(t,
		"https://image-upstream.example/v1/images/generations",
		httpclient.BuildOpenAIEndpointURL("https://image-upstream.example/v1", upstreamcore.OpenAIImagesGenerationsEndpoint),
	)
	require.Equal(t,
		"https://open.bigmodel.cn/api/paas/v4/images/generations",
		httpclient.BuildOpenAIEndpointURL("https://open.bigmodel.cn/api/paas/v4", upstreamcore.OpenAIImagesGenerationsEndpoint),
	)
	require.Equal(t,
		"https://image-upstream.example/v1/images/edits",
		httpclient.BuildOpenAIEndpointURL("https://image-upstream.example/v1/", upstreamcore.OpenAIImagesEditsEndpoint),
	)
	require.Equal(t,
		"https://image-upstream.example/v1/images/generations",
		httpclient.BuildOpenAIEndpointURL("https://image-upstream.example", upstreamcore.OpenAIImagesGenerationsEndpoint),
	)
	require.Equal(t,
		"https://image-upstream.example/v1/images/generations",
		httpclient.BuildOpenAIEndpointURL("https://image-upstream.example/v1/images/generations", upstreamcore.OpenAIImagesGenerationsEndpoint),
	)
}

// parseOpenAIImageTestSSEEvents groups fixture output on blank LF lines and
// returns labeled data observations. It recognizes the exact event: and data:
// prefixes used by these output assertions, trimming captured values and
// skipping chunks with neither field.
//
// Name and Data are accumulated only within one chunk so a later event cannot
// borrow a prior label. Repeated data lines replace the stored Data value;
// this fixture helper does not assemble multiline JSON or implement a
// complete SSE reader. Native multiline parsing is exercised through separate
// execution assertions before output reaches this helper.
func parseOpenAIImageTestSSEEvents(body string) []openAIImageTestSSEEvent {
	// Reset event identity at each blank-line chunk so labels and payloads from
	// separate completed or partial image events remain paired in fixture assertions.
	chunks := strings.Split(body, "\n\n")
	events := make([]openAIImageTestSSEEvent, 0, len(chunks))
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		var event openAIImageTestSSEEvent
		for line := range strings.SplitSeq(chunk, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				event.Name = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			case strings.HasPrefix(line, "data: "):
				event.Data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
		}
		if event.Name != "" || event.Data != "" {
			events = append(events, event)
		}
	}
	return events
}

// findOpenAIImageTestSSEEvent returns the first event with the supplied exact
// label. Missing labels return a zero event and false, letting callers guard
// data assertions before reading the fixture payload.
func findOpenAIImageTestSSEEvent(events []openAIImageTestSSEEvent, name string) (openAIImageTestSSEEvent, bool) {
	for _, event := range events {
		if event.Name == name {
			return event, true
		}
	}
	return openAIImageTestSSEEvent{}, false
}

// TestOpenAIGatewayServiceForwardImages_OAuthAppliesProviderMappingAndReturnsAllImages
// maps the requested image model into a three-image tool exchange. Returned
// model identities remain separate, all images survive, and image-tool usage
// takes its expected precedence.
func TestOpenAIGatewayServiceForwardImages_OAuthAppliesProviderMappingAndReturnsAllImages(t *testing.T) {
	body := []byte(`{"model":"gpt-image-1","prompt":"draw a cat","size":"1024x1024","quality":"high","n":3}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set("api_key", &apikey.APIKey{ID: 42})

	svc := newImagesFixture(imagesFixtureInputs{})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	upstream := &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_123"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"usage\":{\"input_tokens\":11,\"output_tokens\":22,\"input_tokens_details\":{\"cached_tokens\":3},\"output_tokens_details\":{\"image_tokens\":7}},\"tool_usage\":{\"image_gen\":{\"input_tokens\":46,\"output_tokens\":2459,\"output_tokens_details\":{\"image_tokens\":2459},\"images\":3}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2UtMQ==\",\"revised_prompt\":\"draw a cat 1\",\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\"},{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2UtMg==\",\"revised_prompt\":\"draw a cat 2\",\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\"},{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2UtMw==\",\"revised_prompt\":\"draw a cat 3\",\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\"}]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc.Requests.Transport = upstream

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token":       "token-123",
				"chatgpt_account_id": "acct-123",
				"model_mapping":      map[string]any{"gpt-image-1": "gpt-image-2"},
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gpt-image-1", result.Model)
	require.Equal(t, "gpt-image-2", result.UpstreamModel)
	require.Equal(t, 3, result.ImageCount)
	require.Equal(t, 46, result.Usage.InputTokens)
	require.Equal(t, 2459, result.Usage.OutputTokens)
	require.Equal(t, 2459, result.Usage.ImageOutputTokens)

	require.NotNil(t, upstream.lastReq)
	require.Equal(t, ChatgptCodexURL, upstream.lastReq.URL.String())
	require.Equal(t, "chatgpt.com", upstream.lastReq.Host)
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "acct-123", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Equal(t, "responses=experimental", upstream.lastReq.Header.Get("OpenAI-Beta"))

	require.Equal(t, upstreamopenai.ImagesResponsesMainModel, gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "image_generation", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "generate", gjson.GetBytes(upstream.lastBody, "tools.0.action").String())
	require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.lastBody, "tools.0.model").String())
	require.Equal(t, "1024x1024", gjson.GetBytes(upstream.lastBody, "tools.0.size").String())
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "tools.0.quality").String())
	require.Equal(t, int64(3), gjson.GetBytes(upstream.lastBody, "tools.0.n").Int())
	require.Equal(t, "draw a cat", gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "gpt-image-1", gjson.Get(rec.Body.String(), "model").String())
	require.Len(t, gjson.Get(rec.Body.String(), "data").Array(), 3)
	require.Equal(t, "aW1hZ2UtMQ==", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
	require.Equal(t, "aW1hZ2UtMg==", gjson.Get(rec.Body.String(), "data.1.b64_json").String())
	require.Equal(t, "aW1hZ2UtMw==", gjson.Get(rec.Body.String(), "data.2.b64_json").String())
	require.Equal(t, "draw a cat 1", gjson.Get(rec.Body.String(), "data.0.revised_prompt").String())
	require.Equal(t, "draw a cat 3", gjson.Get(rec.Body.String(), "data.2.revised_prompt").String())
}

// TestBoundedJSONNonNegativeInt compares exact scale reduction with
// fractional, overflow and hostile exponent inputs. Parsing accepts the
// tested bounded integer forms and zero exceptions while rejecting invalid
// syntax or quantities.
func TestBoundedJSONNonNegativeInt(t *testing.T) {
	tests := []struct {
		name string // numeric parsing case label
		raw  string // supplied JSON number spelling
		want int    // expected parsed integer quantity
		ok   bool   // expected numeric parsing acceptance
	}{
		{name: "scale reduction before accumulation", raw: `10000000000000000000e-19`, want: 1, ok: true},
		{name: "decimal scale reduction", raw: `10000000000000000000.0e-19`, want: 1, ok: true},
		{name: "fractional after scale reduction", raw: `10000000000000000001e-19`, ok: false},
		{name: "overflow after scale reduction", raw: `92233720368547758080e-1`, ok: false},
		{name: "zero with negative exponent", raw: `0e-100`, want: 0, ok: true},
		{name: "zero beyond exponent bound", raw: `0e101`, want: 0, ok: true},
		{name: "zero padded decimal beyond exponent bound", raw: `0.000000e+000000000000000000000000000000000000000000000000101`, want: 0, ok: true},
		{name: "zero padded exponent", raw: `1e0000`, want: 1, ok: true},
		{name: "negative zero syntax", raw: `-0e101`, ok: false},
		{name: "hostile exponent", raw: `1e-1000`, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := openai.BoundedJSONNonNegativeInt(gjson.Parse(tt.raw))
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestOpenAIGatewayServiceForwardImages_OAuthUpstreamHTTPErrorSurfacesRealError
// receives an invalid size rejection before image output. The returned typed
// error and HTTP JSON retain tested category, code, parameter and client
// message.
func TestOpenAIGatewayServiceForwardImages_OAuthUpstreamHTTPErrorSurfacesRealError(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set("api_key", &apikey.APIKey{ID: 42})

	svc := newImagesFixture(imagesFixtureInputs{})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	svc.Requests.Transport = &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusBadRequest,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"X-Request-Id": []string{"req_img_badreq"},
			},
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"message":"Invalid value for 'size': expected one of 1024x1024, 1536x1024.","type":"invalid_request_error","param":"size","code":"unknown_parameter"}}`,
			)),
		},
	}

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.Nil(t, result)

	var upstreamErr *upstreamopenai.OpenAIImagesUpstreamError
	require.ErrorAs(t, err, &upstreamErr)
	require.Equal(t, http.StatusBadRequest, upstreamErr.StatusCode)
	require.Equal(t, "invalid_request_error", upstreamErr.ErrorType)
	require.Equal(t, "unknown_parameter", upstreamErr.Code)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, "unknown_parameter", gjson.Get(rec.Body.String(), "error.code").String())
	require.Equal(t, "size", gjson.Get(rec.Body.String(), "error.param").String())
	require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), "Invalid value for 'size'")
}

// TestOpenAIGatewayServiceForwardImages_OAuthNonStreamModerationBlockedReturnsClientError
// receives safety failure in a status-200 upstream stream. Buffered
// conversion returns a client 400 with the moderation code and its tested
// reason.
func TestOpenAIGatewayServiceForwardImages_OAuthNonStreamModerationBlockedReturnsClientError(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw blocked image","response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set("api_key", &apikey.APIKey{ID: 42})

	svc := newImagesFixture(imagesFixtureInputs{})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	svc.Requests.Transport = &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_blocked"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000020}}\n\n" +
					"data: {\"type\":\"error\",\"error\":{\"type\":\"image_generation_user_error\",\"code\":\"moderation_blocked\",\"message\":\"Your request was rejected by the safety system. safety_violations=[sexual].\"}}\n\n" +
					"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_blocked\",\"status\":\"failed\",\"error\":{\"type\":\"image_generation_user_error\",\"code\":\"moderation_blocked\",\"message\":\"Your request was rejected by the safety system. safety_violations=[sexual].\"}}}\n\n",
			)),
		},
	}

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.Nil(t, result)
	var upstreamErr *upstreamopenai.OpenAIImagesUpstreamError
	require.ErrorAs(t, err, &upstreamErr)
	require.Equal(t, http.StatusBadRequest, upstreamErr.StatusCode)
	require.Equal(t, "moderation_blocked", upstreamErr.Code)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "image_generation_user_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, "moderation_blocked", gjson.Get(rec.Body.String(), "error.code").String())
	require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), "safety system")
}

// TestOpenAIGatewayServiceForwardImages_OAuthNonStreamServerErrorReturnsFailoverBeforeFlush
// receives server failure without image output. The attempt returns
// classified 502 recovery, leaves client output uncommitted and records one
// correlated Ops failover event.
func TestOpenAIGatewayServiceForwardImages_OAuthNonStreamServerErrorReturnsFailoverBeforeFlush(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_server_error"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000021}}\n\n" +
					"data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"The image service is temporarily unavailable.\"}}\n\n",
			)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 21,
			Name:     "openai-oauth-server-error",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "temporarily unavailable")
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*ops.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "failover", events[0].Kind)
	require.Equal(t, provider.Record.ID, events[0].ProviderID)
	require.Equal(t, http.StatusBadGateway, events[0].UpstreamStatusCode)
}

// TestOpenAIGatewayServiceForwardImages_OAuth429CarriesSameProviderRetryWindow
// receives a one-second Retry-After rejection. The classified failure retains
// same-provider permission, delay and the expected runtime retry deadline
// within the tested tolerance.
func TestOpenAIGatewayServiceForwardImages_OAuth429CarriesSameProviderRetryWindow(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	svc := newImagesFixture(imagesFixtureInputs{transport: &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"1"}, "X-Request-Id": []string{"req_img_oauth_429"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"rate limited"}}`)),
	}}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	provider := &gatewayprovider.ExecutionProvider{Record: providerimages.Record{LoadLocation: time.LoadLocation, ID: 22, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"access_token": "token-123"}}}
	startedAt := time.Now()

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameProvider)
	require.Equal(t, time.Second, failoverErr.SameProviderRetryDelay)
	// Allow request-building overhead when comparing the retry deadline with
	// fixture start time.
	require.WithinDuration(t, startedAt.Add(providerimages.RuntimeRetryWindow), failoverErr.SameProviderRetryDeadline, time.Second)
}

// TestShouldClassifyOpenAIUpstreamStreamReadErrorTransportStrings compares
// known transport error text with a canceled original request. Transport
// strings qualify normally, while that cancellation prevents classifying the
// tested EOF as recoverable transport failure.
func TestShouldClassifyOpenAIUpstreamStreamReadErrorTransportStrings(t *testing.T) {
	for _, message := range []string{"unexpected EOF", "connection reset by peer", "broken pipe", "use of closed network connection"} {
		t.Run(message, func(t *testing.T) {
			require.True(t, upstreamopenai.ShouldClassifyUpstreamStreamReadError(errors.New(message), httpclient.ErrResponseBodyTooLarge))
		})
	}

	// End the original request before checking EOF classification,
	// distinguishing client departure from transport strings that otherwise
	// qualify for recoverable read handling.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, upstreamopenai.ShouldClassifyUpstreamStreamReadError(errors.New("unexpected EOF"), httpclient.ErrResponseBodyTooLarge, canceledCtx))
}

// TestOpenAIGatewayServiceForwardImages_APIKeyGenerationUsesConfiguredV1BaseURL
// sends direct generation through a provider versioned base. Captured URL,
// bearer authentication, model and JSON content type match configuration, and
// one image result reaches output.
func TestOpenAIGatewayServiceForwardImages_APIKeyGenerationUsesConfiguredV1BaseURL(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"X-Request-Id": []string{"req_img_apikey"},
			},
			Body: io.NopCloser(strings.NewReader(`{"created":1710000007,"data":[{"b64_json":"aGVsbG8=","revised_prompt":"draw a cat"}]}`)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 6,
			Name:     "openai-apikey",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "test-api-key",
				"base_url": "https://image-upstream.example/v1",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, "gpt-image-2", result.Model)
	require.Equal(t, "gpt-image-2", result.UpstreamModel)

	upstream, ok := svc.Requests.Transport.(*auxiliaryHTTPRecorder)
	require.True(t, ok)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://image-upstream.example/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer test-api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
	require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "aGVsbG8=", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
}

// TestOpenAIGatewayServiceForwardImages_APIKeyAccessStateUsesTypedFailover
// receives organization suspension before client output. Recovery retains
// provider-auth stage, provider scope, request header identity and sanitized
// client presentation with next-provider action.
func TestOpenAIGatewayServiceForwardImages_APIKeyAccessStateUsesTypedFailover(t *testing.T) {
	body := []byte(`{"model":"gpt-image-1","prompt":"draw a cat"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := []byte(`{"error":{"code":"organization_suspended","message":"Organization has been suspended"}}`)
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"req_images_access_state"},
		},
		Body: io.NopCloser(bytes.NewReader(upstreamBody)),
	}}
	svc := newImagesFixture(imagesFixtureInputs{transport: upstream})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 51,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusForbidden, failoverErr.StatusCode)
	require.Equal(t, forwardcore.GatewayFailureStageProviderAuth, failoverErr.Stage)
	require.Equal(t, forwardcore.GatewayFailureScopeProvider, failoverErr.Scope)
	require.Equal(t, forwardcore.OpenAIUpstreamAccessStateReason, failoverErr.Reason)
	require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
	require.Equal(t, http.StatusBadGateway, failoverErr.ClientStatusCode)
	require.Equal(t, "Upstream access is temporarily unavailable, please retry later", failoverErr.ClientMessage)
	require.False(t, failoverErr.RetryableOnSameProvider)
	require.Equal(t, "req_images_access_state", http.Header(failoverErr.ResponseHeaders).Get("x-request-id"))
	require.False(t, c.Writer.Written())
}

// TestOpenAIGatewayServiceForwardImages_APIKeyStreamJSONResponseBillsImage
// requests streaming but receives ordinary JSON image data. The result
// retains streaming intent, observed image and token usage, while the client
// gets the supplied image document.
func TestOpenAIGatewayServiceForwardImages_APIKeyStreamJSONResponseBillsImage(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true,"response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"X-Request-Id": []string{"req_img_stream_json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"created":1710000008,"usage":{"input_tokens":12,"output_tokens":21,"output_tokens_details":{"image_tokens":9}},"data":[{"b64_json":"aGVsbG8=","revised_prompt":"draw a cat"}]}`)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 7,
			Name:     "openai-apikey",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "test-api-key",
				"base_url": "https://image-upstream.example/v1",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 21, result.Usage.OutputTokens)
	require.Equal(t, 9, result.Usage.ImageOutputTokens)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "aGVsbG8=", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
}

// TestOpenAIGatewayServiceForwardImages_APIKeyStreamRawJSONEventStreamFallbackBillsImage
// receives raw JSON under an event-stream content type. Compatibility reading
// retains the image and its token observations from that mislabeled response.
func TestOpenAIGatewayServiceForwardImages_APIKeyStreamRawJSONEventStreamFallbackBillsImage(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true,"response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream_json_mislabeled"},
			},
			Body: io.NopCloser(strings.NewReader(`{"created":1710000009,"usage":{"input_tokens":10,"output_tokens":18,"output_tokens_details":{"image_tokens":8}},"data":[{"b64_json":"ZmluYWw="}]}`)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 8,
			Name:     "openai-apikey",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "test-api-key",
				"base_url": "https://image-upstream.example/v1",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 18, result.Usage.OutputTokens)
	require.Equal(t, 8, result.Usage.ImageOutputTokens)
	require.Equal(t, "ZmluYWw=", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
}

// TestOpenAIGatewayServiceForwardImages_APIKeyStreamMultilineSSEDataBillsImage
// splits a completion object across consecutive data lines. The streaming
// reader joins the event and retains one image with its tested token usage.
func TestOpenAIGatewayServiceForwardImages_APIKeyStreamMultilineSSEDataBillsImage(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true,"response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream_multiline"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"image_generation.completed\",\n" +
					"data: \"usage\":{\"input_tokens\":10,\"output_tokens\":18,\"output_tokens_details\":{\"image_tokens\":8}},\n" +
					"data: \"b64_json\":\"ZmluYWw=\",\"output_format\":\"png\"}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 8,
			Name:     "openai-apikey",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key": "test-api-key",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 18, result.Usage.OutputTokens)
	require.Equal(t, 8, result.Usage.ImageOutputTokens)
}

// TestOpenAIGatewayServiceForwardImages_APIKeyEditUsesConfiguredV1BaseURL
// sends multipart editing through a versioned provider base. Captured
// operation URL, authentication and model form field match configuration, and
// the edited image reaches output.
func TestOpenAIGatewayServiceForwardImages_APIKeyEditUsesConfiguredV1BaseURL(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace background"))
	imagePart, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("png-image-content"))
	require.NoError(t, err)
	// Finish the multipart body before execution so provider model rewriting
	// receives complete form fields and the original uploaded image bytes.
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"X-Request-Id": []string{"req_img_edit_apikey"},
			},
			Body: io.NopCloser(strings.NewReader(`{"created":1710000008,"data":[{"b64_json":"ZWRpdGVk","revised_prompt":"replace background"}]}`)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body.Bytes(), true)
	require.NoError(t, err)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 7,
			Name:     "openai-apikey",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "test-api-key",
				"base_url": "https://image-upstream.example/v1/",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body.Bytes(), parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)

	upstream, ok := svc.Requests.Transport.(*auxiliaryHTTPRecorder)
	require.True(t, ok)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://image-upstream.example/v1/images/edits", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer test-api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Contains(t, upstream.lastReq.Header.Get("Content-Type"), "multipart/form-data")
	require.Contains(t, string(upstream.lastBody), `name="model"`)
	require.Contains(t, string(upstream.lastBody), "gpt-image-2")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "ZWRpdGVk", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
}

// TestOpenAIGatewayServiceForwardImages_OAuthStreamingTransformsEvents
// converts partial and completed Responses image events into Images SSE.
// Event names, data URLs, metadata and retained image-tool token observations
// follow the tested client representation.
func TestOpenAIGatewayServiceForwardImages_OAuthStreamingTransformsEvents(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true,"response_format":"url"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	upstream := &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000001,\"tools\":[{\"type\":\"image_generation\",\"model\":\"gpt-image-2\",\"background\":\"auto\",\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\"}]}}\n\n" +
					"data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"cGFydGlhbA==\",\"partial_image_index\":0,\"output_format\":\"png\",\"background\":\"auto\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000001,\"usage\":{\"input_tokens\":5,\"output_tokens\":9,\"output_tokens_details\":{\"image_tokens\":4}},\"tool_usage\":{\"image_gen\":{\"input_tokens\":46,\"output_tokens\":2459,\"output_tokens_details\":{\"image_tokens\":2459},\"images\":1}},\"tools\":[{\"type\":\"image_generation\",\"model\":\"gpt-image-2\",\"background\":\"auto\",\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\"}],\"output\":[{\"type\":\"image_generation_call\",\"result\":\"ZmluYWw=\",\"output_format\":\"png\"}]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc.Requests.Transport = upstream

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 2,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, openai.ForwardUsage{InputTokens: 46, OutputTokens: 2459, ImageOutputTokens: 2459}, result.Usage)
	events := parseOpenAIImageTestSSEEvents(rec.Body.String())
	partial, ok := findOpenAIImageTestSSEEvent(events, "image_generation.partial_image")
	require.True(t, ok)
	require.Equal(t, "image_generation.partial_image", gjson.Get(partial.Data, "type").String())
	require.Equal(t, int64(1710000001), gjson.Get(partial.Data, "created_at").Int())
	require.Equal(t, "cGFydGlhbA==", gjson.Get(partial.Data, "b64_json").String())
	require.Equal(t, "data:image/png;base64,cGFydGlhbA==", gjson.Get(partial.Data, "url").String())
	require.Equal(t, "gpt-image-2", gjson.Get(partial.Data, "model").String())
	require.Equal(t, "png", gjson.Get(partial.Data, "output_format").String())
	require.Equal(t, "high", gjson.Get(partial.Data, "quality").String())
	require.Equal(t, "1024x1024", gjson.Get(partial.Data, "size").String())
	require.Equal(t, "auto", gjson.Get(partial.Data, "background").String())

	completed, ok := findOpenAIImageTestSSEEvent(events, "image_generation.completed")
	require.True(t, ok)
	require.Equal(t, "image_generation.completed", gjson.Get(completed.Data, "type").String())
	require.Equal(t, int64(1710000001), gjson.Get(completed.Data, "created_at").Int())
	require.Equal(t, "ZmluYWw=", gjson.Get(completed.Data, "b64_json").String())
	require.Equal(t, "data:image/png;base64,ZmluYWw=", gjson.Get(completed.Data, "url").String())
	require.Equal(t, "gpt-image-2", gjson.Get(completed.Data, "model").String())
	require.Equal(t, "png", gjson.Get(completed.Data, "output_format").String())
	require.Equal(t, "high", gjson.Get(completed.Data, "quality").String())
	require.Equal(t, "1024x1024", gjson.Get(completed.Data, "size").String())
	require.Equal(t, "auto", gjson.Get(completed.Data, "background").String())
	require.JSONEq(t, `{"input_tokens":46,"output_tokens":2459,"output_tokens_details":{"image_tokens":2459},"images":1}`, gjson.Get(completed.Data, "usage").Raw)
	require.False(t, gjson.Get(completed.Data, "revised_prompt").Exists())
}

// TestOpenAIGatewayServiceForwardImages_OAuthStreamingModerationBlockedEmitsError
// receives a status-200 failed response with moderation evidence. The
// returned typed client error also appears in the tested Images SSE error
// event.
func TestOpenAIGatewayServiceForwardImages_OAuthStreamingModerationBlockedEmitsError(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw blocked image","stream":true,"response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	svc.Requests.Transport = &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream_blocked"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_blocked_stream\",\"status\":\"failed\",\"error\":{\"type\":\"image_generation_user_error\",\"code\":\"moderation_blocked\",\"message\":\"Your request was rejected by the safety system.\"}}}\n\n",
			)),
		},
	}

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 2,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.Nil(t, result)
	var upstreamErr *upstreamopenai.OpenAIImagesUpstreamError
	require.ErrorAs(t, err, &upstreamErr)
	require.Equal(t, http.StatusBadRequest, upstreamErr.StatusCode)
	require.Equal(t, "moderation_blocked", upstreamErr.Code)

	events := parseOpenAIImageTestSSEEvents(rec.Body.String())
	errorEvent, ok := findOpenAIImageTestSSEEvent(events, "error")
	require.True(t, ok)
	require.Equal(t, "error", gjson.Get(errorEvent.Data, "type").String())
	require.Equal(t, "image_generation_user_error", gjson.Get(errorEvent.Data, "error.type").String())
	require.Equal(t, "moderation_blocked", gjson.Get(errorEvent.Data, "error.code").String())
	require.Contains(t, gjson.Get(errorEvent.Data, "error.message").String(), "safety system")
}

// TestOpenAIGatewayServiceForwardImages_OAuthStreamingServerErrorBeforeFlushReturnsFailover
// receives a failed terminal before partial image content. Execution returns
// a classified 502 error and leaves response bytes uncommitted for another
// attempt.
func TestOpenAIGatewayServiceForwardImages_OAuthStreamingServerErrorBeforeFlushReturnsFailover(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true,"response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream_server_error"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_server_error\",\"status\":\"failed\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"The image service is temporarily unavailable.\"}}}\n\n",
			)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 23,
			Name:     "openai-oauth-stream-server-error",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "temporarily unavailable")
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

// TestOpenAIGatewayServiceForwardImages_OAuthStreamingServerErrorAfterFlushDoesNotFailover
// emits partial image content before server failure. It retains the typed
// image error, appends its error event and records exhausted retry evidence
// instead of another-provider classification.
func TestOpenAIGatewayServiceForwardImages_OAuthStreamingServerErrorAfterFlushDoesNotFailover(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true,"response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_server_error_after_partial"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"cGFydGlhbA==\",\"partial_image_index\":0,\"output_format\":\"png\"}\n\n" +
					"data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"The image service failed after partial output.\"}}\n\n",
			)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 22,
			Name:     "openai-oauth-partial-server-error",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	var upstreamErr *upstreamopenai.OpenAIImagesUpstreamError
	require.ErrorAs(t, err, &upstreamErr)
	require.True(t, upstreamopenai.IsOpenAIImagesRetryableUpstreamError(upstreamErr))
	require.True(t, c.Writer.Written())
	require.Contains(t, rec.Body.String(), "event: image_generation.partial_image")
	require.Contains(t, rec.Body.String(), "event: error")
	require.Contains(t, rec.Body.String(), "failed after partial output")

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*ops.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "retry_exhausted_failover", events[0].Kind)
}

// TestOpenAIGatewayServiceForwardImages_APIKeyStreamingDrainsAfterClientDisconnect
// rejects downstream writes after the first delegation. Native reading still
// reaches terminal image and token observations, returning the tested
// billable result despite the write failure.
func TestOpenAIGatewayServiceForwardImages_APIKeyStreamingDrainsAfterClientDisconnect(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Writer = &failingOpenAIImageWriter{ResponseWriter: c.Writer, failAfter: 1}

	svc := newImagesFixture(imagesFixtureInputs{ImageStreamDataIntervalTimeout: 1, ImageStreamKeepaliveInterval: 0, transport: &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream_disconnect_apikey"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"cGFydGlhbA==\"}\n\n" +
					"data: {\"type\":\"image_generation.completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":4,\"output_tokens_details\":{\"image_tokens\":2}},\"b64_json\":\"ZmluYWw=\",\"output_format\":\"png\"}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 8,
			Name:     "openai-apikey",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key": "test-api-key",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 2, result.Usage.ImageOutputTokens)
}

// TestOpenAIGatewayServiceForwardImages_OAuthEditsMultipartUsesResponsesAPI
// converts uploaded source and mask into tool input. Editing action, data
// URIs and output format survive while unsupported fidelity is omitted, and
// the final image retains its prompt.
func TestOpenAIGatewayServiceForwardImages_OAuthEditsMultipartUsesResponsesAPI(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace background with aurora"))
	require.NoError(t, writer.WriteField("input_fidelity", "high"))
	require.NoError(t, writer.WriteField("output_format", "webp"))
	require.NoError(t, writer.WriteField("quality", "high"))

	imageHeader := make(textproto.MIMEHeader)
	imageHeader.Set("Content-Disposition", `form-data; name="image"; filename="source.png"`)
	imageHeader.Set("Content-Type", "image/png")
	imagePart, err := writer.CreatePart(imageHeader)
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("png-image-content"))
	require.NoError(t, err)

	maskHeader := make(textproto.MIMEHeader)
	maskHeader.Set("Content-Disposition", `form-data; name="mask"; filename="mask.png"`)
	maskHeader.Set("Content-Type", "image/png")
	maskPart, err := writer.CreatePart(maskHeader)
	require.NoError(t, err)
	_, err = maskPart.Write([]byte("png-mask-content"))
	require.NoError(t, err)

	// Finish the multipart body before Responses conversion so source and mask
	// parts are both available when native editing input is assembled.
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set("api_key", &apikey.APIKey{ID: 100})

	svc := newImagesFixture(imagesFixtureInputs{})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body.Bytes(), true)
	require.NoError(t, err)

	upstream := &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_edit_123"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000002,\"usage\":{\"input_tokens\":13,\"output_tokens\":21,\"output_tokens_details\":{\"image_tokens\":8}},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"ZWRpdGVk\",\"revised_prompt\":\"replace background with aurora\",\"output_format\":\"webp\",\"quality\":\"high\"}]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc.Requests.Transport = upstream

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 3,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body.Bytes(), parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.lastBody, "tools.0.model").String())
	require.Equal(t, "edit", gjson.GetBytes(upstream.lastBody, "tools.0.action").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "tools.0.input_fidelity").Exists())
	require.Equal(t, "webp", gjson.GetBytes(upstream.lastBody, "tools.0.output_format").String())
	require.True(t, strings.HasPrefix(gjson.GetBytes(upstream.lastBody, "input.0.content.1.image_url").String(), "data:image/png;base64,"))
	require.True(t, strings.HasPrefix(gjson.GetBytes(upstream.lastBody, "tools.0.input_image_mask.image_url").String(), "data:image/png;base64,"))
	require.Equal(t, "replace background with aurora", gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String())
	require.Equal(t, "ZWRpdGVk", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
	require.Equal(t, "replace background with aurora", gjson.Get(rec.Body.String(), "data.0.revised_prompt").String())
}

// TestOpenAIGatewayServiceForwardImages_OAuthEditsStreamingTransformsEvents
// converts editing URL input and partial/final image output. The tool retains
// source and mask references, while image_edit events carry expected data
// URLs, metadata and image count.
func TestOpenAIGatewayServiceForwardImages_OAuthEditsStreamingTransformsEvents(t *testing.T) {
	body := []byte(`{
		"model":"gpt-image-2",
		"prompt":"replace background with aurora",
		"images":[{"image_url":"https://example.com/source.png"}],
		"mask":{"image_url":"https://example.com/mask.png"},
		"stream":true,
		"response_format":"url"
	}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	upstream := &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000003,\"tools\":[{\"type\":\"image_generation\",\"model\":\"gpt-image-2\",\"background\":\"transparent\",\"output_format\":\"webp\",\"quality\":\"high\",\"size\":\"1024x1024\"}]}}\n\n" +
					"data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"cGFydGlhbA==\",\"partial_image_index\":0,\"output_format\":\"webp\",\"background\":\"transparent\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000003,\"usage\":{\"input_tokens\":7,\"output_tokens\":10,\"output_tokens_details\":{\"image_tokens\":5}},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"tools\":[{\"type\":\"image_generation\",\"model\":\"gpt-image-2\",\"background\":\"transparent\",\"output_format\":\"webp\",\"quality\":\"high\",\"size\":\"1024x1024\"}],\"output\":[{\"type\":\"image_generation_call\",\"result\":\"ZWRpdGVk\",\"revised_prompt\":\"replace background with aurora\",\"output_format\":\"webp\"}]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc.Requests.Transport = upstream

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 4,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, "edit", gjson.GetBytes(upstream.lastBody, "tools.0.action").String())
	require.Equal(t, "https://example.com/source.png", gjson.GetBytes(upstream.lastBody, "input.0.content.1.image_url").String())
	require.Equal(t, "https://example.com/mask.png", gjson.GetBytes(upstream.lastBody, "tools.0.input_image_mask.image_url").String())
	events := parseOpenAIImageTestSSEEvents(rec.Body.String())
	partial, ok := findOpenAIImageTestSSEEvent(events, "image_edit.partial_image")
	require.True(t, ok)
	require.Equal(t, "image_edit.partial_image", gjson.Get(partial.Data, "type").String())
	require.Equal(t, int64(1710000003), gjson.Get(partial.Data, "created_at").Int())
	require.Equal(t, "cGFydGlhbA==", gjson.Get(partial.Data, "b64_json").String())
	require.Equal(t, "data:image/webp;base64,cGFydGlhbA==", gjson.Get(partial.Data, "url").String())
	require.Equal(t, "gpt-image-2", gjson.Get(partial.Data, "model").String())
	require.Equal(t, "webp", gjson.Get(partial.Data, "output_format").String())
	require.Equal(t, "high", gjson.Get(partial.Data, "quality").String())
	require.Equal(t, "1024x1024", gjson.Get(partial.Data, "size").String())
	require.Equal(t, "transparent", gjson.Get(partial.Data, "background").String())

	completed, ok := findOpenAIImageTestSSEEvent(events, "image_edit.completed")
	require.True(t, ok)
	require.Equal(t, "image_edit.completed", gjson.Get(completed.Data, "type").String())
	require.Equal(t, int64(1710000003), gjson.Get(completed.Data, "created_at").Int())
	require.Equal(t, "ZWRpdGVk", gjson.Get(completed.Data, "b64_json").String())
	require.Equal(t, "data:image/webp;base64,ZWRpdGVk", gjson.Get(completed.Data, "url").String())
	require.Equal(t, "gpt-image-2", gjson.Get(completed.Data, "model").String())
	require.Equal(t, "webp", gjson.Get(completed.Data, "output_format").String())
	require.Equal(t, "high", gjson.Get(completed.Data, "quality").String())
	require.Equal(t, "1024x1024", gjson.Get(completed.Data, "size").String())
	require.Equal(t, "transparent", gjson.Get(completed.Data, "background").String())
	require.JSONEq(t, `{"images":1}`, gjson.Get(completed.Data, "usage").Raw)
	require.False(t, gjson.Get(completed.Data, "revised_prompt").Exists())
}

// TestOpenAIGatewayServiceForwardImages_OAuthStreamingHandlesOutputItemDoneFallback
// observes an image item before a final response with empty output.
// Conversion retains one image completion with final timestamp and tool
// count, producing no error event.
func TestOpenAIGatewayServiceForwardImages_OAuthStreamingHandlesOutputItemDoneFallback(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true,"response_format":"url"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	upstream := &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream_output_item_done"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_123\",\"type\":\"image_generation_call\",\"result\":\"ZmluYWw=\",\"revised_prompt\":\"draw a cat\",\"output_format\":\"png\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000005,\"usage\":{\"input_tokens\":5,\"output_tokens\":9,\"output_tokens_details\":{\"image_tokens\":4}},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc.Requests.Transport = upstream

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 5,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	events := parseOpenAIImageTestSSEEvents(rec.Body.String())
	completed, ok := findOpenAIImageTestSSEEvent(events, "image_generation.completed")
	require.True(t, ok)
	require.Equal(t, "image_generation.completed", gjson.Get(completed.Data, "type").String())
	require.Equal(t, int64(1710000005), gjson.Get(completed.Data, "created_at").Int())
	require.Equal(t, "ZmluYWw=", gjson.Get(completed.Data, "b64_json").String())
	require.Equal(t, "data:image/png;base64,ZmluYWw=", gjson.Get(completed.Data, "url").String())
	require.Equal(t, "gpt-image-2", gjson.Get(completed.Data, "model").String())
	require.JSONEq(t, `{"images":1}`, gjson.Get(completed.Data, "usage").Raw)
	require.NotContains(t, rec.Body.String(), "event: error")
}

// TestOpenAIGatewayServiceForwardImages_OAuthStreamingHandlesMultilineSSE
// splits a completed Responses object across data lines. Conversion joins
// that payload and preserves image data, terminal tool count and retained
// token observations.
func TestOpenAIGatewayServiceForwardImages_OAuthStreamingHandlesMultilineSSE(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true,"response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := newImagesFixture(imagesFixtureInputs{})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	svc.Requests.Transport = &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream_multiline_oauth"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\n" +
					"data: \"response\":{\"created_at\":1710000011,\"usage\":{\"input_tokens\":6,\"output_tokens\":10,\"output_tokens_details\":{\"image_tokens\":5}},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"TXVsdGlsaW5l\",\"output_format\":\"png\"}]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 11,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 6, result.Usage.InputTokens)
	require.Equal(t, 10, result.Usage.OutputTokens)
	require.Equal(t, 5, result.Usage.ImageOutputTokens)
	events := parseOpenAIImageTestSSEEvents(rec.Body.String())
	completed, ok := findOpenAIImageTestSSEEvent(events, "image_generation.completed")
	require.True(t, ok)
	require.Equal(t, "TXVsdGlsaW5l", gjson.Get(completed.Data, "b64_json").String())
	require.JSONEq(t, `{"images":1}`, gjson.Get(completed.Data, "usage").Raw)
	require.NotContains(t, rec.Body.String(), "event: error")
}

// TestOpenAIGatewayServiceForwardImages_OAuthStreamingDrainsAfterClientDisconnect
// rejects later client writes during partial image output. The reader
// continues to final image and usage observations and returns the tested
// complete billing result.
func TestOpenAIGatewayServiceForwardImages_OAuthStreamingDrainsAfterClientDisconnect(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true,"response_format":"url"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Writer = &failingOpenAIImageWriter{ResponseWriter: c.Writer, failAfter: 1}

	svc := newImagesFixture(imagesFixtureInputs{ImageStreamDataIntervalTimeout: 1, ImageStreamKeepaliveInterval: 0})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)

	upstream := &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream_disconnect_oauth"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"cGFydGlhbA==\",\"partial_image_index\":0,\"output_format\":\"png\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000009,\"usage\":{\"input_tokens\":5,\"output_tokens\":9,\"output_tokens_details\":{\"image_tokens\":4}},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"ZmluYWw=\",\"output_format\":\"png\"}]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc.Requests.Transport = upstream

	provider := &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 9,
			Name:     "openai-oauth",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "token-123",
			},
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, provider, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 9, result.Usage.OutputTokens)
	require.Equal(t, 4, result.Usage.ImageOutputTokens)
}

// newOpenAIImagesTestContext creates a fresh generation request with JSON
// content type and a recorder. Its byte reader retains supplied body storage,
// so callers keep that data stable through parsing and execution.
func newOpenAIImagesTestContext(t *testing.T, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	return c, rec
}

// newOpenAIImagesTestService binds image execution to the supplied upstream
// fixture transport.
func newOpenAIImagesTestService(upstream httpclient.UpstreamTransport) *OpenAIImagesExecutor {
	return newImagesFixture(imagesFixtureInputs{transport: upstream})
}

// newOpenAIImagesAPIKeyProvider creates a direct Images provider with test
// credentials and the standard API base. Each call returns a fresh record, so
// fixture mutations remain independent between tests.
func newOpenAIImagesAPIKeyProvider() *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providerimages.Record{
			LoadLocation: time.LoadLocation, ID: 31,
			Name:     "openai-apikey-images",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": "https://api.openai.com/v1",
			},
		},
	}
}

// openAIImagesJSONResponse creates a fresh successful JSON image response
// with fixed request and token observations. Each invocation owns a new
// readable body that native image execution closes after consuming it.
func openAIImagesJSONResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"req_img_ctx"},
		},
		Body: io.NopCloser(strings.NewReader(
			`{"created":1710000000,"data":[{"b64_json":"aGVsbG8="}],"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`,
		)),
	}
}

// TestForwardOpenAIImagesAPIKey_NonStreamDetachesUpstreamContext starts
// direct nonstreaming generation with an already canceled source context. The
// upstream request context remains active and the completed image result
// returns for subsequent completion handling.
func TestForwardOpenAIImagesAPIKey_NonStreamDetachesUpstreamContext(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`)
	c, _ := newOpenAIImagesTestContext(t, body)

	recorder := &auxiliaryHTTPRecorder{resp: openAIImagesJSONResponse()}
	svc := newOpenAIImagesTestService(recorder)

	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	require.False(t, parsed.Stream, "本用例覆盖非流式生图")

	// Cancel the source before forwarding to distinguish detached image
	// execution from ordinary propagation of an already ended client request.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := svc.ForwardImages(ctx, c, newOpenAIImagesAPIKeyProvider(), body, parsed, "")

	require.NoError(t, err, "客户端断开不应把已在出图的上游调用打断成 context canceled")
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount, "图片已产出，必须带回结果供计费")

	require.NotNil(t, recorder.lastReq)
	require.NoError(t, recorder.lastReq.Context().Err(),
		"交给上游的请求 context 必须已脱钩，不随客户端断开取消")
}

// TestForwardOpenAIImagesAPIKey_StreamKeepsDetachedUpstreamContext starts
// streaming generation with an already canceled source context. The captured
// upstream request uses an active detached context; the fixture deliberately
// discards result and error in this assertion.
func TestForwardOpenAIImagesAPIKey_StreamKeepsDetachedUpstreamContext(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true,"response_format":"b64_json"}`)
	c, _ := newOpenAIImagesTestContext(t, body)

	recorder := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"req_img_ctx_stream"},
		},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000000}}\n\n" +
				"data: {\"type\":\"response.image_generation_call.completed\",\"result\":\"aGVsbG8=\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}}\n\n",
		)),
	}}
	svc := newOpenAIImagesTestService(recorder)

	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	require.True(t, parsed.Stream)

	// Cancel the source before forwarding and inspect the captured transport
	// context, independently of this fixture's discarded response observations.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Inspect only the captured transport context here. The returned stream
	// verdict is discarded because this assertion does not exercise complete
	// image result retention.
	_, _ = svc.ForwardImages(ctx, c, newOpenAIImagesAPIKeyProvider(), body, parsed, "")

	require.NotNil(t, recorder.lastReq)
	require.NoError(t, recorder.lastReq.Context().Err(),
		"流式路径原本就脱钩，不能被改回随客户端取消")
}

// TestDetachUpstreamContextSemantics compares unconditional detachment with
// streaming-dependent behavior on canceled contexts. Unconditional and
// streaming modes return active contexts, while nonstreaming conditional
// detachment retains the original cancellation.
func TestDetachUpstreamContextSemantics(t *testing.T) {
	t.Run("detachUpstreamContext_always_detaches", func(t *testing.T) {
		// End the input context before unconditional detachment; release the
		// derived context after checking that execution no longer follows client
		// cancellation.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		detached, release := gatewayprovider.DetachUpstreamContext(ctx)
		defer release()
		require.NoError(t, detached.Err())
	})

	t.Run("detachStreamUpstreamContext_keeps_cancel_when_not_streaming", func(t *testing.T) {
		// End the input context before conditional nonstream detachment, so an
		// accidental unconditional detach would erase the cancellation required by
		// this mode.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		same, release := gatewayprovider.DetachStreamUpstreamContext(ctx, false)
		defer release()
		require.ErrorIs(t, same.Err(), context.Canceled,
			"非流式时该函数原样返回请求 context —— 生图路径不能用它")
	})

	t.Run("detachStreamUpstreamContext_detaches_when_streaming", func(t *testing.T) {
		// End the input context before conditional streaming detachment; release
		// the returned context after confirming that ongoing stream work can
		// continue independently.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		detached, release := gatewayprovider.DetachStreamUpstreamContext(ctx, true)
		defer release()
		require.NoError(t, detached.Err())
	})
}

// TestImageBackfillFinishesObservedAttempts exercises a successful image
// generation followed by its optional CDN download. Completion must not leave
// an unfinished provider attempt after all upstream work and image delivery finish.
func TestImageBackfillFinishesObservedAttempts(t *testing.T) {
	var saved telemetry.RequestRecord
	ctx := context.WithValue(t.Context(), telemetry.RequestID, "local")
	ctx = telemetry.WithRequestCapture(ctx, telemetry.RequestRecord{RequestID: "local", StartedAt: time.Now()}, func(r telemetry.RequestRecord) { saved = r })
	ctx = telemetry.WithRequestProvider(ctx, 7, "")
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","size":"1024x1024"}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("api_key", &apikey.APIKey{ID: 42})
	svc := newImagesFixture(imagesFixtureInputs{})
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	svc.Requests.Transport = &diagnosticHTTPTransport{pool: httpclient.NewUpstreamPool(), responses: []*http.Response{
		{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"generation-id"}}, Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://cdn.example.com/cat.png"}],"usage":{"input_tokens":10,"output_tokens":20}}`))},
		b64BackfillImageResponse(http.StatusOK, "image/png", b64BackfillPNGBytes),
	}}
	target := b64BackfillProvider(true)
	result, err := svc.ForwardImages(ctx, c, target, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	gatewayprovider.CaptureOpenAI(ctx, &gatewayprovider.OpenAICapture{Result: result, Provider: target.View()})
	t.Logf("attempts: %+v", saved.Attempts)
	require.Len(t, saved.Attempts, 1)
	for _, attempt := range saved.Attempts {
		require.NotEqual(t, "running", attempt.Outcome, "completed request contains an unfinished HTTP exchange")
	}
}

// TestBodyFailureFinishesMediaAttempt reads an API-key image response that
// fails after successful headers. The failed exchange must retain its selected
// identifier and a terminal outcome when forwarding returns no completion result.
func TestBodyFailureFinishesMediaAttempt(t *testing.T) {
	var saved telemetry.RequestRecord
	ctx := context.WithValue(t.Context(), telemetry.RequestID, "request")
	ctx = telemetry.WithRequestCapture(ctx, telemetry.RequestRecord{RequestID: "request", StartedAt: time.Now()}, func(r telemetry.RequestRecord) { saved = r })
	ctx = telemetry.WithRequestProvider(ctx, 7, "")
	body := []byte(`{"model":"gpt-image-2","prompt":"test"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	c.Request = c.Request.WithContext(ctx)
	parsed, err := media.ParseImageRequest(c.Request.URL.Path, c.GetHeader("Content-Type"), body, true)
	require.NoError(t, err)
	transport := &diagnosticHTTPTransport{pool: httpclient.NewUpstreamPool(), responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"known"}}, Body: &openAIImagesReadErrorBody{err: io.ErrUnexpectedEOF}}}}
	svc := newOpenAIImagesTestService(transport)
	target := newOpenAIImagesAPIKeyProvider()
	target.Record.ID = 7
	result, err := svc.ForwardImages(ctx, c, target, body, parsed, "")
	require.Error(t, err)
	require.Nil(t, result)
	require.Len(t, saved.Attempts, 1)
	require.Equal(t, "known", saved.Attempts[0].RequestID)
	t.Logf("failure=%v attempt=%+v", err, saved.Attempts[0])
	require.Equal(t, "failed", saved.Attempts[0].Outcome)
}
