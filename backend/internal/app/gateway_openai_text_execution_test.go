package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Reasoning limits apply to the Anthropic provider through Messages, Responses,
// and Chat entry methods. Each test request asks for max effort above its
// group's high limit and checks both the HTTP denial and the retained
// requested-effort observation.

// TestAnthropicReasoningPolicy_AllEntrypointsDeny sends excessive effort
// through all three client entry methods. Each path must return Forbidden while
// preserving the requested max effort in request state for observation.
func TestAnthropicReasoningPolicy_AllEntrypointsDeny(t *testing.T) {
	executor := &gatewayhttp.UnifiedTextExecutor{}
	target := gatewayadapter.NewExecutionProvider(&provider.Record{ID: 1, Platform: "anthropic", Type: "apikey"})
	for _, tc := range []struct {
		// path and field encode the same excessive effort in each protocol, while
		// handle calls its matching method on the shared selected-provider executor.
		path, field string             // path: client endpoint path; field: protocol effort member
		handle      func(*gin.Context) // selected protocol execution
	}{
		{"/v1/messages", `"output_config":{"effort":"max"}`, func(c *gin.Context) {
			_, _ = executor.Messages(c.Request.Context(), c, target, []byte(c.GetString("body")), "", "")
		}},
		{"/v1/responses", `"reasoning":{"effort":"max"}`, func(c *gin.Context) {
			_, _ = executor.Responses(c.Request.Context(), c, target, []byte(c.GetString("body")))
		}},
		{"/v1/chat/completions", `"reasoning_effort":"max"`, func(c *gin.Context) {
			_, _ = executor.Chat(c.Request.Context(), c, target, []byte(c.GetString("body")), "", "")
		}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			body := `{"model":"claude-fable-5-1","messages":[{"role":"user","content":"hi"}],"input":"hi","max_tokens":100,` + tc.field + `}`
			c.Set("body", body)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
			c.Set(string(keyhttp.ContextKeyAPIKey), &apikey.APIKey{Group: &routing.Group{
				MaxReasoningEffort: "high", MaxReasoningEffortOverLimit: "deny",
			}})
			key, _ := keyhttp.GetAPIKeyFromContext(c)
			c.Set("gateway_effective_key", key)
			c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})
			tc.handle(c)
			require.Equal(t, http.StatusForbidden, c.Writer.Status())
			require.Equal(t, "max", *requeststate.RequestedReasoningEffortFromContext(c.Request.Context()))
		})
	}
}
