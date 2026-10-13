package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Count entry fixtures record routing, funding, selection, and forwarding
// events without supplying generation operations. Their assertions distinguish
// the single upstream compatibility path from Grok's local estimator and check
// that failed funding prevents a count target from being selected.

// openAITextCountProbe records single-count adapter callbacks in its parent
// fixture.
type openAITextCountProbe struct {
	parent *tokenEntryProbeForTest // shared ordered-observation fixture
	call   OpenAICountCall         // captured compatibility count inputs
}

// tokenEntryProbeForTest supplies token entry operations with ordered
// observations and funding failures.
type tokenEntryProbeForTest struct {
	OpenAITokenPorts                // unoverridden token backend defaults
	events           []string       // observed entry operation sequence
	key              *apikey.APIKey // configured effective API key
	eligibility      error          // configured funding admission failure
}

// CountExecution records adapter construction and retains the supplied count
// inputs.
func (p *tokenEntryProbeForTest) CountExecution(_ *gin.Context, call OpenAICountCall) textflow.SingleCountPorts {
	p.mark("count-execution")
	return &openAITextCountProbe{parent: p, call: call}
}

// Select records a successful synthetic count selection in the parent fixture.
func (p *openAITextCountProbe) Select() (bool, error) {
	p.parent.mark("count-select")
	return true, nil
}

// Selected records the selection-latency callback in the parent event sequence.
func (p *openAITextCountProbe) Selected() { p.parent.mark("count-latency") }

// SelectionFailed records the terminal selection notification in the parent
// fixture.
func (p *openAITextCountProbe) SelectionFailed(error) { p.parent.mark("count-selection-failed") }

// Forward records the count exchange and returns success to the loop.
func (p *openAITextCountProbe) Forward() error { p.parent.mark("count-forward"); return nil }

// ForwardFailed records an exchange-failure notification in the parent event
// sequence.
func (p *openAITextCountProbe) ForwardFailed(error) { p.parent.mark("count-forward-failed") }

// TestOpenAITextCountTokensHTTP exercises funded, denied, and local Grok count
// entries. It checks routing precedes funding, funding precedes one count
// selection, generation operations remain unused, and local Grok errors retain
// Anthropic framing.
func TestOpenAITextCountTokensHTTP(t *testing.T) {
	t.Run("funds precede single no-slot selection", func(t *testing.T) {
		p, _, c, w, _ := newNativeTokenEntryProbe(t, `{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`)
		newOpenAITokensEntryProbe(p).CountTokens(c)
		require.Equal(t, 200, w.Code)
		assertOpenAITextEventBefore(t, p.events, "plan", "eligibility")
		assertOpenAITextEventBefore(t, p.events, "eligibility", "count-select")
		assertOpenAITextEventBefore(t, p.events, "count-select", "count-latency")
		assertOpenAITextEventBefore(t, p.events, "count-latency", "count-forward")
		require.NotContains(t, p.events, "user-slot")
		require.NotContains(t, p.events, "moderate")
		require.NotContains(t, p.events, "execution")
	})
	t.Run("failed funds check does not select", func(t *testing.T) {
		p, _, c, _, _ := newNativeTokenEntryProbe(t, `{"model":"gpt-5","messages":[]}`)
		p.eligibility = errors.New("unavailable")
		newOpenAITokensEntryProbe(p).CountTokens(c)
		require.NotContains(t, p.events, "count-select")
		require.NotContains(t, p.events, "user-slot")
	})
	t.Run("grok count uses local estimator without funds or selection", func(t *testing.T) {
		p, _, c, w, _ := newNativeTokenEntryProbe(t, `{"model":"grok-4","messages":[{"role":"user","content":"hello"}]}`)
		p.key = nil
		newOpenAITokensEntryProbe(p).GrokCountTokens(c)
		require.Equal(t, 200, w.Code)
		require.Contains(t, w.Body.String(), "input_tokens")
		require.Empty(t, p.events)
	})
	t.Run("grok local empty body error remains Anthropic", func(t *testing.T) {
		p, _, c, w, _ := newNativeTokenEntryProbe(t, "")
		newOpenAITokensEntryProbe(p).GrokCountTokens(c)
		require.Equal(t, 400, w.Code)
		require.Contains(t, w.Body.String(), `"type":"error"`)
	})
}

// newOpenAITokensEntryProbe binds one fixture to token backend and prompt
// operations.
func newOpenAITokensEntryProbe(p *tokenEntryProbeForTest) *OpenAITokensHandler {
	return NewOpenAITokensHandler(OpenAITokenOptions{MaxBodyBytes: 1024 * 1024, MaxSwitches: 2}, p, p)
}

// mark appends one observed operation to the fixture's event sequence.
func (p *tokenEntryProbeForTest) mark(value string) { p.events = append(p.events, value) }

// Access records the authentication read and returns the configured fixture
// key.
func (p *tokenEntryProbeForTest) Access(*gin.Context) (*apikey.APIKey, bool) {
	p.mark("access")
	return p.key, p.key != nil
}

// Plan records route planning and returns a plan for the key's group.
func (p *tokenEntryProbeForTest) Plan(_ context.Context, key *apikey.APIKey, model string) routing.RoutePlan {
	p.mark("plan")
	return routing.Plan(routing.PlanInput{GroupID: key.GroupID, RequestedModel: model})
}

// Eligibility records funding admission and returns the configured fixture
// failure.
func (p *tokenEntryProbeForTest) Eligibility(context.Context, *apikey.APIKey, *billing.UserSubscription) error {
	p.mark("eligibility")
	return p.eligibility
}

// SessionHash supplies a fixed sticky identity for all counting fixture
// requests.
func (p *tokenEntryProbeForTest) SessionHash(*gin.Context, OpenAISessionInput, []byte) string {
	return "session"
}

// MessageProviderModel returns the supplied model for fixture provider-layer
// routing.
func (p *tokenEntryProbeForTest) MessageProviderModel(_ context.Context, _ *apikey.APIKey, model string) string {
	return model
}

// MappedBodyCache records cache construction and returns a reader of the
// supplied body. The fixture reader ignores mapping arguments and retains the
// same byte slice. Callers use immutable fixture input for these ordering
// assertions.
func (p *tokenEntryProbeForTest) MappedBodyCache(body []byte) func(bool, string) []byte {
	p.mark("mapped-cache")
	return func(bool, string) []byte { return body }
}

// ApplyUserPromptReplacementToBody passes fixture input through for token entry
// parsing.
func (p *tokenEntryProbeForTest) ApplyUserPromptReplacementToBody(_ context.Context, body []byte, _ string) []byte {
	return body
}

// newNativeTokenEntryProbe creates HTTP counting input with an authenticated
// user and key. It returns the recorder, handler, and request body for
// assertions. The test context owns those resources throughout the synchronous
// entry call.
func newNativeTokenEntryProbe(t *testing.T, body string) (*tokenEntryProbeForTest, *OpenAITokensHandler, *gin.Context, *httptest.ResponseRecorder, io.ReadCloser) {
	t.Helper()
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	c.Set(authctx.ContextKeyUser, authctx.AuthSubject{UserID: 7, Concurrency: 2})
	p := &tokenEntryProbeForTest{key: &apikey.APIKey{ID: 9, UserID: 7}}
	return p, newOpenAITokensEntryProbe(p), c, writer, c.Request.Body
}
