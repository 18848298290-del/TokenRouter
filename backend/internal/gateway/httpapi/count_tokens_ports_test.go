package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Count preparation and failed-attempt cleanup share HTTP state with the retry
// loop. These cases observe cancellation status and provider callback order,
// verifying mapped bodies are rebuilt from base input and a failed target's
// session is released before the next provider is used.

// TestFailoverClientGone compares active, canceled, and already written requests.
// Active input must stay unchanged, unwritten canceled input must select 499,
// and written canceled input must retain its prior status while reporting completion of the retry window.
func TestFailoverClientGone(t *testing.T) {
	t.Run("活跃请求返回false", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

		require.False(t, FailoverClientGone(c))
		require.Equal(t, http.StatusOK, c.Writer.Status(), "不应改动状态码")
	})

	t.Run("客户端已断开_返回true并标记499", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)

		require.True(t, FailoverClientGone(c))
		require.Equal(t, StatusClientClosedRequest, c.Writer.Status())
	})

	t.Run("响应已提交_不改状态码", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
		c.String(http.StatusOK, "partial")

		require.True(t, FailoverClientGone(c))
		require.Equal(t, http.StatusOK, c.Writer.Status(), "已提交的状态码不应被覆盖")
	})

	t.Run("nil安全", func(t *testing.T) {
		require.False(t, FailoverClientGone(nil))
	})
}

// TestCountTokensNativeHTTPAttemptContract checks funding preflight, fresh body rewrites, and failed-session release
// for slot-free counts.
// Its callback trace must put funding before planning and selection, release
// the first failed target before reselection, and forward each attempt's own
// mapped model before returning the asserted count and synchronous request type.
func TestCountTokensNativeHTTPAttemptContract(t *testing.T) {
	fixture := &countHTTPContract{t: t, group: 42}
	key := &apikey.APIKey{ID: 7, GroupID: &fixture.group, Group: &routing.Group{}}
	ports := CountHTTPPorts{
		Executor: fixture, Funding: fixture, Diagnoser: routing.ModelAvailabilityDiagnoserFunc(unexpectedCountModelDiagnosis),
		ReadAccess:           func(*gin.Context) (*apikey.APIKey, bool) { return key, true },
		ObserveCompatibility: func(*zap.Logger) {},
		BusinessError: func(*gin.Context, error, bool, func(int, string, string, bool)) bool {
			t.Fatal("未预期的选择失败")
			return false
		},
		Failure: func(*gin.Context, *forwardcore.UpstreamFailoverError, string, bool) { t.Fatal("未预期的耗尽") },
	}
	handler := NewCountTokensHandler(1<<20, 2, ports, fixture)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"model":"client-model","messages":[{"role":"user","content":"hello"}]}`))
	c.Request = c.Request.WithContext(apikey.WithForcePlatform(c.Request.Context(), "antigravity"))
	authctx.SetPrincipal(c, identity.Principal{UserID: 1}, 1, "")
	handler.CountTokens(c)
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"input_tokens":17}`, response.Body.String())
	require.Equal(t, []string{"funding", "plan", "select", "plan", "forward", "release", "select", "plan", "forward"}, fixture.events)
	require.Empty(t, fixture.platform)
	require.Len(t, fixture.bodies, 2)
	require.Equal(t, "attempt-1", gjson.GetBytes(fixture.bodies[0], "model").String())
	require.Equal(t, "attempt-2", gjson.GetBytes(fixture.bodies[1], "model").String())
	value, ok := c.Get(opsRequestTypeKey)
	require.True(t, ok)
	require.Equal(t, int16(usage.RequestTypeSync), value)
}
