package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// searchTransportFunc adapts a request assertion callback to standalone search
// transport.
type searchTransportFunc func(*http.Request, string, int64, int) (*http.Response, error)

// searchReadCloser records whether a supplied fixture response reader has been
// closed.
type searchReadCloser struct {
	io.Reader      // supplied response body reader
	closed    bool // response cleanup observed flag
}

// Do passes request, proxy, provider identity, and concurrency to the fixture
// callback.
func (f searchTransportFunc) Do(r *http.Request, p string, id int64, n int) (*http.Response, error) {
	return f(r, p, id, n)
}

// Close marks the fixture response body closed and returns a successful
// cleanup result.
func (r *searchReadCloser) Close() error { r.closed = true; return nil }

// TestGrokSearchExecutorPreservesRequestAndRelease executes API Key and OAuth
// search against a transport callback with a closable body. Assertions cover
// endpoint, token, proxy/isolation parameters, default-model injection,
// response data, and body closure.
func TestGrokSearchExecutorPreservesRequestAndRelease(t *testing.T) {
	for _, kind := range []string{capability.ProviderTypeAPIKey, capability.ProviderTypeOAuth} {
		t.Run(kind, func(t *testing.T) {
			response := &searchReadCloser{Reader: strings.NewReader(`{"output":[]}`)}
			proxyID := int64(2)
			value := &provider.Record{ID: 7, Platform: capability.PlatformGrok, Type: kind, Concurrency: 3, Credentials: map[string]any{"api_key": "key", "access_token": "oauth"}, ProxyID: &proxyID, Proxy: &egress.Proxy{Protocol: "http", Host: "localhost", Port: 9999}}
			defaults := 0
			executor := &GrokSearchExecutor{DefaultBaseURL: func() string { defaults++; return "https://api.x.ai" }, Transport: searchTransportFunc(func(r *http.Request, p string, id int64, n int) (*http.Response, error) {
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "/v1/responses", r.URL.Path)
				require.Equal(t, int64(7), id)
				require.Equal(t, 3, n)
				require.Equal(t, value.Proxy.URL(), p)
				token := "key"
				if kind == capability.ProviderTypeOAuth {
					token = "oauth"
				}
				require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
				require.Equal(t, "application/json", r.Header.Get("Accept"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NotEmpty(t, gjson.GetBytes(body, "model").String(), "原缺省模型补齐保持")
				return &http.Response{StatusCode: http.StatusOK, Body: response}, nil
			})}
			result, err := executor.Execute(context.Background(), value, []byte(`{"input":"query"}`))
			require.NoError(t, err)
			require.JSONEq(t, `{"output":[]}`, string(result))
			require.Equal(t, 1, defaults)
			require.True(t, response.closed)
		})
	}
}

// TestGrokSearchExecutorPreservesFailureClasses supplies HTTP 400,
// auth/payment, rate-limit, and server response fixtures. Selected failures
// retain status/body in failover errors, ordinary 400 stays a plain error, and
// every body is closed.
func TestGrokSearchExecutorPreservesFailureClasses(t *testing.T) {
	for _, status := range []int{400, 401, 402, 403, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			body := &searchReadCloser{Reader: strings.NewReader("upstream error")}
			executor := &GrokSearchExecutor{Transport: searchTransportFunc(func(*http.Request, string, int64, int) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: body}, nil
			})}
			_, err := executor.Execute(context.Background(), &provider.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "key"}}, []byte(`{"model":"grok"}`))
			require.Error(t, err)
			var retry *forward.UpstreamFailoverError
			require.Equal(t, status != 400, errors.As(err, &retry))
			if retry != nil {
				require.Equal(t, status, retry.StatusCode)
				require.Equal(t, "upstream error", string(retry.ResponseBody))
			}
			require.True(t, body.closed)
		})
	}
}

// TestGrokSearchExecutorCredentialFailurePrecedesURLRead supplies a Grok API
// Key target missing its token and callbacks that fail if invoked. Execution
// returns the credential-stage unauthorized failover reason before endpoint
// lookup or transport.
func TestGrokSearchExecutorCredentialFailurePrecedesURLRead(t *testing.T) {
	executor := &GrokSearchExecutor{DefaultBaseURL: func() string { t.Fatal("凭据失败不读取动态地址"); return "" }, Transport: searchTransportFunc(func(*http.Request, string, int64, int) (*http.Response, error) {
		t.Fatal("凭据失败不能发送")
		return nil, nil
	})}
	_, err := executor.Execute(context.Background(), &provider.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey}, nil)
	var failure *forward.UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, http.StatusUnauthorized, failure.StatusCode)
	require.Equal(t, forward.GatewayFailureReason("grok_search_token"), failure.Reason)
}
