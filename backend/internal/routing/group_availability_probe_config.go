package routing

import (
	"errors"
	"slices"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// group_availability_probe_config.go validates enabled probe settings and
// normalizes their default values.
// Enabled group probes require a model, prompt, a User-Agent without control
// bytes, and bounded scheduling values. Disabled configuration is discarded,
// while a missing legacy retry count becomes three additional attempts and an
// explicit zero retains a single initial attempt. Explicit protocols come from
// the test capability catalog and must remain admitted by the complete group.

// Probe defaults and bounds constrain stored intervals, attempt timeouts, retry
// counts, and User-Agent byte length before administrative configuration is
// persisted.
const (
	DefaultGroupAvailabilityProbeIntervalMinutes = 30
	DefaultGroupAvailabilityProbeTimeoutSeconds  = 30
	DefaultGroupAvailabilityProbeMaxRetries      = 3
	MinGroupAvailabilityProbeIntervalMinutes     = 1
	MaxGroupAvailabilityProbeIntervalMinutes     = 1440
	MinGroupAvailabilityProbeTimeoutSeconds      = 5
	MaxGroupAvailabilityProbeTimeoutSeconds      = 120
	MinGroupAvailabilityProbeMaxRetries          = 0
	MaxGroupAvailabilityProbeMaxRetries          = 10
	MaxGroupAvailabilityProbeUserAgentLength     = 512

	// InvalidGroupAvailabilityProbeConfigReason gives creation and update errors
	// the same HTTP bad-request classification for rejected stored probe settings.
	InvalidGroupAvailabilityProbeConfigReason = "INVALID_AVAILABILITY_PROBE_CONFIG"
)

// NormalizeGroupAvailabilityProbeConfig trims and validates a complete probe
// configuration. Disabled probes return a zero value; enabled probes require
// nonblank model and prompt, apply interval and timeout defaults, and preserve
// explicit zero retries through separate pointer storage. User-Agent control
// checks apply after trimming, so surrounding spaces and tabs are removed
// before validation. A nonempty protocol must belong to the group probe catalog.
func NormalizeGroupAvailabilityProbeConfig(cfg GroupAvailabilityProbeConfig) (GroupAvailabilityProbeConfig, error) {
	if !cfg.Enabled {
		return GroupAvailabilityProbeConfig{}, nil
	}
	if cfg.Protocol != "" && !slices.Contains(provider.GroupProbeProtocols(), cfg.Protocol) {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.protocol is not supported by group probes")
	}

	cfg.ModelID = strings.TrimSpace(cfg.ModelID)
	cfg.Prompt = strings.TrimSpace(cfg.Prompt)
	cfg.UserAgent = strings.TrimSpace(cfg.UserAgent)
	if cfg.ModelID == "" {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.model_id is required when enabled")
	}
	if cfg.Prompt == "" {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.prompt is required when enabled")
	}
	// Bound the stored User-Agent by bytes, matching the header representation
	// used when the provider test service constructs its probe request.
	if len(cfg.UserAgent) > MaxGroupAvailabilityProbeUserAgentLength {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.user_agent is too long")
	}
	if HasInvalidHTTPHeaderValueByte(cfg.UserAgent) {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.user_agent contains invalid header characters")
	}

	if cfg.IntervalMinutes == 0 {
		cfg.IntervalMinutes = DefaultGroupAvailabilityProbeIntervalMinutes
	}
	if cfg.IntervalMinutes < MinGroupAvailabilityProbeIntervalMinutes || cfg.IntervalMinutes > MaxGroupAvailabilityProbeIntervalMinutes {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.interval_minutes must be between 1 and 1440")
	}

	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = DefaultGroupAvailabilityProbeTimeoutSeconds
	}
	if cfg.TimeoutSeconds < MinGroupAvailabilityProbeTimeoutSeconds || cfg.TimeoutSeconds > MaxGroupAvailabilityProbeTimeoutSeconds {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.timeout_seconds must be between 5 and 120")
	}

	// Missing legacy retry input keeps the default retry allowance, while an
	// explicit pointer to zero requests one attempt and must survive normalization
	// as present.
	maxRetries := DefaultGroupAvailabilityProbeMaxRetries
	if cfg.MaxRetries != nil {
		maxRetries = *cfg.MaxRetries
	}
	if maxRetries < MinGroupAvailabilityProbeMaxRetries || maxRetries > MaxGroupAvailabilityProbeMaxRetries {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.max_retries must be between 0 and 10")
	}
	cfg.MaxRetries = &maxRetries

	return cfg, nil
}

// NormalizeGroupAvailabilityProbeConfigForAdminWrite converts normalization
// failures into the shared administrative bad-request reason. It returns the
// same accepted configuration as the core validator, allowing create and update
// handlers to report invalid probe settings consistently.
func NormalizeGroupAvailabilityProbeConfigForAdminWrite(cfg GroupAvailabilityProbeConfig) (GroupAvailabilityProbeConfig, error) {
	normalized, err := NormalizeGroupAvailabilityProbeConfig(cfg)
	if err != nil {
		return GroupAvailabilityProbeConfig{}, infraerrors.BadRequest(InvalidGroupAvailabilityProbeConfigReason, err.Error())
	}
	return normalized, nil
}

// ValidateGroupProbeProtocol checks an enabled probe against the group's entry policy.
// Call after merging and normalizing the complete group update, including when
// the update changes allowed protocols without supplying new probe settings.
func ValidateGroupProbeProtocol(group *Group) error {
	cfg := group.AvailabilityProbeConfig
	if !cfg.Enabled || cfg.Protocol == "" {
		return nil
	}
	if !slices.Contains(provider.GroupProbeProtocols(), cfg.Protocol) || !group.AllowsClientProtocol(cfg.Protocol) {
		return infraerrors.BadRequest(InvalidGroupAvailabilityProbeConfigReason,
			"availability_probe_config.protocol must be allowed by the group and supported by group probes")
	}
	return nil
}

// HasInvalidHTTPHeaderValueByte reports whether the probe User-Agent contains a
// byte below 0x20 or the DEL byte. This stored-probe check rejects tabs along
// with other controls, while bytes above ASCII DEL pass through.
func HasInvalidHTTPHeaderValueByte(value string) bool {
	for i := range len(value) {
		b := value[i]
		if b < 0x20 || b == 0x7f {
			return true
		}
	}
	return false
}
