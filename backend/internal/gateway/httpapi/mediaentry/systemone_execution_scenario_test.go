package mediaentry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/systemone"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// A SystemOne pool retry stays on one selected provider and holds its lease
// through the successful answer. This scenario joins HTTP classification with
// the media adapter and decision loop, observing two exchanges, one completion
// capture and one final lease release.

// systemOnePoolTransport scripts an initial service error followed by served
// decisions.
type systemOnePoolTransport struct {
	httpclient.UpstreamTransport     // unused transport operations
	calls                        int // serial exchange call count
}

// systemOnePoolRun counts completion and lease release around the real HTTP
// adapter.
type systemOnePoolRun struct {
	*systemOneRun // real decision adapter
	// completed and released are counted by callbacks in the same serial Run.
	// The test inspects them after execution returns to distinguish completion
	// capture from final provider-lease release.
	// completed: captured completion call count; released: provider release call count.
	completed, released int
}

// Do returns HTTP 503 for the first fixture exchange and a valid decision for
// every later call. It ignores request parameters, creates a fresh response
// body and mutates its unsynchronized serial call count.
func (p *systemOnePoolTransport) Do(*http.Request, string, int64, int) (*http.Response, error) {
	p.calls++
	status := http.StatusOK
	body := `{"model":"jev-1.13.0","answers":{"available":{"type":"noul","noul":1}},"usage":{"input_tokens":1,"output_tokens":0}}`
	if p.calls == 1 {
		status = http.StatusServiceUnavailable
		body = `{"error":{"message":"busy"}}`
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

// Select returns the provider already installed in this fixture selection.
// Exclusions and context are ignored because the scenario protects
// same-provider pool retry rather than distinct provider replacement.
func (p *systemOnePoolRun) Select(context.Context, map[int64]struct{}) (provider.ProviderSnapshot, error) {
	return p.selection.Provider.Record.RoutingSnapshot(), nil
}

// Acquire supplies one fixture release callback and reports successful
// admission. The callback increments the serial release counter, letting the
// scenario inspect release after both pool exchanges have finished.
func (p *systemOnePoolRun) Acquire(context.Context) (func(), bool) {
	return func() { p.released++ }, true
}

// Report accepts fixture scheduling feedback while leaving its counters
// unchanged.
func (p *systemOnePoolRun) Report(context.Context, systemone.Outcome) {}

// Complete increments the serial count of captured served decision
// completions.
func (p *systemOnePoolRun) Complete(context.Context, upstream.AttemptResult) { p.completed++ }

// TestSystemOnePoolRetryExecution returns a retryable 503 followed by a valid
// decision with no provider-switch allowance. It checks two exchanges share
// one lease, one completion and the delivered vendor-version answer.
func TestSystemOnePoolRetryExecution(t *testing.T) {
	transport := &systemOnePoolTransport{}
	prices := testkit.ResolverWithCards(t, testkit.Calculator(nil, map[string]*pricing.ModelPricing{"jev-latest": {InputPricePerToken: 4.2e-8}}), nil)
	executor := &gatewayhttp.SystemOneExecutor{Transport: transport, Pricing: &admission.ModelPricing{Resolver: prices}}
	runtime := New(Bindings{CheckFunding: func(context.Context, *apikey.APIKey, *billing.UserSubscription, string, bool) error { return nil }, Platform: PlatformPorts{SystemOne: executor.Forward}})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, gatewayhttp.EndpointSystemOne, nil)
	key := &apikey.APIKey{ID: 1, User: &apikey.User{ID: 1}}
	c.Set(string(keyhttp.ContextKeyAPIKey), key)
	selected := gatewayprovider.NewExecutionProvider(&provider.Record{ID: 1, Platform: provider.PlatformJev, Type: provider.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "test", "pool_mode": true, "pool_mode_retry_count": 2, "pool_mode_retry_status_codes": []any{503}}})
	run := &systemOnePoolRun{systemOneRun: &systemOneRun{h: runtime, c: c, key: key, input: gatewayhttp.AuxiliaryHTTPInput{Model: "jev-latest", Body: []byte(`{"model":"jev-latest","state":"available","questions":{"available":{"type":"noul","instructions":"Available?"}}}`)}, selection: &gatewayprovider.SelectionResult{Provider: selected}}}
	require.NoError(t, systemone.Run(c.Request.Context(), 0, run))
	require.Equal(t, 2, transport.calls)
	require.Equal(t, 1, run.completed)
	require.Equal(t, 1, run.released)
	require.Equal(t, 200, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"model":"jev-1.13.0"`)
}
