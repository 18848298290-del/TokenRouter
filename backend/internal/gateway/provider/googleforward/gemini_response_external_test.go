package googleforward_test

import (
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/googleforward"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

var structuredLogCaptureMu sync.Mutex

type inMemoryLogSink struct {
	mu     sync.Mutex
	events []*logging.LogEvent
}

// TestHandleNativeNonStreamingResponse_FeedsImageCounter checks that native generateContent
// response bodies reach image observation so returned images are available for billing.
func TestHandleNativeNonStreamingResponse_FeedsImageCounter(t *testing.T) {
	c := newGeminiImageTestContext(t)
	c.Images = 0

	body := geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiTestPNG + `"}}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,

		Header: http.Header{"Content-Type": []string{"application/json"}},

		Body: io.NopCloser(strings.NewReader(body)),
	}

	svc := newGeminiFixture(geminiDependencies{})
	usage, err := googleforward.GeminiResponseForTest(svc, c).HandleNativeNonStreamingResponse(upstream.NewOutputContext(c.Sink()), resp, false)
	require.NoError(t, err)
	require.NotNil(t, usage)

	require.Equal(t, 1, c.Images)
	require.Equal(t, 1, c.ImageCountForTest("nana-banana-2", "nana-banana-2"))
}

func TestGeminiHandleNativeNonStreamingResponse_DebugDisabledDoesNotEmitHeaderLogs(t *testing.T) {
	logSink, restore := captureStructuredLog(t)
	defer restore()

	svc := newGeminiFixture(geminiDependencies{
		cfg: &googleforward.Options{DebugHeaders: false},
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,

		Header: http.Header{
			"Content-Type":      []string{"application/json"},
			"X-RateLimit-Limit": []string{"60"},
		},

		Body: io.NopCloser(strings.NewReader(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2}}`)),
	}

	usage, err := googleforward.GeminiResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, false)}).HandleNativeNonStreamingResponse(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, false).Sink()), resp, false)
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.NotEmpty(t, w.Body.Bytes())
	require.False(t, logSink.ContainsMessage("[GeminiAPI]"), "debug 关闭时不应输出 Gemini 响应头日志")
}

func (s *inMemoryLogSink) WriteLogEvent(event *logging.LogEvent) {
	if event == nil {
		return
	}
	cloned := *event
	if event.Fields != nil {
		cloned.Fields = make(map[string]any, len(event.Fields))
		maps.Copy(cloned.Fields, event.Fields)
	}
	s.mu.Lock()
	s.events = append(s.events, &cloned)
	s.mu.Unlock()
}

func (s *inMemoryLogSink) ContainsMessage(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.events {
		if ev != nil && strings.Contains(ev.Message, substr) {
			return true
		}
	}
	return false
}

func captureStructuredLog(t *testing.T) (*inMemoryLogSink, func()) {
	t.Helper()
	structuredLogCaptureMu.Lock()

	err := logging.Init(logging.InitOptions{
		Level: "debug",

		Format: "json",

		ServiceName: "tokenrouter",

		Environment: "test",

		Output: logging.OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},

		Sampling: logging.SamplingOptions{Enabled: false},
	})
	require.NoError(t, err)

	sink := &inMemoryLogSink{}
	logging.SetSink(sink)
	return sink, func() {
		logging.SetSink(nil)
		structuredLogCaptureMu.Unlock()
	}
}
