package provider

import (
	"context"
	"fmt"

	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// Qoder creation and editing prepare machine identity before validating the
// provider's site and authentication form. Supported credential containers are
// cloned in
// the hook so preparation and validation work on the selected record, while
// PAT verification chooses CN20 or Global exchange with the provider's proxy
// and TLS request doer.

// qoderCredentialValidator combines site PAT verification with provider
// credential preparation hooks.
type qoderCredentialValidator struct {
	// transport and profiles bind the provider doer used by PAT verification.
	// Global validation uses the shared transport whenever supplied, which takes
	// precedence over the optional validatePAT test override.
	transport QoderTransport              // shared PAT HTTP transport
	profiles  *egressprovider.TLSProfiles // optional TLS profile resolver
	// validatePAT optionally supplies Global validation when transport is absent.
	// A nil callback selects the platform PAT exchanger with the prepared machine.
	validatePAT func(context.Context, *provider.Record, string, *qoder.MachineIdentity) (*qoder.AuthIdentity, error)
	// validateCNPAT optionally supplies CN validation and receives the
	// provider doer.
	// Nil selects the CN20 PAT flow with its fixed site profile.
	validateCNPAT func(context.Context, *provider.Record, string, *qoder.MachineIdentity, qoder.RequestDoer) (*qoder.AuthIdentity, error)
}

// CreateCredentialHooks binds Qoder machine preparation and site credential
// validation.
// The returned callbacks retain transport and profiles for PAT verification.
// Core creation and editing determine when validation runs and whether a PAT
// exchange may be deferred for that edit.
func CreateCredentialHooks(transport QoderTransport, profiles *egressprovider.TLSProfiles) provider.CreateCredentialHooks {
	return (&qoderCredentialValidator{transport: transport, profiles: profiles}).hooks()
}

// hooks returns site resolution, preparation and validation callbacks for the core.
// Prepare requires a nonnil record and clones supported credential containers
// before machine-field changes.
// Validate and ValidateEdit route to this adapter; the core remains responsible
// for its platform/type and direct-token requirements.
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

// validate runs credential validation with immediate PAT verification enabled.
// It delegates to validateEdit with deferPAT false, so site errors and PAT
// exchange failures are returned before core creation can accept the credentials.
func (v *qoderCredentialValidator) validate(ctx context.Context, value *provider.Record) error {
	return v.validateEdit(ctx, value, false)
}

// validateEdit prepares cloned credential and Extra containers for core validation.
// Nil records pass through the core's no-op path. The core validates site/mode
// and direct-token shape, and deferPAT lets it skip the exchange for supported
// edits. CN PAT validation uses CN20; Global validation prefers configured shared
// transport over the optional test callback.
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
			// CN PATs use the CN20 exchange and site profile. Keep that branch
			// separate
			// from Global Center validation so the machine and token route
			// match the selected site.
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
			// A configured shared transport takes precedence over the Global
			// fixture hook,
			// carrying provider proxy and TLS choices into the production PAT
			// exchange.
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

// qoderSiteForRecord parses the provider's site credential using platform
// normalization.
// Nil records return an error with the Global zero-result choice. Empty stored
// site resolves to Global; unsupported spellings return the parser's
// validation error.
func qoderSiteForRecord(value *provider.Record) (qoder.Site, error) {
	if value == nil {
		return qoder.SiteGlobal, fmt.Errorf("qoder: provider is nil")
	}
	return qoder.ParseSite(value.GetCredential("site"))
}
