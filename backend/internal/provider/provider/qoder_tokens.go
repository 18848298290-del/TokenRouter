package provider

import (
	"context"
	"errors"
	"sync"
	"time"

	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// errQoderSessionBuildInvalidated signals an invalidated build or newer credentials.
var errQoderSessionBuildInvalidated = providercore.ErrQoderSessionBuildInvalidated

type (
	qoderPATExchanger           func(ctx context.Context, pat string, machine *qoder.MachineIdentity) (*qoder.AuthIdentity, error)
	qoderCNPATExchanger         func(ctx context.Context, pat string, machine *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error)
	qoderOrganizationTagsGetter func(ctx context.Context, token, uid string) (*qoder.OrganizationTags, error)
)

type qoderSessionCacheEntry = providercore.QoderSessionCacheEntry[*qoder.SessionContext]

// QoderTokenProvider builds and caches COSY sessions for Qoder providers.
type QoderTokenProvider struct {
	Core                *qoderSessionState
	coreOnce            sync.Once
	exchangePAT         qoderPATExchanger
	exchangeCNPAT       qoderCNPATExchanger
	getOrgTags          qoderOrganizationTagsGetter
	httpUpstream        QoderTransport
	tlsFPProfileService *egressadapter.TLSProfiles
}

type qoderSessionState = providercore.QoderSessions[*qoder.SessionContext]

func NewQoderTokenProvider(builder qoder.SessionBuilder) *QoderTokenProvider {
	return &QoderTokenProvider{
		Core: &qoderSessionState{
			Sessions:            make(map[int64]qoderSessionCacheEntry),
			ProviderStates:      make(map[int64]providercore.QoderSessionProviderState),
			SessionBuildTimeout: providercore.DefaultQoderSessionBuildTimeout,
		},
		exchangePAT:   qoderPATExchanger(builder.ExchangePAT),
		exchangeCNPAT: qoderCNPATExchanger(builder.ExchangeCNPAT),
		getOrgTags:    qoderOrganizationTagsGetter(builder.GetOrgTags),
	}
}

func (p *QoderTokenProvider) SetHTTPUpstream(httpUpstream QoderTransport, tlsFPProfileService *egressadapter.TLSProfiles) {
	if p == nil {
		return
	}
	p.httpUpstream = httpUpstream
	p.tlsFPProfileService = tlsFPProfileService
}

func (p *QoderTokenProvider) GetSession(ctx context.Context, provider *providercore.Record) (*qoder.SessionContext, error) {
	if p == nil {
		return nil, errors.New("qoder token provider is nil")
	}
	return p.qoderState().GetSession(ctx, provider, func(ctx context.Context, value *providercore.Record) (*qoder.SessionContext, time.Time, error) {
		return p.buildSession(ctx, value)
	})
}

func (p *QoderTokenProvider) Invalidate(providerID int64) {
	if p != nil {
		p.qoderState().Invalidate(providerID)
	}
}

func (p *QoderTokenProvider) InvalidateProvider(provider *providercore.Record) {
	if p != nil {
		p.qoderState().InvalidateProvider(provider)
	}
}

// qoderState lazily initializes the shared session state that owns the cache and locks.
func (p *QoderTokenProvider) qoderState() *qoderSessionState {
	p.coreOnce.Do(func() {
		if p.Core == nil {
			p.Core = &qoderSessionState{}
		}
	})
	return p.Core
}

func (p *QoderTokenProvider) StopContext(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return p.qoderState().StopContext(ctx)
}

// QoderCredentialInput selects credentials for upstream exchange; the provider core owns cached snapshots.
func QoderCredentialInput(value *providercore.Record) *qoder.CredentialInput {
	if value == nil {
		return nil
	}
	return &qoder.CredentialInput{
		Name:               value.Name,
		Pat:                value.GetCredential("pat"),
		Site:               value.GetCredential("site"),
		RefreshMode:        value.GetCredential("refresh_mode"),
		SecurityOauthToken: value.GetCredential("security_oauth_token"),
		MachineId:          value.GetCredential("machine_id"),
		MachineToken:       value.GetCredential("machine_token"),
		MachineType:        value.GetCredential("machine_type"),
		Uid:                value.GetCredential("uid"),
		Aid:                value.GetCredential("aid"),
		IdentityName:       value.GetCredential("name"),
		UserType:           value.GetCredential("user_type"),
		RefreshToken:       value.GetCredential("refresh_token"),
		OrganizationId:     value.GetCredential("organization_id"),
		OrganizationName:   value.GetCredential("organization_name"),
	}
}

func (p *QoderTokenProvider) sessionBuilder(value *providercore.Record) *qoder.SessionBuilder {
	return &qoder.SessionBuilder{ExchangePAT: qoder.PATExchanger(p.exchangePAT), ExchangeCNPAT: qoder.CNPATExchanger(p.exchangeCNPAT), GetOrgTags: qoder.OrganizationTagsGetter(p.getOrgTags), Doer: QoderRequestDoer(value, p.httpUpstream, p.tlsFPProfileService)}
}

func (p *QoderTokenProvider) buildSession(ctx context.Context, value *providercore.Record) (*qoder.SessionContext, time.Time, error) {
	return p.sessionBuilder(value).BuildSession(ctx, QoderCredentialInput(value))
}
