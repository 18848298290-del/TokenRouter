package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	providerauth "github.com/TokenFlux/TokenRouter/internal/provider"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

func provideQoderAuthorizationHTTP(source *provideradapter.QoderAuthorization) *providerhttp.QoderOAuthHandler {
	return providerhttp.NewQoderOAuthHandler(source)
}

// provideQoderAuthorization binds proxy lookup; authorization owns sessions and upstream code owns requests.
func provideQoderAuthorization(proxies egress.ProxyRepository) *provideradapter.QoderAuthorization {
	return provideradapter.NewQoderAuthorization(func(ctx context.Context, id *int64) (string, error) {
		if id == nil {
			return "", nil
		}
		if proxies == nil {
			return "", errors.New("proxy repository is not configured")
		}
		proxy, err := proxies.GetByID(ctx, *id)
		if err != nil {
			return "", fmt.Errorf("get proxy: %w", err)
		}
		if proxy == nil {
			return "", errors.New("proxy not found")
		}
		return proxy.URL(), nil
	}, nil)
}

// provideQoderTokens creates the shared execution session cache with upstream site exchange.
func provideQoderTokens(transport provideradapter.QoderTransport, profiles *egressprovider.TLSProfiles) *provideradapter.QoderTokenProvider {
	source := provideradapter.NewQoderTokenProvider(qoder.SessionBuilder{})
	source.SetHTTPUpstream(transport, profiles)
	return source
}

// provideTokenCacheInvalidator binds the shared session and access-token caches.
func provideTokenCacheInvalidator(cache providerauth.AccessTokenCache, sessions *provideradapter.QoderTokenProvider) providerauth.TokenCacheInvalidator {
	return providerauth.NewCompositeTokenCacheInvalidator(cache, sessions, slog.Warn)
}
