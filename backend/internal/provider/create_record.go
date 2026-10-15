package provider

import (
	"errors"
	"time"
)

// create_record.go constructs validated provider records with normalized
// credentials, quota windows, and scheduling defaults.
// Returned new-provider records are active and schedulable after configuration
// validation and protocol migration. Creation removes imported monitor/session observations
// and fingerprint seeds before generating any required account seed. Credentials
// can be mutated during normalization, so failed construction can leave supplied
// maps changed even though no provider is returned or persisted.
//
// Quota reset configuration is validated before dates and fixed-window usage are
// reconciled. Expiration seconds become a new time value, while accepted rate
// multiplier and load-factor pointers remain borrowed from the input. Defaults
// and comparisons differ: expiration pauses default on, negative multipliers
// fail, and nonpositive load factors remain unset. Administrative persistence and
// group binding occur after this constructor returns its validated record.

// CreationOptions supplies time and fingerprint dependencies used to normalize
// provider configuration before administrative creation or duplication.
type CreationOptions struct {
	// Now and LoadLocation are retained on the record and must be callable when
	// normalized Extra is nonnil. Reset calculation and fixed-window reconciliation
	// read Now separately; LoadLocation resolves their configured zone or UTC.
	Now          func() time.Time                     // quota calculation observation clock
	LoadLocation func(string) (*time.Location, error) // quota time-zone lookup function
	// NewSeed is required when OpenAI OAuth-like fingerprint mode needs a generated
	// seed. Supplied seeds are removed before that mode decides whether to call it.
	NewSeed func() string
}

// BuildProviderForCreate returns a validated active, schedulable record for a
// nonnil creation input. Normalization can mutate input credentials and the
// supplied Extra map before copying their top-level containers, including on
// error. Nested values and accepted multiplier, load-factor, and proxy pointers
// can remain shared with caller input.
//
// System-managed observations are removed before fingerprint preparation and
// protocol migration. Quota settings are validated before their dates and usage
// windows are calculated, using two clock reads when Extra is nonnil. Positive
// expiration seconds produce a new time pointer; auto-pause defaults true.
// Negative rate multipliers fail, while load factors are accepted only when
// positive and at most 10000. The multiplier comparison does not reject NaN;
// load factors use integer input and remain unset when nonpositive. No persistence
// occurs in this constructor.
func BuildProviderForCreate(input *CreateProviderInput, providerExtra map[string]any, options CreationOptions) (*Record, error) {
	// Discard deprecated configuration before interpreting usage settings, then
	// remove system-managed monitor and session observations so creation starts
	// without imported runtime state.
	DiscardDeprecatedProviderExtra(providerExtra)
	if err := NormalizeUpstreamUsageExtra(providerExtra); err != nil {
		return nil, err
	}
	delete(providerExtra, OllamaCloudUsageSessionExtraKey)
	delete(providerExtra, OllamaCloudUsageAutoRefreshExtraKey)
	delete(providerExtra, OllamaCloudUsageSnapshotExtraKey)
	delete(providerExtra, CNUsageMonitorSnapshotExtraKey)
	// Fingerprint preparation strips caller-supplied seeds and creates a seed only
	// for modes requiring one, so its value comes from the creation factory rather
	// than an imported seed entry.
	providerExtra = PrepareCodexFingerprintExtraForCreate(input.Platform, input.Type, providerExtra, options.NewSeed)
	provider := &Record{
		Now: options.Now, LoadLocation: options.LoadLocation,
		Name:        input.Name,
		Notes:       NormalizeProviderNotes(input.Notes),
		Platform:    input.Platform,
		Type:        input.Type,
		Credentials: input.Credentials,
		Extra:       providerExtra,
		ProxyID:     input.ProxyID,
		Concurrency: NormalizeProviderConcurrency(input.Platform, input.Type, input.Concurrency),
		Priority:    input.Priority,
		Status:      StatusActive,
		Schedulable: true,
	}
	if err := NormalizeCNProviderCredentials(provider, true); err != nil {
		return nil, err
	}
	if err := NormalizeOpenAIAPIKeyConfiguration(provider); err != nil {
		return nil, err
	}
	if err := NormalizeProviderProtocols(provider); err != nil {
		return nil, err
	}
	// Validate reset mode, time, and zone before calculating dates or resetting
	// fixed-window usage, so rejected configuration cannot publish derived quota values.
	if provider.Extra != nil {
		if err := ValidateQuotaResetConfig(provider.Extra, options.LoadLocation); err != nil {
			return nil, err
		}
		ComputeQuotaResetAt(provider.Extra, options.Now(), options.LoadLocation)
		NormalizeFixedQuotaWindows(provider.Extra, options.Now(), options.LoadLocation)
	}
	if input.ExpiresAt != nil && *input.ExpiresAt > 0 {
		// Positive Unix seconds become a separate expiration time value.
		expiresAt := time.Unix(*input.ExpiresAt, 0)
		provider.ExpiresAt = &expiresAt
	}
	if input.AutoPauseOnExpired != nil {
		provider.AutoPauseOnExpired = *input.AutoPauseOnExpired
	} else {
		provider.AutoPauseOnExpired = true
	}
	if input.RateMultiplier != nil {
		// Zero multipliers are accepted; negative values reject construction.
		if *input.RateMultiplier < 0 {
			return nil, errors.New("rate_multiplier must be >= 0")
		}
		provider.RateMultiplier = input.RateMultiplier
	}
	if input.LoadFactor != nil && *input.LoadFactor > 0 {
		// Reject load denominators above ten thousand before retaining their pointer.
		if *input.LoadFactor > 10000 {
			return nil, errors.New("load_factor must be <= 10000")
		}
		provider.LoadFactor = input.LoadFactor
	}
	if err := ValidateGeminiThirdPartyBaseURL(provider); err != nil {
		return nil, err
	}
	if _, err := BalanceAlertConfiguration(provider); err != nil {
		return nil, err
	}
	return provider, nil
}
