package routing

import (
	"errors"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

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

	InvalidGroupAvailabilityProbeConfigReason = "INVALID_AVAILABILITY_PROBE_CONFIG"
)

// NormalizeGroupAvailabilityProbeConfig validates and normalizes active probe settings.
// Disabled probes discard other fields so unused model and prompt values are not persisted.
func NormalizeGroupAvailabilityProbeConfig(cfg GroupAvailabilityProbeConfig) (GroupAvailabilityProbeConfig, error) {
	if !cfg.Enabled {
		return GroupAvailabilityProbeConfig{}, nil
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

	// A pointer distinguishes a missing legacy field from an explicit zero-retry setting.
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

// NormalizeGroupAvailabilityProbeConfigForAdminWrite returns invalid probe settings as a bad-request error.
func NormalizeGroupAvailabilityProbeConfigForAdminWrite(cfg GroupAvailabilityProbeConfig) (GroupAvailabilityProbeConfig, error) {
	normalized, err := NormalizeGroupAvailabilityProbeConfig(cfg)
	if err != nil {
		return GroupAvailabilityProbeConfig{}, infraerrors.BadRequest(InvalidGroupAvailabilityProbeConfigReason, err.Error())
	}
	return normalized, nil
}

// HasInvalidHTTPHeaderValueByte detects control bytes that would make a stored User-Agent invalid.
func HasInvalidHTTPHeaderValueByte(value string) bool {
	for i := range len(value) {
		b := value[i]
		if b < 0x20 || b == 0x7f {
			return true
		}
	}
	return false
}
