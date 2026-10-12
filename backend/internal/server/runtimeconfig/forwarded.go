package runtimeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip/policy"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Storage keys for client IP settings and their compatibility migration marker.
const (
	SettingKeyForwardedClientIPModeV2   = "forwarded_client_ip_mode_v2_migrated"
	SettingKeyAPIKeyACLTrustForwardedIP = "api_key_acl_trust_forwarded_ip"
	SettingKeyForwardedClientIPHeaders  = "forwarded_client_ip_headers"
)

// ForwardedSettingsRepository reads client IP settings and persists migration updates.
type ForwardedSettingsRepository interface {
	GetMultiple(context.Context, []string) (map[string]string, error)
	SetMultiple(context.Context, map[string]string) error
}

// ForwardedSettingsOptions contains initial client IP settings and runtime publication callbacks.
type ForwardedSettingsOptions struct {
	InitialTrust             bool
	TrustedProxiesConfigured bool
	Headers                  func() []string
	Publish                  func(bool, []string)
}

// ForwardedSettings migrates persisted client IP settings and publishes them to the runtime.
type ForwardedSettings struct {
	settingRepo ForwardedSettingsRepository
	options     ForwardedSettingsOptions
}

// ForwardedInput contains the editable client IP settings.
type ForwardedInput struct {
	APIKeyACLTrustForwardedIP bool     `json:"api_key_acl_trust_forwarded_ip"`
	ForwardedClientIPHeaders  []string `json:"forwarded_client_ip_headers"`
}

// NewForwardedSettings creates a settings loader; app calls LoadForwardedClientIPSettings during startup.
func NewForwardedSettings(repo ForwardedSettingsRepository, options ForwardedSettingsOptions) *ForwardedSettings {
	return &ForwardedSettings{settingRepo: repo, options: options}
}

// PrepareForwardedSettings validates header names and encodes values for persistence.
func PrepareForwardedSettings(value ForwardedInput) (ForwardedInput, map[string]string, error) {
	headers, err := policy.NormalizeForwardedClientIPHeaders(value.ForwardedClientIPHeaders)
	if err != nil {
		return value, nil, apperror.BadRequest("INVALID_FORWARDED_CLIENT_IP_HEADERS", err.Error())
	}
	value.ForwardedClientIPHeaders = headers
	raw, err := json.Marshal(headers)
	if err != nil {
		return value, nil, fmt.Errorf("marshal forwarded client IP headers: %w", err)
	}
	return value, map[string]string{SettingKeyAPIKeyACLTrustForwardedIP: strconv.FormatBool(value.APIKeyACLTrustForwardedIP), SettingKeyForwardedClientIPHeaders: string(raw)}, nil
}

// Apply publishes the supplied settings to the runtime.
func (s *ForwardedSettings) Apply(value ForwardedInput) {
	s.options.Publish(value.APIKeyACLTrustForwardedIP, value.ForwardedClientIPHeaders)
}

// SettingsParticipant prepares client IP values for the shared settings write.
func SettingsParticipant() settings.Participant {
	return settings.Participant{Module: "server", Fields: []string{"api_key_acl_trust_forwarded_ip", "forwarded_client_ip_headers"}, Keys: []string{SettingKeyAPIKeyACLTrustForwardedIP, SettingKeyForwardedClientIPHeaders}, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
		if len(input) == 0 {
			return settings.PreparedChange{}, nil
		}
		var value ForwardedInput
		raw, err := json.Marshal(input)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		if err = json.Unmarshal(raw, &value); err != nil {
			return settings.PreparedChange{}, err
		}
		_, values, err := PrepareForwardedSettings(value)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		for key := range values {
			if _, ok := input[key]; !ok {
				delete(values, key)
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}

// ParseForwardedHeaders rejects malformed JSON, null, and invalid header names.
func ParseForwardedHeaders(value string) ([]string, error) {
	var headers []string
	if err := json.Unmarshal([]byte(value), &headers); err != nil {
		return nil, fmt.Errorf("parse forwarded_client_ip_headers: %w", err)
	}
	if headers == nil {
		return nil, fmt.Errorf("parse forwarded_client_ip_headers: value must be a JSON array")
	}
	normalized, err := policy.NormalizeForwardedClientIPHeaders(headers)
	if err != nil {
		return nil, fmt.Errorf("parse forwarded_client_ip_headers: %w", err)
	}
	return normalized, nil
}

// LoadForwardedClientIPSettings loads and migrates settings, disabling forwarded trust on read or header parse errors.
func (s *ForwardedSettings) LoadForwardedClientIPSettings(ctx context.Context) error {
	if s == nil || s.settingRepo == nil {
		return nil
	}

	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyAPIKeyACLTrustForwardedIP,
		SettingKeyForwardedClientIPHeaders,
		SettingKeyForwardedClientIPModeV2,
	})
	if err != nil {
		s.options.Publish(false, nil)
		return fmt.Errorf("get forwarded client ip settings: %w", err)
	}

	enabled := s.options.InitialTrust
	headers := s.options.Headers()
	storedValue, hasStoredValue := values[SettingKeyAPIKeyACLTrustForwardedIP]
	if hasStoredValue {
		enabled = storedValue == "true"
	}

	var headersErr error
	if storedHeaders, ok := values[SettingKeyForwardedClientIPHeaders]; ok {
		headers, headersErr = ParseForwardedHeaders(storedHeaders)
		if headersErr != nil {
			enabled = false
			headers = []string{}
			headersErr = fmt.Errorf("load forwarded client ip headers: %w", headersErr)
		}
	}

	updates := make(map[string]string)
	if _, hasStoredHeaders := values[SettingKeyForwardedClientIPHeaders]; !hasStoredHeaders {
		headersJSON, marshalErr := json.Marshal(headers)
		if marshalErr != nil {
			headers = []string{}
			headersErr = errors.Join(headersErr, fmt.Errorf("marshal forwarded client ip headers: %w", marshalErr))
			headersJSON = []byte("[]")
		}
		updates[SettingKeyForwardedClientIPHeaders] = string(headersJSON)
	}
	if values[SettingKeyForwardedClientIPModeV2] != "true" {
		updates[SettingKeyForwardedClientIPModeV2] = "true"
		// Older installations stored false by default; restore compatibility only when no trusted proxy policy was configured.
		if headersErr == nil && hasStoredValue && !enabled && !s.options.TrustedProxiesConfigured {
			enabled = true
			updates[SettingKeyAPIKeyACLTrustForwardedIP] = "true"
		}
	}
	if len(updates) > 0 {
		if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
			s.options.Publish(enabled, headers)
			return errors.Join(headersErr, fmt.Errorf("migrate forwarded client ip setting: %w", err))
		}
	}

	s.options.Publish(enabled, headers)
	return headersErr
}

// ReadForwardedSettings overlays persisted values and disables forwarded trust if header parsing fails.
func ReadForwardedSettings(settings map[string]string, prior ForwardedInput) ForwardedInput {
	apiKeyACLTrustForwardedIP := prior.APIKeyACLTrustForwardedIP
	forwardedClientIPHeaders := prior.ForwardedClientIPHeaders
	if value, ok := settings[SettingKeyAPIKeyACLTrustForwardedIP]; ok {
		apiKeyACLTrustForwardedIP = value == "true"
	}
	if value, ok := settings[SettingKeyForwardedClientIPHeaders]; ok {
		parsed, err := ParseForwardedHeaders(value)
		if err != nil {
			slog.Error("invalid persisted forwarded client IP headers; forwarded trust disabled", "error", err)
			apiKeyACLTrustForwardedIP = false
			forwardedClientIPHeaders = []string{}
		} else {
			forwardedClientIPHeaders = parsed
		}
	}

	return ForwardedInput{APIKeyACLTrustForwardedIP: apiKeyACLTrustForwardedIP, ForwardedClientIPHeaders: forwardedClientIPHeaders}
}
