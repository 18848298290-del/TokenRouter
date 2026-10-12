package provider

import (
	"context"
	"maps"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

func (s *Admin) CreateProvider(ctx context.Context, input *CreateProviderInput) (*Record, error) {
	providerExtra := maps.Clone(input.Extra)
	DiscardDeprecatedProviderExtra(providerExtra)
	if err := NormalizeUpstreamUsageExtra(providerExtra); err != nil {
		return nil, err
	}
	providerExtra, err := NormalizeGrokMediaEligibilityExtra(input.Platform, providerExtra)
	if err != nil {
		return nil, err
	}
	if err := ValidateUpstreamRequestIDHeaderExtra(providerExtra); err != nil {
		return nil, err
	}

	// Keep requested group bindings for validation and persistence.
	groupIDs := input.GroupIDs

	// Normalize header names to lowercase and validate overrides.
	if err := egress.NormalizeHeaderOverrideCredentials(input.Credentials); err != nil {
		return nil, err
	}
	// Temporary SSO and passwords must not be stored after OAuth exchange.
	input.Credentials = SanitizeStoredCredentials(input.Platform, input.Credentials)

	provider, err := BuildProviderForCreate(input, providerExtra, s.options.Creation)
	if err != nil {
		return nil, err
	}
	// Create machine identity for new Qoder providers; edits read existing identity under compatibility rules.
	if provider.IsQoderCosy() {
		s.options.Credentials.Prepare(provider)
	}
	s.attachProxyForValidation(ctx, provider)
	if err := s.options.Credentials.Validate(ctx, provider); err != nil {
		return nil, err
	}
	if err := s.providerRepo.Create(ctx, provider); err != nil {
		return nil, err
	}

	// Bind groups after the provider has been created.
	if len(groupIDs) > 0 {
		if err := s.providerRepo.BindGroups(ctx, provider.ID, groupIDs); err != nil {
			return nil, err
		}
	}

	// Background work owns a provider copy separate from the value returned to HTTP callers.
	if provider.Type == ProviderTypeOAuth && (provider.Platform == PlatformOpenAI || provider.Platform == PlatformAntigravity) {
		value := CloneRecord(provider)
		s.options.Background("service/admin_provider.go:CreateProvider", func() {
			defer func() {
				if r := recover(); r != nil {
					event := "create_provider_openai_privacy_panic"
					if value.Platform == PlatformAntigravity {
						event = "create_provider_antigravity_privacy_panic"
					}
					s.options.Error(event, "provider_id", value.ID, "recover", r)
				}
			}()
			if value.Platform == PlatformOpenAI {
				s.options.Privacy.EnsureOpenAIPrivacy(context.Background(), value)
			} else {
				s.options.Privacy.EnsureAntigravityPrivacy(context.Background(), value)
			}
		})
	}

	return provider, nil
}

func (s *Admin) attachProxyForValidation(ctx context.Context, value *Record) {
	if s.options.Proxies == nil || value == nil || value.Proxy != nil || value.ProxyID == nil || *value.ProxyID <= 0 {
		return
	}
	if proxy, err := s.options.Proxies.GetByID(ctx, *value.ProxyID); err == nil && proxy != nil {
		value.Proxy = proxy
	}
}
