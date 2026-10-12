package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"

	servertiming "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

// Shared req-client fixtures reset the package cache serially and inspect
// pointer reuse, option separation, and construction failures. An unexpected
// cache type remains stored while a fresh usable client is returned; a local
// HTTP server separately demonstrates dependency timing registration.

// TestGetSharedReqClient_ForceHTTP2SeparatesCache constructs otherwise equal
// options with different HTTP/2 settings. It requires distinct clients and
// distinct cache keys so one negotiation choice does not reuse the other's
// entry.
func TestGetSharedReqClient_ForceHTTP2SeparatesCache(t *testing.T) {
	sharedReqClients = sync.Map{}
	base := ReqClientOptions{
		ProxyURL: "http://proxy.local:8080",
		Timeout:  time.Second,
	}
	clientDefault, err := GetSharedReqClient(base)
	require.NoError(t, err)

	force := base
	force.ForceHTTP2 = true
	clientForce, err := GetSharedReqClient(force)
	require.NoError(t, err)

	require.NotSame(t, clientDefault, clientForce)
	require.NotEqual(t, buildReqClientKey(base), buildReqClientKey(force))
}

// TestGetSharedReqClient_ReuseCachedClient requests the same construction
// options twice after clearing the cache. It requires the same client pointer
// on both calls, protecting reuse within that option identity.
func TestGetSharedReqClient_ReuseCachedClient(t *testing.T) {
	sharedReqClients = sync.Map{}
	opts := ReqClientOptions{
		ProxyURL: "http://proxy.local:8080",
		Timeout:  2 * time.Second,
	}
	first, err := GetSharedReqClient(opts)
	require.NoError(t, err)
	second, err := GetSharedReqClient(opts)
	require.NoError(t, err)
	require.Same(t, first, second)
}

// TestGetSharedReqClient_IgnoresNonClientCache seeds an unexpected value
// under the requested key. It requires a usable returned client while the
// stored value keeps its string type after LoadOrStore.
func TestGetSharedReqClient_IgnoresNonClientCache(t *testing.T) {
	sharedReqClients = sync.Map{}
	opts := ReqClientOptions{
		ProxyURL: " http://proxy.local:8080 ",
		Timeout:  3 * time.Second,
	}
	key := buildReqClientKey(opts)
	sharedReqClients.Store(key, "invalid")

	client, err := GetSharedReqClient(opts)
	require.NoError(t, err)

	require.NotNil(t, client)
	loaded, ok := sharedReqClients.Load(key)
	require.True(t, ok)
	require.IsType(t, "invalid", loaded)
}

// TestGetSharedReqClient_ImpersonateAndProxy supplies a spaced proxy URL with
// Chrome impersonation enabled. It requires successful construction and the
// exact trimmed proxy, timeout, and flag cache-key text.
func TestGetSharedReqClient_ImpersonateAndProxy(t *testing.T) {
	sharedReqClients = sync.Map{}
	opts := ReqClientOptions{
		ProxyURL:    "  http://proxy.local:8080  ",
		Timeout:     4 * time.Second,
		Impersonate: true,
	}
	client, err := GetSharedReqClient(opts)
	require.NoError(t, err)

	require.NotNil(t, client)
	require.Equal(t, "http://proxy.local:8080|4s|true|false", buildReqClientKey(opts))
}

// TestGetSharedReqClient_InvalidProxyURL supplies malformed proxy syntax to a
// cleared cache. It requires construction to return an error identifying
// invalid proxy URL parsing before a client is returned.
func TestGetSharedReqClient_InvalidProxyURL(t *testing.T) {
	sharedReqClients = sync.Map{}
	opts := ReqClientOptions{
		ProxyURL: "://missing-scheme",
		Timeout:  time.Second,
	}
	_, err := GetSharedReqClient(opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid proxy URL")
}

// TestGetSharedReqClient_ProxyURLMissingHost supplies an HTTP proxy URL with
// no host. It requires construction to fail with the missing-host diagnostic
// rather than accepting unusable proxy routing.
func TestGetSharedReqClient_ProxyURLMissingHost(t *testing.T) {
	sharedReqClients = sync.Map{}
	opts := ReqClientOptions{
		ProxyURL: "http://",
		Timeout:  time.Second,
	}
	_, err := GetSharedReqClient(opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "proxy URL missing host")
}

// TestInstrumentReqClientRecordsDependency sends a request with a collector
// to a local no-content server. It requires successful HTTP status and an
// HTTP dependency metric after req transport instrumentation.
func TestInstrumentReqClientRecordsDependency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	collector := servertiming.New(time.Now())
	ctx := servertiming.WithCollector(context.Background(), collector)
	client := InstrumentReqClient(req.C())
	response, err := client.R().SetContext(ctx).Get(server.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, response.StatusCode)

	header := collector.HeaderValue(time.Now(), "bypass")
	require.True(t, strings.Contains(header, "dep_http;dur="), header)
}
