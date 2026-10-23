package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// imageProbeSink retains test events from a synchronous native Images probe.
type imageProbeSink struct {
	events []providercore.TestEvent // emitted probe output events
}

// Begin accepts the probe's metadata commitment for this in-memory fixture.
func (*imageProbeSink) Begin(context.Context, bool) error { return nil }

// Emit records each probe event for completion and image assertions.
func (s *imageProbeSink) Emit(_ context.Context, event providercore.TestEvent) error {
	s.events = append(s.events, event)
	return nil
}

// TestExecuteImageAPIKeyStreamOnly probes a provider that accepts only streams.
// Both successful aggregation and incomplete-stream failure must submit exactly
// one generation, with the automatic preview request disabling partial images.
// An unsolicited expired preview cannot trigger a download or abort the probe.
func TestExecuteImageAPIKeyStreamOnly(t *testing.T) {
	for _, failed := range []bool{false, true} {
		body := "data: {\"type\":\"image_generation.partial_image\",\"url\":\"https://preview.invalid/expired\"}\n\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"aW1hZ2U=\"}\n\n"
		if failed {
			body += "data: {"
		}
		transport := &codexInviteResetHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}}}
		probe := &OpenAIProviderTest{
			Transport:    transport,
			ValidateURL:  func(raw string) (string, error) { return raw, nil },
			ApplyRouting: func(*TestRun, *providercore.Record, *http.Request, bool) {},
			ResolveTLS:   func(*TestRun, *providercore.Record) *tlsfingerprint.Profile { return nil },
		}
		sink := &imageProbeSink{}
		run := NewTestRun(t.Context(), nil, sink)
		value := &providercore.Record{Platform: providercore.PlatformOpenAI, Type: providercore.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "fixture", providercore.ImagesGenerationsModeKey: "streaming"}}
		err := probe.ExecuteImageAPIKey(run, run.Context, value, "gpt-image-2", "draw")
		run.Cancel()
		require.Equal(t, failed, err != nil)
		require.Len(t, transport.requests, 1)
		require.True(t, gjson.Get(transport.bodies[0], "stream").Bool())
		require.EqualValues(t, 0, gjson.Get(transport.bodies[0], "partial_images").Int())
		last := sink.events[len(sink.events)-1]
		if failed {
			require.Equal(t, "error", last.Type)
		} else {
			require.Equal(t, "test_complete", last.Type)
		}
	}
}
