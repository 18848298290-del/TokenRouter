package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// Provider health policies use separate persisted settings keys and different
// normalization rules. Missing or malformed cooldown settings select their
// owning defaults, while valid decoded values can still require range
// correction. Setters validate administrator input before storage and may
// normalize the same supplied object when a disabled control permits an
// otherwise invalid value.
//
// OpenAI 403 policy distinguishes the enabled cooldown from its optional
// failure-threshold action, so each switch controls validation of its own
// timing and count fields. The API Key health breaker additionally keeps a
// thirty-second scalar snapshot and returns value copies to callers. Platform
// scheduling thresholds use another cache owned by the same runtime instance,
// with their coalesced reads and committed-setting publication implemented
// alongside the threshold parser.

// Persisted provider policy keys and image-cooldown bounds connect settings
// storage with health actions; the API Key breaker lifetime controls
// process-local reuse.
const (
	// SettingKeyOllamaCloudUsageSettings identifies persisted Ollama Cloud usage
	// settings.
	SettingKeyOllamaCloudUsageSettings = "ollama_cloud_usage_settings"

	// These keys select independently stored cooldown, failure-counter, and
	// stream policies interpreted by provider health commands.
	// SettingKeyOpenAI403CooldownSettings: OAuth 403 policy.
	SettingKeyOpenAI403CooldownSettings = "openai_oauth_403_cooldown_settings"
	// SettingKeyOpenAIAPIKeyHealthBreakerSettings: pool API Key failure policy.
	SettingKeyOpenAIAPIKeyHealthBreakerSettings = "openai_apikey_health_breaker_settings"
	// SettingKeyOpenAIImagesOAuthUnavailableCooldownSettings: OAuth image
	// cooldown policy.
	SettingKeyOpenAIImagesOAuthUnavailableCooldownSettings = "openai_images_oauth_unavailable_cooldown_settings"
	// SettingKeyOverloadCooldownSettings: overload cooldown policy.
	SettingKeyOverloadCooldownSettings = "overload_cooldown_settings"
	// SettingKeyRateLimit429CooldownSettings: rate-limit response policy.
	SettingKeyRateLimit429CooldownSettings = "rate_limit_429_cooldown_settings"
	// SettingKeyStreamTimeoutSettings: stream timeout policy.
	SettingKeyStreamTimeoutSettings = "stream_timeout_settings"

	// OpenAIImagesOAuthUnavailableDefaultCooldownMinutes selects the built-in
	// image pause when stored policy data is absent, malformed, or outside the
	// supported range.
	OpenAIImagesOAuthUnavailableDefaultCooldownMinutes = 30

	// OpenAIImagesOAuthUnavailableMaxCooldownMinutes bounds administrator image
	// pauses and selects default replacement for excessive stored durations.
	OpenAIImagesOAuthUnavailableMaxCooldownMinutes = 120
	// openAIAPIKeyHealthBreakerSettingsCacheTTL: process policy reuse duration.
	openAIAPIKeyHealthBreakerSettingsCacheTTL = 30 * time.Second
)

// RuntimeSettingsStore supplies persisted single-key provider policy reads
// and writes.
type RuntimeSettingsStore interface {
	// GetValue returns a stored policy string for key. Missing keys use the
	// sentinel passed to NewRuntimeSettings so each getter can distinguish
	// defaults from an actual storage outage.
	GetValue(context.Context, string) (string, error)
	// Set persists the encoded policy under its settings key. Setters have
	// already validated or normalized their input before calling it, and
	// propagate its error to the administrator.
	Set(context.Context, string, string) error
}

// RuntimeSettings retains the provider settings store and process-local
// policy caches. Construct it before use and keep repository dependencies
// usable while synchronous and detached threshold reads are active. Keep this
// runtime at its original address after first use, as required by its atomic
// caches and flight synchronization.
type RuntimeSettings struct {
	// providerSchedulingThresholdsCache and providerSchedulingThresholdsSF
	// combine an immutable published map with coalesced reloads. The cached
	// entry is replaced as a whole, and returned maps are copied.
	providerSchedulingThresholdsCache atomic.Value       // atomic platform threshold observation
	providerSchedulingThresholdsSF    singleflight.Group // shared threshold read group
	// settingRepo is borrowed for later policy reads and writes. Detached
	// threshold lookups can continue using it after the initiating context is
	// cancelled.
	settingRepo RuntimeSettingsStore // retained provider settings store
	notFound    error                // repository missing-key sentinel
	// openAIAPIKeyHealthBreakerCache stores immutable scalar policy entries
	// after successful observations. Get returns copies and independently
	// reloads expired entries without coalescing those reads.
	openAIAPIKeyHealthBreakerCache atomic.Value // atomic API Key breaker observation
}

// OpenAIImagesOAuthUnavailableCooldownSettings sets the duration of a
// provider's OAuth image-unavailability pause.
type OpenAIImagesOAuthUnavailableCooldownSettings struct {
	CooldownMinutes int `json:"cooldown_minutes"` // minutes
}

// OpenAIAPIKeyHealthBreakerSettings configures rolling OpenAI pool API Key
// failure accounting and the resulting cooldown.
type OpenAIAPIKeyHealthBreakerSettings struct {
	// WindowMinutes and FailureThreshold describe the same rolling failure
	// history. Enabled selects whether health handling records that history, and
	// CooldownMinutes supplies the provider hold after its threshold trips.
	Enabled          bool `json:"enabled"`           // failure breaker enabled flag
	WindowMinutes    int  `json:"window_minutes"`    // minutes
	FailureThreshold int  `json:"failure_threshold"` // rolling failure count threshold
	CooldownMinutes  int  `json:"cooldown_minutes"`  // minutes
}

// cachedOpenAIAPIKeyHealthBreakerSettings holds a scalar policy copy with its
// process-local expiration.
type cachedOpenAIAPIKeyHealthBreakerSettings struct {
	settings  OpenAIAPIKeyHealthBreakerSettings // cached scalar breaker policy
	expiresAt time.Time                         // breaker observation expiration instant
}

// NewRuntimeSettings retains a settings repository and its missing-key
// sentinel for provider policy reads. Construction initializes no
// observations; methods with required storage expect a nonnil repository,
// while optional getters provide their own defaults.
func NewRuntimeSettings(repo RuntimeSettingsStore, notFound error) *RuntimeSettings {
	return &RuntimeSettings{settingRepo: repo, notFound: notFound}
}

// GetOllamaCloudUsageSettings returns decoded usage settings or disabled
// defaults for absent storage or a missing key. Other repository failures are
// wrapped, and malformed JSON is reported by the owning decoder.
func (s *RuntimeSettings) GetOllamaCloudUsageSettings(ctx context.Context) (*OllamaCloudUsageSettings, error) {
	defaults := DefaultOllamaCloudUsageSettings()
	if s == nil || s.settingRepo == nil {
		return defaults, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOllamaCloudUsageSettings)
	if err != nil {
		if errors.Is(err, s.notFound) {
			return defaults, nil
		}
		return nil, fmt.Errorf("get Ollama Cloud usage settings: %w", err)
	}
	return DecodeOllamaCloudUsageSettings(raw)
}

// SetOllamaCloudUsageSettings validates and encodes the supplied settings
// before writing their key. Missing storage returns
// ErrOllamaCloudUsageUnavailable; encoding can default or normalize the
// caller's object before either successful persistence or a later validation
// failure.
func (s *RuntimeSettings) SetOllamaCloudUsageSettings(ctx context.Context, settings *OllamaCloudUsageSettings) error {
	if s == nil || s.settingRepo == nil {
		return ErrOllamaCloudUsageUnavailable
	}
	data, err := EncodeOllamaCloudUsageSettings(settings)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, SettingKeyOllamaCloudUsageSettings, data)
}

// DefaultStreamTimeoutSettings returns a disabled policy using temporary
// blocking, five-minute suspension, and three failures within ten minutes.
// Each call allocates a scalar policy that the caller can customize.
func DefaultStreamTimeoutSettings() *StreamTimeoutSettings {
	return &StreamTimeoutSettings{
		Enabled:                false,
		Action:                 StreamTimeoutActionTempUnsched,
		TempUnschedMinutes:     5,
		ThresholdCount:         3,
		ThresholdWindowMinutes: 10,
	}
}

// DefaultOverloadCooldownSettings returns an enabled overload policy using a
// ten-minute provider cooldown. Each call allocates a scalar policy that the
// caller can customize independently of other default reads.
func DefaultOverloadCooldownSettings() *OverloadCooldownSettings {
	return &OverloadCooldownSettings{
		Enabled:         true,
		CooldownMinutes: 10,
	}
}

// DefaultOpenAIImagesOAuthUnavailableCooldownSettings returns a thirty-minute
// OAuth image-unavailability cooldown for absent or unusable stored policies.
// Each call allocates a separate scalar settings object for the caller to
// use.
func DefaultOpenAIImagesOAuthUnavailableCooldownSettings() *OpenAIImagesOAuthUnavailableCooldownSettings {
	return &OpenAIImagesOAuthUnavailableCooldownSettings{CooldownMinutes: OpenAIImagesOAuthUnavailableDefaultCooldownMinutes}
}

// DefaultOpenAIAPIKeyHealthBreakerSettings returns a disabled breaker with a
// two-minute window, ten-failure threshold, and five-minute cooldown. Each
// call allocates a scalar policy that the caller can customize.
func DefaultOpenAIAPIKeyHealthBreakerSettings() *OpenAIAPIKeyHealthBreakerSettings {
	return &OpenAIAPIKeyHealthBreakerSettings{
		Enabled:          false,
		WindowMinutes:    2,
		FailureThreshold: 10,
		CooldownMinutes:  5,
	}
}

// normalizeOpenAIAPIKeyHealthBreakerSettings returns a copied policy bounded
// to supported window, count, and cooldown ranges. Nil input selects
// defaults; scalar copying leaves the supplied policy unchanged while
// retaining its Enabled flag.
func normalizeOpenAIAPIKeyHealthBreakerSettings(settings *OpenAIAPIKeyHealthBreakerSettings) *OpenAIAPIKeyHealthBreakerSettings {
	if settings == nil {
		return DefaultOpenAIAPIKeyHealthBreakerSettings()
	}
	result := *settings
	// Bound successfully decoded legacy or direct-storage policies so rolling
	// failure accounting receives its supported count and minute ranges.
	if result.WindowMinutes < 1 {
		result.WindowMinutes = 1
	} else if result.WindowMinutes > 60 {
		result.WindowMinutes = 60
	}
	if result.FailureThreshold < 1 {
		result.FailureThreshold = 1
	} else if result.FailureThreshold > 10000 {
		result.FailureThreshold = 10000
	}
	if result.CooldownMinutes < 1 {
		result.CooldownMinutes = 1
	} else if result.CooldownMinutes > 60 {
		result.CooldownMinutes = 60
	}
	return &result
}

// GetOverloadCooldownSettings reads overload policy from configured storage,
// defaulting missing keys, empty values, and malformed JSON. Valid decoded
// cooldowns are bounded to one through one hundred twenty minutes; other
// repository errors are returned.
func (s *RuntimeSettings) GetOverloadCooldownSettings(ctx context.Context) (*OverloadCooldownSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOverloadCooldownSettings)
	if err != nil {
		if errors.Is(err, s.notFound) {
			return DefaultOverloadCooldownSettings(), nil
		}
		return nil, fmt.Errorf("get overload cooldown settings: %w", err)
	}
	if value == "" {
		return DefaultOverloadCooldownSettings(), nil
	}

	var settings OverloadCooldownSettings
	// Discard a failed decoder's partial result so malformed policy text cannot
	// supply a mixture of decoded fields and zero-valued cooldown behavior.
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultOverloadCooldownSettings(), nil
	}

	// Valid JSON can still contain older or directly stored durations. Bound
	// them before overload health commands calculate a provider pause deadline.
	if settings.CooldownMinutes < 1 {
		settings.CooldownMinutes = 1
	}
	if settings.CooldownMinutes > 120 {
		settings.CooldownMinutes = 120
	}

	return &settings, nil
}

// SetOverloadCooldownSettings persists a nonnil overload policy after
// validating its duration. Enabled out-of-range values fail; disabled ones
// are replaced with ten minutes on the supplied object before encoding, even
// if storage later fails.
func (s *RuntimeSettings) SetOverloadCooldownSettings(ctx context.Context, settings *OverloadCooldownSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}

	// An inactive policy can be saved with an invalid duration; normalize the
	// same object so re-enabling it later starts with a supported cooldown.
	if settings.CooldownMinutes < 1 || settings.CooldownMinutes > 120 {
		if settings.Enabled {
			return fmt.Errorf("cooldown_minutes must be between 1-120")
		}
		settings.CooldownMinutes = 10
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal overload cooldown settings: %w", err)
	}

	return s.settingRepo.Set(ctx, SettingKeyOverloadCooldownSettings, string(data))
}

// GetRateLimit429CooldownSettings reads the 429 policy, defaulting missing
// keys, empty values, and malformed JSON. Valid decoded seconds are bounded
// to one through seven thousand two hundred; other repository failures are
// returned.
func (s *RuntimeSettings) GetRateLimit429CooldownSettings(ctx context.Context) (*RateLimit429CooldownSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRateLimit429CooldownSettings)
	if err != nil {
		if errors.Is(err, s.notFound) {
			return DefaultRateLimit429CooldownSettings(), nil
		}
		return nil, fmt.Errorf("get 429 cooldown settings: %w", err)
	}
	if value == "" {
		return DefaultRateLimit429CooldownSettings(), nil
	}

	var settings RateLimit429CooldownSettings
	// Discard partially decoded fields on JSON failure so fallback 429 handling
	// receives the complete built-in policy rather than incomplete data.
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultRateLimit429CooldownSettings(), nil
	}

	// Directly stored seconds are bounded before fallback 429 handling uses them
	// to calculate its short provider recovery deadline.
	if settings.CooldownSeconds < 1 {
		settings.CooldownSeconds = 1
	}
	if settings.CooldownSeconds > 7200 {
		settings.CooldownSeconds = 7200
	}

	return &settings, nil
}

// SetRateLimit429CooldownSettings persists a nonnil 429 policy after
// validating its duration. Enabled out-of-range values fail; disabled ones
// become five seconds on the supplied object before encoding, including when
// the later write fails.
func (s *RuntimeSettings) SetRateLimit429CooldownSettings(ctx context.Context, settings *RateLimit429CooldownSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}

	// Normalize inactive policy input on the caller's object before storage; an
	// active invalid duration still fails before the settings key is written.
	if settings.CooldownSeconds < 1 || settings.CooldownSeconds > 7200 {
		if settings.Enabled {
			return fmt.Errorf("cooldown_seconds must be between 1-7200")
		}
		settings.CooldownSeconds = 5
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal 429 cooldown settings: %w", err)
	}

	return s.settingRepo.Set(ctx, SettingKeyRateLimit429CooldownSettings, string(data))
}

// GetOpenAIImagesOAuthUnavailableCooldownSettings reads the OAuth image
// cooldown, selecting thirty minutes for missing, empty, malformed, or
// out-of-range data. A valid stored duration is returned directly; other
// storage failures are wrapped.
func (s *RuntimeSettings) GetOpenAIImagesOAuthUnavailableCooldownSettings(ctx context.Context) (*OpenAIImagesOAuthUnavailableCooldownSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIImagesOAuthUnavailableCooldownSettings)
	if err != nil {
		if errors.Is(err, s.notFound) {
			return DefaultOpenAIImagesOAuthUnavailableCooldownSettings(), nil
		}
		return nil, fmt.Errorf("get OpenAI images OAuth unavailable cooldown settings: %w", err)
	}
	if value == "" {
		return DefaultOpenAIImagesOAuthUnavailableCooldownSettings(), nil
	}

	var settings OpenAIImagesOAuthUnavailableCooldownSettings
	// Unusable image-cooldown JSON or an unsupported duration selects the
	// complete built-in pause, keeping health deadline calculation within its
	// configured supported range.
	if err := json.Unmarshal([]byte(value), &settings); err != nil ||
		settings.CooldownMinutes <= 0 || settings.CooldownMinutes > OpenAIImagesOAuthUnavailableMaxCooldownMinutes {
		return DefaultOpenAIImagesOAuthUnavailableCooldownSettings(), nil
	}
	return &settings, nil
}

// SetOpenAIImagesOAuthUnavailableCooldownSettings saves a nonnil OAuth image
// cooldown from one through one hundred twenty minutes. Invalid input returns
// before encoding or storage, and valid input is encoded without modifying
// the supplied value.
func (s *RuntimeSettings) SetOpenAIImagesOAuthUnavailableCooldownSettings(ctx context.Context, settings *OpenAIImagesOAuthUnavailableCooldownSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}
	// Reject unsupported image-pause durations before writing the settings key.
	if settings.CooldownMinutes <= 0 || settings.CooldownMinutes > OpenAIImagesOAuthUnavailableMaxCooldownMinutes {
		return fmt.Errorf("cooldown_minutes must be between 1-%d", OpenAIImagesOAuthUnavailableMaxCooldownMinutes)
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal OpenAI images OAuth unavailable cooldown settings: %w", err)
	}
	return s.settingRepo.Set(ctx, SettingKeyOpenAIImagesOAuthUnavailableCooldownSettings, string(data))
}

// GetStreamTimeoutSettings reads stream-timeout policy, defaulting missing,
// empty, and malformed settings. Valid decoded timing and count values are
// bounded, unknown actions select temporary blocking, and unrelated
// repository errors are returned.
func (s *RuntimeSettings) GetStreamTimeoutSettings(ctx context.Context) (*StreamTimeoutSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyStreamTimeoutSettings)
	if err != nil {
		if errors.Is(err, s.notFound) {
			return DefaultStreamTimeoutSettings(), nil
		}
		return nil, fmt.Errorf("get stream timeout settings: %w", err)
	}
	if value == "" {
		return DefaultStreamTimeoutSettings(), nil
	}

	var settings StreamTimeoutSettings
	// A JSON error can leave fields partially assigned. Return the complete
	// disabled policy so timeout handling does not combine incomplete controls.
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultStreamTimeoutSettings(), nil
	}

	// Stored stream policies can bypass administrator validation. Bound all
	// count and timing fields before timeout commands use them for suspension
	// and rolling failure history.
	if settings.TempUnschedMinutes < 1 {
		settings.TempUnschedMinutes = 1
	}
	if settings.TempUnschedMinutes > 60 {
		settings.TempUnschedMinutes = 60
	}
	if settings.ThresholdCount < 1 {
		settings.ThresholdCount = 1
	}
	if settings.ThresholdCount > 10 {
		settings.ThresholdCount = 10
	}
	if settings.ThresholdWindowMinutes < 1 {
		settings.ThresholdWindowMinutes = 1
	}
	if settings.ThresholdWindowMinutes > 60 {
		settings.ThresholdWindowMinutes = 60
	}

	// A stored action outside the supported choices selects temporary blocking
	// so timeout handling has a recognized response when the counter trips.
	switch settings.Action {
	case StreamTimeoutActionTempUnsched, StreamTimeoutActionError, StreamTimeoutActionNone:
	default:
		settings.Action = StreamTimeoutActionTempUnsched
	}

	return &settings, nil
}

// SetStreamTimeoutSettings validates a nonnil stream policy's durations,
// count, and action before encoding and storage. Invalid values fail even
// when the policy is disabled; the supplied object remains unchanged.
func (s *RuntimeSettings) SetStreamTimeoutSettings(ctx context.Context, settings *StreamTimeoutSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}

	// Validate suspension and counting ranges together before persistence so
	// timeout commands receive supported timing and failure-history controls.
	if settings.TempUnschedMinutes < 1 || settings.TempUnschedMinutes > 60 {
		return fmt.Errorf("temp_unsched_minutes must be between 1-60")
	}
	if settings.ThresholdCount < 1 || settings.ThresholdCount > 10 {
		return fmt.Errorf("threshold_count must be between 1-10")
	}
	if settings.ThresholdWindowMinutes < 1 || settings.ThresholdWindowMinutes > 60 {
		return fmt.Errorf("threshold_window_minutes must be between 1-60")
	}

	switch settings.Action {
	case StreamTimeoutActionTempUnsched, StreamTimeoutActionError, StreamTimeoutActionNone:
	default:
		return fmt.Errorf("invalid action: %s", settings.Action)
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal stream timeout settings: %w", err)
	}

	return s.settingRepo.Set(ctx, SettingKeyStreamTimeoutSettings, string(data))
}

// GetOpenAI403CooldownSettings decodes 403 policy over built-in defaults, so
// omitted fields inherit those values. Missing, empty, and malformed settings
// return defaults; nonpositive decoded ranges restore defaults and oversized
// ranges are capped.
func (s *RuntimeSettings) GetOpenAI403CooldownSettings(ctx context.Context) (*OpenAI403CooldownSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAI403CooldownSettings)
	if err != nil {
		if errors.Is(err, s.notFound) {
			return DefaultOpenAI403CooldownSettings(), nil
		}
		return nil, fmt.Errorf("get openai 403 cooldown settings: %w", err)
	}
	if value == "" {
		return DefaultOpenAI403CooldownSettings(), nil
	}

	settings := *DefaultOpenAI403CooldownSettings()
	// Omitted fields retain defaults, but a failed decoder discards all partial
	// assignments so the 403 command receives one complete known policy.
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultOpenAI403CooldownSettings(), nil
	}

	// Decode over defaults, then repair nonpositive explicit fields and cap
	// oversized values before the 403 command applies cooldown or escalation.
	if settings.CooldownMinutes < 1 {
		settings.CooldownMinutes = OpenAI403CooldownMinutesDefault
	}
	if settings.CooldownMinutes > 120 {
		settings.CooldownMinutes = 120
	}
	if settings.ThresholdCount < 1 {
		settings.ThresholdCount = OpenAI403DisableThresholdDefault
	}
	if settings.ThresholdCount > 20 {
		settings.ThresholdCount = 20
	}
	if settings.ThresholdWindowMinutes < 1 {
		settings.ThresholdWindowMinutes = OpenAI403CounterWindowMinutesDefault
	}
	if settings.ThresholdWindowMinutes > 1440 {
		settings.ThresholdWindowMinutes = 1440
	}

	return &settings, nil
}

// SetOpenAI403CooldownSettings validates or normalizes a nonnil policy before
// saving it. Cooldown duration is mandatory when Enabled is true; failure
// count and its window are mandatory only when Enabled and
// ErrorOnThresholdEnabled are both true.
//
// Invalid inactive controls receive their built-in defaults on the caller's
// object while the switches retain their supplied values. These shared switch
// conditions mean an active-range validation failure returns before any
// inactive-field normalization can occur. Encoding and persistence follow
// successful range handling, and a storage failure leaves the already
// normalized caller object changed.
func (s *RuntimeSettings) SetOpenAI403CooldownSettings(ctx context.Context, settings *OpenAI403CooldownSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}

	// Enabled selects the cooldown, while escalation also needs its own switch.
	// Normalize inactive fields in place so persisted controls remain usable
	// when later enabled; an active invalid field returns immediately.
	if settings.CooldownMinutes < 1 || settings.CooldownMinutes > 120 {
		if settings.Enabled {
			return fmt.Errorf("cooldown_minutes must be between 1-120")
		}
		settings.CooldownMinutes = OpenAI403CooldownMinutesDefault
	}
	if settings.ThresholdCount < 1 || settings.ThresholdCount > 20 {
		if settings.Enabled && settings.ErrorOnThresholdEnabled {
			return fmt.Errorf("threshold_count must be between 1-20")
		}
		settings.ThresholdCount = OpenAI403DisableThresholdDefault
	}
	if settings.ThresholdWindowMinutes < 1 || settings.ThresholdWindowMinutes > 1440 {
		if settings.Enabled && settings.ErrorOnThresholdEnabled {
			return fmt.Errorf("threshold_window_minutes must be between 1-1440")
		}
		settings.ThresholdWindowMinutes = OpenAI403CounterWindowMinutesDefault
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal openai 403 cooldown settings: %w", err)
	}

	return s.settingRepo.Set(ctx, SettingKeyOpenAI403CooldownSettings, string(data))
}

// GetOpenAIAPIKeyHealthBreakerSettings returns a new scalar policy copy,
// reusing a fresh thirty-second observation. Nil or absent storage selects
// uncached defaults; missing, blank, or malformed values also select
// defaults, while other storage errors return before cache publication.
//
// Expired reads independently decode and normalize storage before publishing.
// Concurrent misses may perform separate calls, and the latest Store supplies
// the visible observation. Callers can change their returned policy without
// changing the cached settings; successful observations measure their
// expiration after the storage read.
func (s *RuntimeSettings) GetOpenAIAPIKeyHealthBreakerSettings(ctx context.Context) (*OpenAIAPIKeyHealthBreakerSettings, error) {
	if s == nil || s.settingRepo == nil {
		return DefaultOpenAIAPIKeyHealthBreakerSettings(), nil
	}
	if cached, ok := s.openAIAPIKeyHealthBreakerCache.Load().(*cachedOpenAIAPIKeyHealthBreakerSettings); ok && cached != nil && time.Now().Before(cached.expiresAt) {
		result := cached.settings
		return &result, nil
	}

	settings := DefaultOpenAIAPIKeyHealthBreakerSettings()
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIAPIKeyHealthBreakerSettings)
	if err != nil && !errors.Is(err, s.notFound) {
		return nil, fmt.Errorf("get OpenAI API key health breaker settings: %w", err)
	}
	if err == nil && strings.TrimSpace(value) != "" {
		var stored OpenAIAPIKeyHealthBreakerSettings
		// Invalid JSON leaves the initialized disabled defaults selected, then
		// caches that policy normally instead of reporting a decoding error.
		if json.Unmarshal([]byte(value), &stored) == nil {
			settings = normalizeOpenAIAPIKeyHealthBreakerSettings(&stored)
		}
	}
	// Publish a scalar copy after the read so callers cannot mutate this
	// observation through either the decoded settings or returned pointer.
	s.openAIAPIKeyHealthBreakerCache.Store(&cachedOpenAIAPIKeyHealthBreakerSettings{
		settings:  *settings,
		expiresAt: time.Now().Add(openAIAPIKeyHealthBreakerSettingsCacheTTL),
	})
	result := *settings
	return &result, nil
}
