package httpclient

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/imroc/req/v3"

	proxyurl "github.com/TokenFlux/TokenRouter/internal/infra/httpclient/proxy"
	servertiming "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

// sharedReqClients caches req clients by construction options.
//
// Clients share their underlying connection pools.
// The key includes proxy, timeout, impersonation, and HTTP/2 settings.
// Concurrent misses may construct multiple candidates.
// LoadOrStore selects the shared instance returned to callers.
var sharedReqClients sync.Map

// ReqClientOptions configures shared req client construction.
type ReqClientOptions struct {
	ProxyURL    string        // HTTP, HTTPS, SOCKS5, or SOCKS5H proxy URL
	Timeout     time.Duration // Overall request timeout
	Impersonate bool          // Use req's Chrome fingerprint
	ForceHTTP2  bool          // Force HTTP/2
}

// GetSharedReqClient returns the shared req client for these options.
// Callers must not mutate its configuration.
func GetSharedReqClient(opts ReqClientOptions) (*req.Client, error) {
	key := buildReqClientKey(opts)
	if cached, ok := sharedReqClients.Load(key); ok {
		if c, ok := cached.(*req.Client); ok {
			return c, nil
		}
	}

	client := req.C().SetTimeout(opts.Timeout)
	if opts.ForceHTTP2 {
		client = client.EnableForceHTTP2()
	}
	if opts.Impersonate {
		client = client.ImpersonateChrome()
	}
	trimmed, _, err := proxyurl.Parse(opts.ProxyURL)
	if err != nil {
		return nil, err
	}
	if trimmed != "" {
		client.SetProxyURL(trimmed)
	}
	client = InstrumentReqClient(client)

	actual, _ := sharedReqClients.LoadOrStore(key, client)
	if c, ok := actual.(*req.Client); ok {
		return c, nil
	}
	return client, nil
}

func InstrumentReqClient(client *req.Client) *req.Client {
	if client == nil {
		return nil
	}
	client.GetTransport().WrapRoundTripFunc(func(rt http.RoundTripper) req.HttpRoundTripFunc {
		timed := servertiming.WrapRoundTripper(rt)
		return timed.RoundTrip
	})
	return client
}

func buildReqClientKey(opts ReqClientOptions) string {
	return fmt.Sprintf("%s|%s|%t|%t",
		strings.TrimSpace(opts.ProxyURL),
		opts.Timeout.String(),
		opts.Impersonate,
		opts.ForceHTTP2,
	)
}
