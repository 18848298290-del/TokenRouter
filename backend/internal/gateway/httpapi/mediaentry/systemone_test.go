package mediaentry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/openaiattempt"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// SystemOne usage warnings retain the served decision and correlate its
// unknown usage with the request attempt. These fixtures also check
// Jev-specific final error rules and verify repeated provider exchanges share
// one initial RPM admission while later funding checks skip that increment.

// systemOneFunds permits fixture funding admission so RPM behavior can be
// observed.
type systemOneFunds struct{}

// systemOneRPM counts serial RPM admissions for one decision request fixture.
type systemOneRPM struct {
	scheduler.UserRPMCache     // unused RPM cache operations
	calls                  int // serial RPM admission count
}

// TestSystemOneFailoverRules exhausts a Jev request under Jev, unrelated and
// unrestricted response rules. It checks applicable rewrite/monitoring
// behavior and preserves Retry-After for every platform fixture.
func TestSystemOneFailoverRules(t *testing.T) {
	for _, platform := range []string{"jev", "openai", ""} {
		t.Run(platform, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, gatewayhttp.EndpointSystemOne, nil)
			rule := gatewaytestkit.NonFailoverRule(503, "busy", 409, "Configured platform error")
			if platform != "" {
				rule.Platforms = []string{platform}
			}
			rule.SkipMonitoring = true
			rules := gatewaytestkit.ErrorRules([]*errorpolicy.ErrorPassthroughRule{rule})
			defer rules.Stop()
			runtime := New(Bindings{Common: openaiattempt.Bindings{Support: &openaiattempt.Support{Rules: rules}}})
			(mediaHTTPAdapter{runtime}).BindErrors(c)
			run := &systemOneRun{h: runtime, c: c}
			run.End(&forward.UpstreamFailoverError{StatusCode: 503, ResponseBody: []byte(`{"error":{"message":"busy"}}`), ResponseHeaders: http.Header{"Retry-After": []string{"17"}}})
			require.Equal(t, "17", recorder.Header().Get("Retry-After"))
			if platform == "openai" {
				require.Equal(t, 502, recorder.Code)
				require.NotContains(t, recorder.Body.String(), "Configured platform error")
				require.False(t, c.GetBool(gatewayhttp.OpsSkipPassthroughKey))
			} else {
				require.Equal(t, 409, recorder.Code)
				require.Contains(t, recorder.Body.String(), "Configured platform error")
				require.True(t, c.GetBool(gatewayhttp.OpsSkipPassthroughKey))
			}
		})
	}
}

// TestSystemOneMissingUsageRetainsSuccessfulRequest adds invalid-usage
// observation after a successful answer was written. It checks the request
// remains completed with HTTP 200 and gains one correlated usage_unknown
// attempt and usage_invalid Ops event.
func TestSystemOneMissingUsageRetainsSuccessfulRequest(t *testing.T) {
	var saved telemetry.RequestRecord
	ctx := telemetry.WithRequestCapture(context.Background(), telemetry.RequestRecord{RequestID: "local", State: "completed", Status: 200}, func(record telemetry.RequestRecord) { saved = record })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, gatewayhttp.EndpointSystemOne, nil).WithContext(ctx)
	c.Data(200, "application/json", []byte(`{"answers":{"available":{"type":"noul","noul":1}}}`))
	run := &systemOneRun{c: c, log: zap.NewNop(), selection: &gatewayprovider.SelectionResult{Provider: gatewayprovider.NewExecutionProvider(&provider.Record{ID: 42, Platform: provider.PlatformJev, Type: provider.ProviderTypeAPIKey})}}
	run.MissingUsage(ctx, upstream.AttemptResult{Served: true, RequestID: "upstream"})
	require.Equal(t, 200, c.Writer.Status())
	require.Equal(t, "completed", saved.State)
	require.Len(t, saved.Attempts, 1)
	require.Equal(t, "usage_unknown", saved.Attempts[0].Outcome)
	require.Equal(t, "upstream", saved.Attempts[0].RequestID)
	events, present := c.Get(gatewayhttp.OpsUpstreamErrorsKey)
	require.True(t, present)
	typed, ok := events.([]*ops.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, typed, 1)
	require.Equal(t, "usage_invalid", typed[0].Kind)
}

// Check permits fixture funding input and returns a successful admission
// result.
func (systemOneFunds) Check(context.Context, billing.CheckInput) error { return nil }

// IncrementUserRPM increments and returns the serial fixture admission count.
// It ignores user/context inputs and reports no cache error, allowing the test
// to distinguish initial counting from later skipped checks.
func (r *systemOneRPM) IncrementUserRPM(context.Context, int64) (int, error) {
	r.calls++
	return r.calls, nil
}

// TestSystemOneConsumesOneRPM performs initial funding admission followed by
// two selected-provider exchanges. It checks both forwards succeed while the
// RPM cache increments once because repeated funding checks skip counting.
func TestSystemOneConsumesOneRPM(t *testing.T) {
	cache := &systemOneRPM{}
	checker := admission.NewFundingAdmission(systemOneFunds{}, scheduler.NewRPMAdmission(cache, nil, scheduler.Diagnostics{}))
	called := 0
	runtime := New(Bindings{CheckFunding: checker.CheckKey, Platform: PlatformPorts{SystemOne: func(context.Context, *gin.Context, *gatewayprovider.ExecutionProvider, []byte) (upstream.AttemptResult, error) {
		called++
		return upstream.AttemptResult{Served: true, HasUsage: true}, nil
	}}})
	key := &apikey.APIKey{ID: 1, User: &apikey.User{ID: 1, RPMLimit: 1}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/systemone", nil)
	c.Set(string(keyhttp.ContextKeyAPIKey), key)
	require.Nil(t, (mediaHTTPAdapter{runtime}).Billing(c))
	run := &systemOneRun{h: runtime, c: c, key: key, selection: &gatewayprovider.SelectionResult{Provider: gatewayprovider.NewExecutionProvider(&provider.Record{ID: 1, Platform: provider.PlatformJev, Type: provider.ProviderTypeAPIKey})}}
	for range 2 {
		result := run.Forward(c.Request.Context())
		require.NoError(t, result.Err)
	}
	require.Equal(t, 1, cache.calls)
	require.Equal(t, 2, called)
}
