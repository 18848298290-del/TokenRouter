package searchtools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/search"
	"github.com/TokenFlux/TokenRouter/internal/search/contract"
)

// searchStub records synchronous query requests and returns one fixed search
// result plus its configured error.
type searchStub struct {
	calls   int                    // recorded policy or search invocation count
	request contract.SearchRequest // retained most recent search request
	err     error                  // configured search operation error
	// events retains the caller's ordering sink. Search appends through that
	// shared slice pointer so the acceptance callback and this stub record one sequence.
	events *[]string // optional search ordering sink
}

// sourceStub returns its supplied searcher as the current manager binding.
type sourceStub struct {
	searcher Searcher // retained current manager fixture
}

// settingStub records global-gate reads and returns the configured bool.
type settingStub struct {
	enabled bool // configured global-gate bool
	calls   int  // global-gate read invocation count
}

// pricingConfigStub counts group feature reads and returns the configured
// choice.
type pricingConfigStub struct {
	calls   int  // group-feature read invocation count
	enabled bool // configured group-feature bool
}

// outputStub records synthetic writes with copied byte payloads and
// configurable event failure.
type outputStub struct {
	events []string // attempted synthetic event names
	bodies [][]byte // copied attempted event payloads
	json   []byte   // copied synthetic JSON payload
	// failAt matches the one-based attempted event count after it is recorded.
	// Zero disables failure injection, and the failing payload remains available
	// for assertions even though WriteEvent returns an error.
	failAt  int  // fail after recording this event count
	flushes int  // recorded output flush invocation count
	started bool // synthetic stream start observed
}

// SearchWithBestProvider records the supplied request and optional ordering
// event, then returns its fixed result/vendor plus configured error. Tests
// call it synchronously and inspect retained request fields after execution.
func (s *searchStub) SearchWithBestProvider(_ context.Context, r contract.SearchRequest) (*contract.SearchResponse, string, error) {
	s.calls++
	s.request = r
	if s.events != nil {
		*s.events = append(*s.events, "search")
	}
	return &contract.SearchResponse{Query: r.Query, Results: []contract.SearchResult{{URL: "https://example.test/page", Title: "title", Snippet: "snippet"}}}, "brave", s.err
}

// Current returns the configured searcher fixture for manager availability
// checks.
func (s sourceStub) Current() Searcher { return s.searcher }

// IsWebSearchEmulationEnabled counts global-gate reads and returns the
// supplied bool.
func (s *settingStub) IsWebSearchEmulationEnabled(context.Context) bool { s.calls++; return s.enabled }

// Enabled counts group-policy reads and returns its configured bool without an
// error.
func (s *pricingConfigStub) Enabled(context.Context, int64, string) (bool, error) {
	s.calls++
	return s.enabled, nil
}

// StartStream records synthetic streaming start without opening a network
// response.
func (o *outputStub) StartStream() { o.started = true }

// WriteEvent records its name and a copied payload before applying failAt. The
// configured matching event returns a fixture error, so recorded data
// represents attempted output rather than demonstrated write success.
func (o *outputStub) WriteEvent(name string, body []byte) error {
	o.events = append(o.events, name)
	o.bodies = append(o.bodies, append([]byte(nil), body...))
	if len(o.events) == o.failAt {
		return errors.New("write failed")
	}
	return nil
}

// WriteJSON copies the supplied payload for later synthetic-response
// assertions.
func (o *outputStub) WriteJSON(body []byte) { o.json = append([]byte(nil), body...) }

// Flush counts the output-buffer emission request made by the emulator.
func (o *outputStub) Flush() { o.flushes++ }

// newTestEmulator builds active global policy around the supplied search stub
// and a fixed sufficiently long ID. Callers can execute JSON or streaming
// output without relying on random identities in their assertions.
func newTestEmulator(provider *searchStub) *Emulator {
	return NewEmulator(sourceStub{provider}, &settingStub{enabled: true}, nil, time.Now, func() string { return "12345678-1234-1234-1234-123456789abc" }, nil)
}

// TestEmulatorSyntheticUsageAndEventOrder executes JSON and streaming search
// with a serial-acceptance callback. The query/options, callback order,
// event/block shape and positive wire estimate must coexist with zero
// structured token usage.
func TestEmulatorSyntheticUsageAndEventOrder(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			order := []string{}
			provider := &searchStub{events: &order}
			core := newTestEmulator(provider)
			out := &outputStub{}
			result, err := core.Execute(context.Background(), Request{Body: []byte(`{"messages":[{"role":"user","content":"query"}]}`), ProxyURL: "http://proxy.test", Stream: stream, OnAccepted: func() { order = append(order, "accepted") }}, out)
			require.NoError(t, err)
			require.Equal(t, []string{"accepted", "search"}, order)
			require.Equal(t, 1, provider.calls)
			require.Equal(t, contract.SearchRequest{Query: "query", MaxResults: 5, ProxyURL: "http://proxy.test"}, provider.request)
			require.Equal(t, "claude-sonnet-4-6", result.Model)
			require.Zero(t, result.Usage.InputTokens)
			require.Zero(t, result.Usage.OutputTokens)
			if stream {
				require.True(t, out.started)
				require.Equal(t, 1, out.flushes)
				require.Equal(t, []string{"message_start", "content_block_start", "content_block_stop", "content_block_start", "content_block_stop", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}, out.events)
				var event struct {
					// Usage captures synthetic wire estimates for this output fixture.
					// The separately returned Result keeps its measured TokenUsage at zero.
					Usage struct {
						Output int `json:"output_tokens"` // synthetic wire output tokens
					} `json:"usage"`
				}
				require.NoError(t, json.Unmarshal(out.bodies[8], &event))
				require.Greater(t, event.Usage.Output, 0)
			} else {
				var msg struct {
					Content []json.RawMessage `json:"content"` // synthetic message content blocks
					// Usage captures the JSON response's display estimate independently of
					// the returned Result, whose structured token measurement remains zero.
					Usage struct {
						Output int `json:"output_tokens"` // synthetic wire output tokens
					} `json:"usage"`
				}
				require.NoError(t, json.Unmarshal(out.json, &msg))
				require.Len(t, msg.Content, 3)
				require.Greater(t, msg.Usage.Output, 0)
			}
		})
	}
}

// TestEmulatorWriteFailureStopsEventsWithoutInventingBillableUsage fails the
// third attempted event after recording it. Output must stop there and flush
// once while returning a result with zero measured token usage.
func TestEmulatorWriteFailureStopsEventsWithoutInventingBillableUsage(t *testing.T) {
	provider := &searchStub{}
	out := &outputStub{failAt: 3}
	result, err := newTestEmulator(provider).Execute(context.Background(), Request{Body: []byte(`{"messages":[{"role":"user","content":"query"}]}`), Stream: true}, out)
	require.NoError(t, err)
	require.Len(t, out.events, 3)
	require.Equal(t, 1, out.flushes)
	require.Zero(t, result.Usage.InputTokens)
	require.Zero(t, result.Usage.OutputTokens)
}

// TestEmulatorProxyFailureRetainsCause returns the configured
// proxy-unavailable search error. Execution must retain its cause in
// ProxyFailure, produce no synthetic result and leave event/JSON output empty.
func TestEmulatorProxyFailureRetainsCause(t *testing.T) {
	provider := &searchStub{err: search.ErrProxyUnavailable}
	out := &outputStub{}
	result, err := newTestEmulator(provider).Execute(context.Background(), Request{Body: []byte(`{"messages":[{"role":"user","content":"query"}]}`)}, out)
	require.Nil(t, result)
	require.ErrorIs(t, err, search.ErrProxyUnavailable)
	var failure *ProxyFailure
	require.ErrorAs(t, err, &failure)
	require.Empty(t, out.events)
	require.Empty(t, out.json)
}

// TestEmulatorPolicyShortCircuit changes manager availability, tool shape and
// exact provider mode around enabled policies. Earlier failed gates must
// prevent later reads, and enabled/disabled overrides must bypass group lookup
// after prerequisites.
func TestEmulatorPolicyShortCircuit(t *testing.T) {
	settings := &settingStub{enabled: true}
	pricingConfig := &pricingConfigStub{enabled: true}
	id := int64(1)
	core := NewEmulator(sourceStub{}, settings, pricingConfig, nil, nil, nil)
	input := PolicyInput{Body: []byte(`{"tools":[{"type":"web_search"}]}`), GroupID: &id}
	require.False(t, core.ShouldEmulate(context.Background(), input))
	require.Zero(t, settings.calls)
	core.source = sourceStub{&searchStub{}}
	input.Body = []byte(`{"tools":[]}`)
	require.False(t, core.ShouldEmulate(context.Background(), input))
	require.Zero(t, settings.calls)
	input.Body = []byte(`{"tools":[{"type":"web_search"}]}`)
	input.Mode = "enabled"
	require.True(t, core.ShouldEmulate(context.Background(), input))
	require.Zero(t, pricingConfig.calls)
	input.Mode = "disabled"
	require.False(t, core.ShouldEmulate(context.Background(), input))
	require.Zero(t, pricingConfig.calls)
	input.Mode = "default"
	require.True(t, core.ShouldEmulate(context.Background(), input))
	require.Equal(t, 1, pricingConfig.calls)
}
