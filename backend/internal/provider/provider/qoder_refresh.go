package provider

import (
	"context"
	"errors"
	"time"

	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// Qoder refresh results need to retain saved credential metadata while replacing
// the exchanged identity and its expiry. The adapter binds provider transport to
// a copy of the site exchange options, converts the returned identity, and asks
// the core merge helper to produce credentials for conditional persistence.

// QoderRefreshOptions supplies site exchange callbacks and optional credential
// conversion for refresh.
type QoderRefreshOptions struct {
	// Exchange, Transport and Profiles supply one refresh call. Refresh copies
	// Exchange and replaces its doer with the selection resolved from Transport
	// and Profiles, leaving the configured options available to later calls.
	Exchange  qoder.RefreshExchange       // site token exchange settings
	Transport QoderTransport              // optional shared HTTP transport
	Profiles  *egressprovider.TLSProfiles // optional TLS profile resolver
	// BuildCredentials replaces the platform-generated patch when configured. The
	// core still merges saved credentials and sets site, mode and exchange expiry.
	// The returned map becomes that merge destination. The callback grants mutation
	// ownership for this refresh, including saved metadata and expiry cleanup.
	BuildCredentials func(*provider.QoderTokenInfo) map[string]any
}

// QoderTokenRefresher exchanges site tokens and merges saved credential metadata.
type QoderTokenRefresher struct {
	options QoderRefreshOptions // retained refresh dependency settings
}

// NewQoderTokenRefresher retains exchange options for later provider
// credential refresh calls.
func NewQoderTokenRefresher(options QoderRefreshOptions) *QoderTokenRefresher {
	return &QoderTokenRefresher{options: options}
}

// CacheKey returns the core's provider-ID key used to coordinate Qoder refreshes.
func (r *QoderTokenRefresher) CacheKey(value *provider.Record) string {
	return provider.QoderTokenCacheKey(value)
}

// CanRefresh reports whether the record is a Qoder COSY provider eligible by kind.
func (r *QoderTokenRefresher) CanRefresh(value *provider.Record) bool {
	return provider.CanRefreshQoder(value)
}

// NeedsRefresh applies the core's expiry-based Qoder refresh eligibility.
// The duration argument is accepted by the interface while the core uses its
// fifteen-minute threshold, excludes PAT sessions, and requires a stored refresh
// token and expiry.
func (r *QoderTokenRefresher) NeedsRefresh(value *provider.Record, duration time.Duration) bool {
	return provider.NeedsRefreshQoder(value, duration)
}

// Refresh exchanges the provider's site credentials and returns a merged
// credential map.
// It rejects records outside Qoder COSY, binds transport for this call and returns
// exchange errors directly. BuildCredentials can replace the initial patch;
// the core retains existing metadata, normalizes site/mode, removes CN legacy
// machine fields, and drops stored expiry when the exchange supplies none.
// The caller persists the returned map through its refresh coordinator. Keep
// the record stable during exchange and merging. Inherited nested credential
// values remain shared with that record, so copy them before editing those values.
func (r *QoderTokenRefresher) Refresh(ctx context.Context, value *provider.Record) (map[string]any, error) {
	if !r.CanRefresh(value) {
		return nil, errors.New("not a qoder cosy provider")
	}
	input := QoderCredentialInput(value)
	exchange := r.options.Exchange
	exchange.Doer = QoderRequestDoer(value, r.options.Transport, r.options.Profiles)
	result, err := exchange.Refresh(ctx, input)
	if err != nil {
		return nil, err
	}
	patch := qoder.TokenInfoCredentials(result.Identity, input, result.Machine)
	if r.options.BuildCredentials != nil {
		token := qoder.BuildAuthorizationTokenInfoForSite(result.Identity, result.Machine, result.Site, result.Mode, time.Time{}, nil, nil)
		patch = r.options.BuildCredentials((*provider.QoderTokenInfo)(token))
	}
	// Exchange expiry controls the next refresh decision. Delegate merge cleanup
	// so an absent new expiry cannot leave an old near-expired deadline attached
	// to freshly exchanged credentials.
	return provider.MergeQoderRefreshCredentials(value.Credentials, patch, string(result.Site), result.Mode, result.ExpiresAt), nil
}
