package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Error response selection falls back to caller defaults when no matcher is
// bound and otherwise applies the matching rule's request observation flag.
// These cases inspect the returned tuple and the presence and boolean value
// of skip-monitoring state after a matching error.

// TestApplyErrorPassthroughRule_NoBoundService supplies upstream error bytes with
// no request matcher. The returned status, type, and message must remain
// the provided defaults, with a false match flag despite the upstream payload.
func TestApplyErrorPassthroughRule_NoBoundService(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	status, errType, errMsg, matched := ApplyErrorPassthroughRule(
		c,
		capability.PlatformAnthropic,
		http.StatusUnprocessableEntity,
		[]byte(`{"error":{"message":"invalid schema"}}`),
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	)

	assert.False(t, matched)
	assert.Equal(t, http.StatusBadGateway, status)
	assert.Equal(t, "upstream_error", errType)
	assert.Equal(t, "Upstream request failed", errMsg)
}

// TestApplyErrorPassthroughRule_SkipMonitoringSetsContextKey binds a matching rule
// with observation suppression enabled. The match flag must be true and
// request context must contain a boolean true skip marker for ordinary Ops processing.
func TestApplyErrorPassthroughRule_SkipMonitoringSetsContextKey(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	rule := gatewaytestkit.NonFailoverRule(http.StatusBadRequest, "prompt is too long", http.StatusBadRequest, "上下文超限")
	rule.SkipMonitoring = true

	ruleSvc := gatewaytestkit.ErrorRules([]*errorpolicy.ErrorPassthroughRule{rule})
	BindErrorPassthroughService(c, ruleSvc)

	_, _, _, matched := ApplyErrorPassthroughRule(
		c,
		capability.PlatformAnthropic,
		http.StatusBadRequest,
		[]byte(`{"error":{"message":"prompt is too long"}}`),
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	)

	assert.True(t, matched)
	v, exists := c.Get(OpsSkipPassthroughKey)
	assert.True(t, exists, "OpsSkipPassthroughKey should be set when skip_monitoring=true")
	boolVal, ok := v.(bool)
	assert.True(t, ok, "value should be bool")
	assert.True(t, boolVal)
}

// TestApplyErrorPassthroughRule_NoSkipMonitoringDoesNotSetContextKey matches the
// same error with suppression disabled. Matching must succeed while the
// previously empty context remains without a skip marker, protecting normal observation on that path.
func TestApplyErrorPassthroughRule_NoSkipMonitoringDoesNotSetContextKey(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	rule := gatewaytestkit.NonFailoverRule(http.StatusBadRequest, "prompt is too long", http.StatusBadRequest, "上下文超限")
	rule.SkipMonitoring = false

	ruleSvc := gatewaytestkit.ErrorRules([]*errorpolicy.ErrorPassthroughRule{rule})
	BindErrorPassthroughService(c, ruleSvc)

	_, _, _, matched := ApplyErrorPassthroughRule(
		c,
		capability.PlatformAnthropic,
		http.StatusBadRequest,
		[]byte(`{"error":{"message":"prompt is too long"}}`),
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	)

	assert.True(t, matched)
	_, exists := c.Get(OpsSkipPassthroughKey)
	assert.False(t, exists, "OpsSkipPassthroughKey should NOT be set when skip_monitoring=false")
}
