package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	openaiprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Canonical image intent is cached per HTTP request, and transformed attempts
// can produce a different decision without replacing that cache. Tests compare
// true, false, unknown, mapped, and concurrent request cases; the benchmark
// measures classification reuse for a multi-attempt large payload.

// openAIImageIntentHintBenchmarkSink retains benchmark classification results
// so the repeated classifier work remains observable to the benchmark harness.
var openAIImageIntentHintBenchmarkSink bool

// TestResolveOpenAIGroupMappedImageIntent maps text and image aliases to
// opposite model categories. It checks the routing model, rewritten body model,
// and image classification all follow the group-mapped target.
func TestResolveOpenAIGroupMappedImageIntent(t *testing.T) {
	tests := []struct {
		name           string // test scenario label
		requestedModel string // client alias model name
		mappedModel    string // effective group model name
		wantIntent     bool   // expected mapped image intent
	}{
		{
			name:           "普通别名映射为生图模型",
			requestedModel: "draw-alias",
			mappedModel:    "gpt-image-1",
			wantIntent:     true,
		},
		{
			name:           "生图别名映射为普通模型",
			requestedModel: "gpt-image-1",
			mappedModel:    "gpt-5.1",
			wantIntent:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"` + tt.requestedModel + `","input":"hello"}`)
			mappedBody, routingModel, imageIntent := GroupMappedImageIntent(
				"/v1/responses",
				tt.requestedModel,
				body,
				routing.GroupMappingResult{Mapped: true, MappedModel: tt.mappedModel},
				capability.PlatformOpenAI,
				openaiprotocol.ReplaceModelInBody,
			)

			require.Equal(t, tt.mappedModel, routingModel)
			require.Equal(t, tt.mappedModel, gjson.GetBytes(mappedBody, "model").String())
			require.Equal(t, tt.wantIntent, imageIntent)
		})
	}
}

// TestSeedOpenAIForwardImageIntentHint seeds positive and negative unmapped
// decisions and leaves mapped input unresolved. It checks known boolean values
// are stored only when the handler can use the current canonical model.
func TestSeedOpenAIForwardImageIntentHint(t *testing.T) {
	tests := []struct {
		name        string // test scenario label
		groupMapped bool   // group model-mapping enabled flag
		imageIntent bool   // incoming canonical image decision
		wantHint    bool   // expected cached boolean presence
	}{
		{name: "seed true", imageIntent: true, wantHint: true},
		{name: "seed false", imageIntent: false, wantHint: true},
		{name: "mapped body stays unknown", groupMapped: true, imageIntent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &gin.Context{}
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

			SeedOpenAIForwardImageIntentHint(c, tt.groupMapped, tt.imageIntent)

			var hintValues []bool
			for _, value := range c.Keys {
				if hint, ok := value.(bool); ok {
					hintValues = append(hintValues, hint)
				}
			}
			if !tt.wantHint {
				require.Empty(t, hintValues)
				return
			}
			require.Equal(t, []bool{tt.imageIntent}, hintValues)
		})
	}
}

// newOpenAIImageIntentHintTestContext creates a Gin context with the chosen
// transport identity.
func newOpenAIImageIntentHintTestContext(transport OpenAIClientTransport) *gin.Context {
	c := &gin.Context{}
	SetOpenAIClientTransport(c, transport)
	return c
}

// countingOpenAIImageIntentClassifier wraps production classification with an
// atomic call counter. The returned closure shares the supplied counter across
// requests and records every invocation before delegating the endpoint, model,
// and payload.
func countingOpenAIImageIntentClassifier(calls *atomic.Int64) ImageIntentClassifier {
	return func(endpoint string, requestedModel string, body []byte) bool {
		calls.Add(1)
		return gatewayprovider.ImageIntent().IsImageGenerationIntent(endpoint, requestedModel, body)
	}
}

// TestResolveOpenAIImageIntentHintCachesTrueAndFalse resolves image and text
// input twice on HTTP contexts. It checks one classifier call per request and a
// known cached decision for both positive and negative results.
func TestResolveOpenAIImageIntentHintCachesTrueAndFalse(t *testing.T) {
	tests := []struct {
		name string // test scenario label
		body []byte // canonical classifier request bytes
		want bool   // expected image-intent boolean decision
	}{
		{name: "true", body: []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation"}]}`), want: true},
		{name: "false is known", body: []byte(`{"model":"gpt-5.4","input":"write code"}`), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
			var calls atomic.Int64
			classify := countingOpenAIImageIntentClassifier(&calls)

			require.Equal(t, tt.want, ResolveOpenAIImageIntentHint(c, "gpt-5.4", tt.body, classify))
			require.Equal(t, tt.want, ResolveOpenAIImageIntentHint(c, "gpt-5.4", tt.body, classify))
			require.Equal(t, int64(1), calls.Load())
			cached, known := GetOpenAIImageIntentHint(c)
			require.True(t, known)
			require.Equal(t, tt.want, cached)
		})
	}
}

// TestResolveOpenAIImageIntentHintUsesHandlerSeed supplies each boolean
// canonical hint before resolution. It checks the seeded result is returned and
// the classifier remains unused even when a body is supplied.
func TestResolveOpenAIImageIntentHintUsesHandlerSeed(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
		SetOpenAIImageIntentHint(c, seeded)
		var calls atomic.Int64

		got := ResolveOpenAIImageIntentHint(c, "gpt-5.4", []byte(`{"model":"gpt-5.4"}`), countingOpenAIImageIntentClassifier(&calls))

		require.Equal(t, seeded, got)
		require.Zero(t, calls.Load())
	}
}

// TestResolveOpenAIPassthroughImageIntentReusesCanonicalAcrossFailover repeats
// an unchanged passthrough attempt three times. It checks all results remain
// negative and the canonical body is classified once for that HTTP request.
func TestResolveOpenAIPassthroughImageIntentReusesCanonicalAcrossFailover(t *testing.T) {
	c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
	body := []byte(`{"model":"gpt-5.4","input":"write code"}`)
	var calls atomic.Int64
	classify := countingOpenAIImageIntentClassifier(&calls)

	for range 3 {
		require.False(t, ResolveOpenAIPassthroughImageIntent(c, "gpt-5.4", body, "gpt-5.4", body, false, classify))
	}
	require.Equal(t, int64(1), calls.Load())
}

// TestResolveOpenAIPassthroughImageIntentKeepsCompactMappingAttemptLocal
// changes text to image and image to text models for Compact attempts. It
// checks the attempt result changes while the cached canonical decision remains
// reusable.
func TestResolveOpenAIPassthroughImageIntentKeepsCompactMappingAttemptLocal(t *testing.T) {
	t.Run("text to image", func(t *testing.T) {
		c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
		body := []byte(`{"model":"draw-alias","input":"draw"}`)
		compactBody := []byte(`{"model":"gpt-image-2","input":"draw"}`)
		var calls atomic.Int64
		classify := countingOpenAIImageIntentClassifier(&calls)

		require.True(t, ResolveOpenAIPassthroughImageIntent(c, "draw-alias", body, "gpt-image-2", compactBody, true, classify))
		cached, known := GetOpenAIImageIntentHint(c)
		require.True(t, known)
		require.False(t, cached)

		require.False(t, ResolveOpenAIPassthroughImageIntent(c, "draw-alias", body, "draw-alias", body, false, classify))
		require.Equal(t, int64(2), calls.Load())
		cached, known = GetOpenAIImageIntentHint(c)
		require.True(t, known)
		require.False(t, cached)
	})

	t.Run("image to text", func(t *testing.T) {
		c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
		body := []byte(`{"model":"gpt-image-2","input":"draw"}`)
		compactBody := []byte(`{"model":"gpt-5.4","input":"draw"}`)
		var calls atomic.Int64
		classify := countingOpenAIImageIntentClassifier(&calls)

		require.False(t, ResolveOpenAIPassthroughImageIntent(c, "gpt-image-2", body, "gpt-5.4", compactBody, true, classify))
		cached, known := GetOpenAIImageIntentHint(c)
		require.True(t, known)
		require.True(t, cached)

		require.True(t, ResolveOpenAIPassthroughImageIntent(c, "gpt-image-2", body, "gpt-image-2", body, false, classify))
		require.Equal(t, int64(2), calls.Load())
	})
}

// TestResolveOpenAIPassthroughImageIntentInvalidationDoesNotPolluteCanonical
// removes an image tool from one attempt. It checks separate canonical and
// attempt classifications, retention of the positive canonical cache, and reuse
// on unchanged input.
func TestResolveOpenAIPassthroughImageIntentInvalidationDoesNotPolluteCanonical(t *testing.T) {
	c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
	canonicalBody := []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation"}]}`)
	strippedBody := []byte(`{"model":"gpt-5.4","tools":[]}`)
	var calls atomic.Int64
	classify := countingOpenAIImageIntentClassifier(&calls)

	require.False(t, ResolveOpenAIPassthroughImageIntent(c, "gpt-5.4", canonicalBody, "gpt-5.4", strippedBody, true, classify))
	require.Equal(t, int64(2), calls.Load(), "unknown canonical and invalidated attempt are classified independently")
	cached, known := GetOpenAIImageIntentHint(c)
	require.True(t, known)
	require.True(t, cached)

	require.True(t, ResolveOpenAIPassthroughImageIntent(c, "gpt-5.4", canonicalBody, "gpt-5.4", canonicalBody, false, classify))
	require.Equal(t, int64(2), calls.Load())
}

// TestResolveOpenAIPassthroughImageIntentMappedBodyStartsUnknownThenSeedsCanonical
// starts without a cached decision and evaluates a rewritten text attempt. It
// checks two classifications and retention of the canonical image-model
// decision.
func TestResolveOpenAIPassthroughImageIntentMappedBodyStartsUnknownThenSeedsCanonical(t *testing.T) {
	c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
	canonicalBody := []byte(`{"model":"gpt-image-2","input":"draw"}`)
	strippedAttemptBody := []byte(`{"model":"gpt-5.4","input":"draw"}`)
	_, known := GetOpenAIImageIntentHint(c)
	require.False(t, known)
	var calls atomic.Int64
	classify := countingOpenAIImageIntentClassifier(&calls)

	require.False(t, ResolveOpenAIPassthroughImageIntent(c, "gpt-image-2", canonicalBody, "gpt-5.4", strippedAttemptBody, true, classify))
	require.Equal(t, int64(2), calls.Load())
	cached, known := GetOpenAIImageIntentHint(c)
	require.True(t, known)
	require.True(t, cached)
}

// TestResolveOpenAIPassthroughImageIntentReusesAcrossInvariantMutations
// supplies attempt bodies with policy and namespace changes marked as
// invariant. It checks repeated resolutions reuse one canonical negative
// decision for each request.
func TestResolveOpenAIPassthroughImageIntentReusesAcrossInvariantMutations(t *testing.T) {
	tests := []struct {
		name          string // test scenario label
		canonicalBody []byte // unchanged canonical request bytes
		attemptBody   []byte // transformed per-attempt request bytes
		want          bool   // expected image-intent boolean decision
	}{
		{
			name:          "oauth sanitize fast policy and reasoning",
			canonicalBody: []byte(`{"model":"gpt-5.4","input":[{"type":"input_image","image_url":"data:image/png;base64,"}],"service_tier":"fast","reasoning":{"effort":"minimal"}}`),
			attemptBody:   []byte(`{"model":"gpt-5.4","input":[],"service_tier":"priority","reasoning":{"effort":"none"},"store":false,"stream":true}`),
			want:          false,
		},
		{
			name:          "namespace flatten",
			canonicalBody: []byte(`{"model":"gpt-5.4","tools":[{"type":"namespace","name":"code_tools"}]}`),
			attemptBody:   []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"code_tools.run"}]}`),
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
			var calls atomic.Int64
			classify := countingOpenAIImageIntentClassifier(&calls)

			require.Equal(t, tt.want, ResolveOpenAIPassthroughImageIntent(c, "gpt-5.4", tt.canonicalBody, "gpt-5.4", tt.attemptBody, false, classify))
			require.Equal(t, tt.want, ResolveOpenAIPassthroughImageIntent(c, "gpt-5.4", tt.canonicalBody, "gpt-5.4", tt.attemptBody, false, classify))
			require.Equal(t, int64(1), calls.Load())
		})
	}
}

// TestResolveOpenAIImageIntentHintExcludesWebSocketAndUnknownTransport resolves
// unchanged input on transports without HTTP caching. It checks two classifier
// calls and an unknown stored hint after both resolutions.
func TestResolveOpenAIImageIntentHintExcludesWebSocketAndUnknownTransport(t *testing.T) {
	for _, transport := range []OpenAIClientTransport{OpenAIClientTransportWS, OpenAIClientTransportUnknown} {
		c := newOpenAIImageIntentHintTestContext(transport)
		var calls atomic.Int64
		classify := countingOpenAIImageIntentClassifier(&calls)
		body := []byte(`{"model":"gpt-5.4","input":"write code"}`)

		require.False(t, ResolveOpenAIImageIntentHint(c, "gpt-5.4", body, classify))
		require.False(t, ResolveOpenAIImageIntentHint(c, "gpt-5.4", body, classify))
		require.Equal(t, int64(2), calls.Load())
		_, known := GetOpenAIImageIntentHint(c)
		require.False(t, known)
	}
}

// TestResolveOpenAIImageIntentHintConcurrentRequestsAreIsolated runs separate
// HTTP contexts concurrently with alternating image intent. It checks each
// request's repeated result agrees with its input and the shared classifier
// count equals requests.
func TestResolveOpenAIImageIntentHintConcurrentRequestsAreIsolated(t *testing.T) {
	const requests = 32
	var calls atomic.Int64
	classify := countingOpenAIImageIntentClassifier(&calls)
	var wg sync.WaitGroup
	results := make([][2]bool, requests)

	// Each goroutine owns a separate Gin context and result index. The
	// shared atomic counter measures classifier calls while Wait protects
	// subsequent result assertions.
	for i := range requests {
		wg.Add(1)
		go func(index int, image bool) {
			defer wg.Done()
			c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
			body := []byte(`{"model":"gpt-5.4","input":"write code"}`)
			if image {
				body = []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation"}]}`)
			}
			results[index][0] = ResolveOpenAIImageIntentHint(c, "gpt-5.4", body, classify)
			results[index][1] = ResolveOpenAIImageIntentHint(c, "gpt-5.4", body, classify)
		}(i, i%2 == 0)
	}
	wg.Wait()
	for i, result := range results {
		require.Equal(t, i%2 == 0, result[0])
		require.Equal(t, result[0], result[1])
	}
	require.Equal(t, int64(requests), calls.Load())
}

// BenchmarkOpenAIPassthroughImageIntentHintLargeBody compares rescanning with
// cached canonical classification over four attempts on a large payload. It
// reports allocations and classifier calls per operation while retaining each
// result.
func BenchmarkOpenAIPassthroughImageIntentHintLargeBody(b *testing.B) {
	// Four MiB of text makes repeated payload scans measurable.
	body := []byte(`{"model":"gpt-5.4","input":"` + strings.Repeat("x", 4<<20) + `"}`)
	// Four attempts expose per-request classification reuse in the
	// benchmark.
	const attempts = 4

	b.Run("scan_each_attempt", func(b *testing.B) {
		c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
		b.ReportAllocs()
		calls := 0
		for range b.N {
			// Reset the hint to an unknown value for each benchmark
			// operation, so reuse is measured across its attempts
			// rather than across separate requests.
			c.Set(openAIImageIntentHintContextKey, struct{}{})
			for range attempts {
				calls++
				openAIImageIntentHintBenchmarkSink = gatewayprovider.ImageIntent().IsImageGenerationIntent(media.OpenAIResponsesEndpoint, "gpt-5.4", body)
			}
		}
		b.ReportMetric(float64(calls)/float64(b.N), "classifier_calls/op")
	})

	b.Run("request_scoped_hint", func(b *testing.B) {
		c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
		b.ReportAllocs()
		calls := 0
		classify := func(endpoint string, requestedModel string, candidate []byte) bool {
			calls++
			return gatewayprovider.ImageIntent().IsImageGenerationIntent(endpoint, requestedModel, candidate)
		}
		for range b.N {
			// Reset the hint to an unknown value for each benchmark
			// operation, so reuse is measured across its attempts
			// rather than across separate requests.
			c.Set(openAIImageIntentHintContextKey, struct{}{})
			for range attempts {
				openAIImageIntentHintBenchmarkSink = ResolveOpenAIPassthroughImageIntent(c, "gpt-5.4", body, "gpt-5.4", body, false, classify)
			}
		}
		b.ReportMetric(float64(calls)/float64(b.N), "classifier_calls/op")
	})
}

// TestOpenAIGatewayServicePassthroughCompactImageIntentIsAttemptLocal forwards
// Compact requests with mappings between text and image models. It checks image
// attempt rejection or upstream forwarding follows the mapped model while
// canonical cache values remain tied to original input.
func TestOpenAIGatewayServicePassthroughCompactImageIntentIsAttemptLocal(t *testing.T) {
	tests := []struct {
		name           string // test scenario label
		canonicalModel string // canonical request model name
		compactModel   string // mapped Compact attempt model
		wantRejected   bool   // expected local image rejection
		wantCanonical  bool   // expected cached canonical decision
	}{
		{
			name:           "text to image rejects",
			canonicalModel: "gpt-5.4",
			compactModel:   "gpt-image-2",
			wantRejected:   true,
		},
		{
			name:           "image to text reaches upstream",
			canonicalModel: "gpt-image-2",
			compactModel:   "gpt-5.4",
			wantCanonical:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"resp_compact","model":"` + tt.compactModel + `","usage":{"input_tokens":1,"output_tokens":1}}`)),
			}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			c, recorder := newOpenAIImageGenerationControlTestContext(false, "unit-test-agent/1.0")
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses/compact", nil)
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			provider := newOpenAIImageGenerationControlTestProvider()
			provider.Record.Extra = map[string]any{"openai_passthrough": true}
			provider.Record.Credentials = map[string]any{
				"api_key": "sk-test",
				"compact_model_mapping": map[string]any{
					tt.canonicalModel: tt.compactModel,
				},
			}
			body := []byte(`{"model":"` + tt.canonicalModel + `","stream":false,"input":"draw"}`)

			result, err := svc.Forward(context.Background(), c, provider, body)

			cached, known := GetOpenAIImageIntentHint(c)
			require.True(t, known)
			require.Equal(t, tt.wantCanonical, cached)
			if tt.wantRejected {
				require.Error(t, err)
				require.Nil(t, result)
				require.Equal(t, http.StatusForbidden, recorder.Code)
				require.Nil(t, upstream.lastReq)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, tt.compactModel, gjson.GetBytes(upstream.lastBody, "model").String())
		})
	}
}
