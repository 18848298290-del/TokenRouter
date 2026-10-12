package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

type QoderAuthorization struct {
	Core            *provider.QoderAuthorization[*qoder.AuthorizationFlow]
	ResolveProxyURL func(context.Context, *int64) (string, error)
	ClientFactory   qoder.OAuthClientFactory
}

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

func (p *QoderAuthorization) ValidateSite(raw string) (string, error) {
	site, err := qoder.ParseSite(raw)
	return string(site), err
}

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

	// The CN site reuses the authorization URL's UUID machine_id and leaves other machine fields empty.
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
	session := &provider.QoderAuthorizationSession[*qoder.AuthorizationFlow]{State: state, Flow: flow, CreatedAt: time.Now()}
	return sessionID, session, &provider.QoderAuthURLResult{AuthURL: flow.AuthURL, SessionID: sessionID, State: state, ExpiresIn: int64(provider.QoderOAuthSessionTTL / time.Second), Interval: provider.QoderOAuthPollInterval, Site: string(profile.Site)}, nil
}

// GenerateAuthURLForSite delegates session creation and authorization checks to the provider service.
func (p *QoderAuthorization) GenerateAuthURLForSite(ctx context.Context, site string, id *int64) (*provider.QoderAuthURLResult, error) {
	return p.Core.GenerateAuthURLForSite(ctx, site, id)
}

func (p *QoderAuthorization) ExchangeCode(ctx context.Context, input *provider.QoderExchangeCodeInput) (*provider.QoderTokenInfo, error) {
	return p.Core.ExchangeCode(ctx, input)
}

func (p *QoderAuthorization) Poll(ctx context.Context, id, state string, proxy *int64) (*provider.QoderPollResult, error) {
	return p.Core.Poll(ctx, id, state, proxy)
}
