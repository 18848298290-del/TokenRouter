package provider

import (
	"context"
	"fmt"

	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

type qoderCredentialValidator struct {
	transport     QoderTransport
	profiles      *egressprovider.TLSProfiles
	validatePAT   func(context.Context, *provider.Record, string, *qoder.MachineIdentity) (*qoder.AuthIdentity, error)
	validateCNPAT func(context.Context, *provider.Record, string, *qoder.MachineIdentity, qoder.RequestDoer) (*qoder.AuthIdentity, error)
}

// CreateCredentialHooks handles machine identity, credential compatibility, and PAT validation during creation
// and edits.
func CreateCredentialHooks(transport QoderTransport, profiles *egressprovider.TLSProfiles) provider.CreateCredentialHooks {
	return (&qoderCredentialValidator{transport: transport, profiles: profiles}).hooks()
}

func (v *qoderCredentialValidator) hooks() provider.CreateCredentialHooks {
	return provider.CreateCredentialHooks{
		Site: func(value *provider.Record) (string, error) {
			site, err := qoderSiteForRecord(value)
			return string(site), err
		},
		Prepare: func(value *provider.Record) {
			value.Credentials = provider.CloneValues(value.Credentials)
			ensureQoderMachineCredentials(value)
		},
		Validate:     v.validate,
		ValidateEdit: v.validateEdit,
	}
}

func (v *qoderCredentialValidator) validate(ctx context.Context, value *provider.Record) error {
	return v.validateEdit(ctx, value, false)
}

func (v *qoderCredentialValidator) validateEdit(ctx context.Context, value *provider.Record, deferPAT bool) error {
	if value != nil {
		value.Credentials = provider.CloneValues(value.Credentials)
		value.Extra = provider.CloneValues(value.Extra)
	}
	return provider.ValidateQoderCredentials(ctx, value, deferPAT, provider.QoderCredentialValidation{
		Normalize: func(site, mode string) (string, error) {
			parsed, err := qoder.ParseSite(site)
			if err != nil {
				return "", err
			}
			_, err = qoder.ParseRefreshMode(mode)
			return string(parsed), err
		},
		ValidatePAT: func(ctx context.Context, value *provider.Record, site, pat string) error {
			machine := qoder.MachineForCredentials(QoderCredentialInput(value))
			doer := QoderRequestDoer(value, v.transport, v.profiles)
			if qoder.Site(site) == qoder.SiteCN {
				validate := v.validateCNPAT
				if validate == nil {
					validate = func(ctx context.Context, _ *provider.Record, pat string, machine *qoder.MachineIdentity, doer qoder.RequestDoer) (*qoder.AuthIdentity, error) {
						profile, err := qoder.ProfileForSite(qoder.SiteCN)
						if err != nil {
							return nil, err
						}
						identity, _, err := qoder.ExchangeQoderCN20PATContext(ctx, pat, machine, profile, doer)
						return identity, err
					}
				}
				if _, err := validate(ctx, value, pat, machine, doer); err != nil {
					return fmt.Errorf("validate qoder cn pat: %w", err)
				}
				return nil
			}
			validate := v.validatePAT
			if validate == nil || v.transport != nil {
				validate = func(ctx context.Context, _ *provider.Record, pat string, machine *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
					return qoder.ExchangePATContext(ctx, pat, machine, "", doer)
				}
			}
			if _, err := validate(ctx, value, pat, machine); err != nil {
				return fmt.Errorf("validate qoder pat: %w", err)
			}
			return nil
		},
	})
}

func qoderSiteForRecord(value *provider.Record) (qoder.Site, error) {
	if value == nil {
		return qoder.SiteGlobal, fmt.Errorf("qoder: provider is nil")
	}
	return qoder.ParseSite(value.GetCredential("site"))
}
