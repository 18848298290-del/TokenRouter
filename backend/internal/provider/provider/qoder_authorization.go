package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// Qoder device authorization keeps a site-specific machine identity together
// with its authorization URL, nonce and PKCE verifier. The adapter copies the
// request's machine_id into the stored flow so later completion uses the same
// identity that was advertised to the browser, including CN's UUID-only shape.
//
// The core owns session expiry, completion claims and replayed token results.
// Preparation resolves the selected proxy once and retains its URL in the flow;
// later exchange and polling use that creation-time proxy. The client factory
// is selected during construction, while preparation returns both the private
// flow and public session proof without starting session cleanup itself.

// QoderAuthorization adapts site device flows to the core authorization
// session service.
type QoderAuthorization struct {
	// Core owns session storage, expiry and concurrent completion claims. The
	// constructor binds this adapter as its preparation and
	// platform-completion callbacks.
	Core *provider.QoderAuthorization[*qoder.AuthorizationFlow]
	// ResolveProxyURL is required during preparation, including when proxyID
	// is nil.
	// Its result becomes the stored flow proxy and its errors stop session
	// creation.
	ResolveProxyURL func(context.Context, *int64) (string, error)
	// ClientFactory constructs completion clients from the stored flow. A nil
	// constructor input selects DefaultOAuthClientFactory before any
	// authorization session is prepared.
	ClientFactory qoder.OAuthClientFactory
}

// NewQoderAuthorization creates a session core with site-specific device-flow
// callbacks.
// Resolve is required during URL preparation. A nil factory selects the platform
// default; construction allocates session storage but cleanup starts only when
// the core lifecycle is started by its owner.
func NewQoderAuthorization(resolve func(context.Context, *int64) (string, error), factory qoder.OAuthClientFactory) *QoderAuthorization {
	p := &QoderAuthorization{ResolveProxyURL: resolve, ClientFactory: factory}
	if p.ClientFactory == nil {
		p.ClientFactory = qoder.DefaultOAuthClientFactory
	}
	p.Core = &provider.QoderAuthorization[*qoder.AuthorizationFlow]{Store: provider.NewQoderAuthorizationStore[*qoder.AuthorizationFlow](), Prepare: p.prepare, Complete: func(ctx context.Context, flow *qoder.AuthorizationFlow) (*provider.QoderTokenInfo, bool, error) {
		token, pending, err := qoder.CompleteAuthorization(ctx, flow, p.ClientFactory)
		return (*provider.QoderTokenInfo)(token), pending, err
	}}
	return p
}

// ValidateSite normalizes a supported site name or returns its validation error.
// Trimmed case-insensitive Global and CN names are accepted. Empty input selects
// Global for legacy credentials through the platform parser.
func (p *QoderAuthorization) ValidateSite(raw string) (string, error) {
	site, err := qoder.ParseSite(raw)
	return string(site), err
}

// prepare constructs the private device flow and its public session proof.
// Unsupported site or proxy-resolution failure returns an error before the flow
// is stored. The public result carries session/state proof, the core's ten-minute
// expiry in seconds, poll interval and selected site.
//
// The generated device request supplies nonce, verifier, URL and machine_id.
// Preparation installs that same ID in the site's machine shape so the browser
// authorization and later token completion use one identity. The resolved proxy
// is captured in the flow rather than reread from a later poll argument.
func (p *QoderAuthorization) prepare(ctx context.Context, site string, proxyID *int64) (string, *provider.QoderAuthorizationSession[*qoder.AuthorizationFlow], *provider.QoderAuthURLResult, error) {
	profile, err := qoder.ProfileForSite(qoder.Site(site))
	if err != nil {
		return "", nil, nil, err
	}
	req, err := qoder.NewDeviceAuthRequestForProfile(profile)
	if err != nil {
		return "", nil, nil, fmt.Errorf("generate qoder device auth request: %w", err)
	}
	sessionID := qoder.RandomHex(32)
	state := qoder.RandomToken(32)

	proxyURL, err := p.ResolveProxyURL(ctx, proxyID)
	if err != nil {
		return "", nil, nil, err
	}

	// The request URL already names its machine_id. Install that ID into the
	// site's machine shape so completion uses the browser identity; CN leaves
	// its other machine fields empty.
	machine := qoder.NewMachineForSite(profile.Site)
	machine.MachineID = req.MachineID
	flow := &qoder.AuthorizationFlow{
		Nonce:        req.Nonce,
		CodeVerifier: req.CodeVerifier,
		Machine:      machine,
		AuthURL:      req.AuthorizationURL(),
		Site:         profile.Site,
		Profile:      profile,
		ProxyURL:     proxyURL,
	}
	// Public expiry is expressed in seconds, matching the authorization
	// response field
	// while the core retains a duration for its session lifetime checks.
	session := &provider.QoderAuthorizationSession[*qoder.AuthorizationFlow]{State: state, Flow: flow, CreatedAt: time.Now()}
	return sessionID, session, &provider.QoderAuthURLResult{AuthURL: flow.AuthURL, SessionID: sessionID, State: state, ExpiresIn: int64(provider.QoderOAuthSessionTTL / time.Second), Interval: provider.QoderOAuthPollInterval, Site: string(profile.Site)}, nil
}

// GenerateAuthURLForSite prepares and stores a device session through Core.
// Site and proxy errors stop creation. The returned proof belongs to that
// session, and stopped authorization rejects new preparation through the core's
// activity barrier.
func (p *QoderAuthorization) GenerateAuthURLForSite(ctx context.Context, site string, id *int64) (*provider.QoderAuthURLResult, error) {
	return p.Core.GenerateAuthURLForSite(ctx, site, id)
}

// ExchangeCode delegates normalized session proof and completion to Core.
// The core can fill state/code from CallbackURL in input, validates session state,
// and reports pending authorization as an error. Completed token results can be
// replayed from the same stored session.
func (p *QoderAuthorization) ExchangeCode(ctx context.Context, input *provider.QoderExchangeCodeInput) (*provider.QoderTokenInfo, error) {
	return p.Core.ExchangeCode(ctx, input)
}

// Poll reports pending or completed authorization through the stored device flow.
// It validates session proof and can wait for another completion claim with ctx.
// The proxy argument is accepted by the core for compatibility; the flow uses
// its creation-time proxy.
func (p *QoderAuthorization) Poll(ctx context.Context, id, state string, proxy *int64) (*provider.QoderPollResult, error) {
	return p.Core.Poll(ctx, id, state, proxy)
}
