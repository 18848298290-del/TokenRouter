package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

var (
	// ConfigProviderSensitiveConfigFields is the authoritative list of config keys that
	// are treated as secrets per provider. Must stay in sync with the frontend
	// definition at frontend/src/components/payment/providerConfig.ts
	// (PROVIDER_CONFIG_FIELDS, fields with sensitive: true).
	//
	// Key matching is case-insensitive. Non-listed keys (e.g. appId, notifyUrl,
	// stripe publishableKey) are returned in plaintext by the admin GET API.
	ConfigProviderSensitiveConfigFields = map[string]map[string]struct{}{
		TypeEasyPay:   {"pkey": {}},
		TypeAlipay:    {"privatekey": {}, "publickey": {}, "alipaypublickey": {}},
		TypeWxpay:     {"privatekey": {}, "apiv3key": {}, "publickey": {}},
		TypeStripe:    {"secretkey": {}, "webhooksecret": {}},
		TypeAirwallex: {"apikey": {}, "webhooksecret": {}},
	}

	// ConfigProviderPendingOrderProtectedConfigFields lists config keys that cannot be
	// changed while the instance has in-progress orders. This includes secrets plus
	// all provider identity fields that are snapshotted into orders or used by
	// webhook/refund verification.
	ConfigProviderPendingOrderProtectedConfigFields = map[string]map[string]struct{}{
		TypeEasyPay:   {"pkey": {}, "pid": {}},
		TypeAlipay:    {"privatekey": {}, "publickey": {}, "alipaypublickey": {}, "appid": {}},
		TypeWxpay:     {"privatekey": {}, "apiv3key": {}, "publickey": {}, "appid": {}, "mpappid": {}, "mchid": {}, "publickeyid": {}, "certserial": {}},
		TypeStripe:    {"secretkey": {}, "webhooksecret": {}, "currency": {}},
		TypeAirwallex: {"clientid": {}, "apikey": {}, "webhooksecret": {}, "apibase": {}, "accountid": {}, "currency": {}},
	}

	ConfigValidProviderKeys = map[string]bool{
		TypeEasyPay: true, TypeAlipay: true, TypeWxpay: true, TypeStripe: true, TypeAirwallex: true,
	}

	ConfigEasyPayCustomMethodCodePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)
)

// ProviderInstanceResponse is the API response for a provider instance.
type ProviderInstanceResponse struct {
	ID              int64             `json:"id"`
	ProviderKey     string            `json:"provider_key"`
	Name            string            `json:"name"`
	Config          map[string]string `json:"config"`
	SupportedTypes  []string          `json:"supported_types"`
	Limits          string            `json:"limits"`
	Enabled         bool              `json:"enabled"`
	RefundEnabled   bool              `json:"refund_enabled"`
	AllowUserRefund bool              `json:"allow_user_refund"`
	SortOrder       int               `json:"sort_order"`
	PaymentMode     string            `json:"payment_mode"`
}

type ConfigEasyPayCustomMethodConfig struct {
	ID                      string                 `json:"id,omitempty"`
	DisplayNameLocalization *locale.Update[string] `json:"displayNameLocalization,omitempty"`
	Type                    string                 `json:"type"`
	UpstreamType            string                 `json:"upstreamType"`
	DisplayName             string                 `json:"displayName"`
}

// ConfigValidateProviderConfig runs the provider's constructor to surface config-level
// errors at save time (e.g. wxpay missing certSerial), instead of only failing
// when an order is created. Returns the structured ApplicationError from the
// constructor so the frontend i18n layer can localize it.
//
// Only validates enabled instances — a disabled instance may be a half-filled
// draft the admin will complete later.
func (s *ConfigService) ConfigValidateProviderConfig(providerKey string, config map[string]string) error {
	_, err := s.runtime.CreateProvider(providerKey, "_validate_", config)
	return err
}

// ListProviderInstancesWithConfig returns provider instances with decrypted config.
func (s *ConfigService) ListProviderInstancesWithConfig(ctx context.Context) ([]ProviderInstanceResponse, error) {
	instances, err := s.store.ListInstances(ctx, InstanceFilter{SortByOrder: true})
	if err != nil {
		return nil, err
	}
	result := make([]ProviderInstanceResponse, 0, len(instances))
	for _, inst := range instances {
		resp := ProviderInstanceResponse{
			ID: inst.ID, ProviderKey: inst.ProviderKey, Name: inst.Name,
			SupportedTypes: ConfigSplitTypes(inst.SupportedTypes), Limits: inst.Limits,
			Enabled: inst.Enabled, RefundEnabled: inst.RefundEnabled, AllowUserRefund: inst.AllowUserRefund,
			SortOrder: inst.SortOrder, PaymentMode: inst.PaymentMode,
		}
		resp.Config = s.ConfigDecryptAndMaskConfig(inst.ProviderKey, inst.Config)
		result = append(result, resp)
	}
	return result, nil
}

// ConfigDecryptAndMaskConfig reads configuration for the admin API and removes provider secrets.
func (s *ConfigService) ConfigDecryptAndMaskConfig(providerKey, stored string) map[string]string {
	cfg := s.ConfigDecryptConfig(stored)
	if cfg == nil {
		return nil
	}
	masked := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if ConfigIsSensitiveProviderConfigField(providerKey, k) {
			continue
		}
		masked[k] = v
	}
	return masked
}

func ConfigIsSensitiveProviderConfigField(providerKey, fieldName string) bool {
	fields, ok := ConfigProviderSensitiveConfigFields[providerKey]
	if !ok {
		return false
	}
	_, found := fields[strings.ToLower(fieldName)]
	return found
}

func ConfigHasPendingOrderProtectedConfigChange(providerKey string, currentConfig, nextConfig map[string]string) bool {
	fields, ok := ConfigProviderPendingOrderProtectedConfigFields[providerKey]
	if !ok {
		return false
	}
	for fieldName := range fields {
		if ConfigProviderConfigFieldValue(currentConfig, fieldName) != ConfigProviderConfigFieldValue(nextConfig, fieldName) {
			return true
		}
	}
	return false
}

func ConfigProviderConfigFieldValue(config map[string]string, fieldName string) string {
	for key, value := range config {
		if strings.EqualFold(key, fieldName) {
			return value
		}
	}
	return ""
}

func (s *ConfigService) ConfigCountPendingOrders(ctx context.Context, providerInstanceID int64) (int, error) {
	return s.store.CountInProgressByProvider(ctx, providerInstanceID)
}

func (s *ConfigService) CreateProviderInstance(ctx context.Context, req CreateProviderInstanceRequest) (*ProviderInstance, error) {
	typesStr := ConfigJoinTypes(req.SupportedTypes)
	if err := ConfigValidateProviderRequest(req.ProviderKey, req.Name, typesStr); err != nil {
		return nil, err
	}
	if req.ProviderKey == TypeEasyPay {
		if err := prepareMethodNames(req.Config, nil); err != nil {
			return nil, err
		}
		if err := ConfigValidateEasyPayCustomMethods(req.Config, typesStr); err != nil {
			return nil, err
		}
	}
	if err := s.ConfigValidateVisibleMethodEnablementConflicts(ctx, 0, req.ProviderKey, typesStr, req.Enabled); err != nil {
		return nil, err
	}
	if req.Enabled {
		if err := s.ConfigValidateProviderConfig(req.ProviderKey, req.Config); err != nil {
			return nil, err
		}
	}
	enc, err := s.ConfigEncryptConfig(req.Config)
	if err != nil {
		return nil, err
	}
	allowUserRefund := req.AllowUserRefund && req.RefundEnabled
	return s.store.CreateInstance(ctx, ProviderInstance{
		ProviderKey:     req.ProviderKey,
		Name:            req.Name,
		Config:          enc,
		SupportedTypes:  typesStr,
		Enabled:         req.Enabled,
		PaymentMode:     req.PaymentMode,
		SortOrder:       req.SortOrder,
		Limits:          req.Limits,
		RefundEnabled:   req.RefundEnabled,
		AllowUserRefund: allowUserRefund,
	})
}

// TestProviderDraft probes EasyPay with an unsaved configuration using a read-only order query.
// The probe order number is not persisted and cannot initiate a charge.
func (s *ConfigService) TestProviderDraft(ctx context.Context, req TestProviderDraftRequest) (*ProviderDraftTestResult, error) {
	providerKey := strings.TrimSpace(req.ProviderKey)
	if providerKey != TypeEasyPay {
		return nil, infraerrors.BadRequest("UNSUPPORTED_PROVIDER_TEST", "payment provider test is not supported")
	}

	config := req.Config
	if config == nil {
		config = map[string]string{}
	}
	if req.InstanceID != nil {
		inst, err := s.store.Instance(ctx, *req.InstanceID)
		if err != nil {
			if s.store.IsNotFound(err) {
				return nil, infraerrors.NotFound("PROVIDER_NOT_FOUND", "payment provider instance not found")
			}
			return nil, fmt.Errorf("load provider instance for test: %w", err)
		}
		if inst.ProviderKey != providerKey {
			return nil, infraerrors.BadRequest("PROVIDER_KEY_MISMATCH", "provider key does not match instance")
		}
		mergedConfig, mergeErr := s.ConfigMergeConfig(ctx, *req.InstanceID, config)
		if mergeErr != nil {
			return nil, mergeErr
		}
		config = mergedConfig
	}

	prov, err := s.runtime.CreateProvider(providerKey, "_draft_test_", config)
	if err != nil {
		return nil, infraerrors.BadRequest("PAYMENT_PROVIDER_TEST_INVALID_CONFIG", "payment provider test configuration is invalid").WithCause(err)
	}
	if _, err := prov.QueryOrder(ctx, s.runtime.NewTradeNo()); err != nil {
		return nil, infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_TEST_FAILED", "payment provider test failed").WithCause(err)
	}
	return &ProviderDraftTestResult{Reachable: true}, nil
}

func ConfigValidateProviderRequest(providerKey, name, supportedTypes string) error {
	if strings.TrimSpace(name) == "" {
		return infraerrors.BadRequest("VALIDATION_ERROR", "provider name is required")
	}
	if !ConfigValidProviderKeys[providerKey] {
		return infraerrors.BadRequest("VALIDATION_ERROR", fmt.Sprintf("invalid provider key: %s", providerKey))
	}
	// Supported types may be empty.
	return nil
}

func ConfigValidateEasyPayCustomMethods(config map[string]string, supportedTypes string) error {
	if config == nil {
		config = map[string]string{}
	}
	raw := strings.TrimSpace(config["customMethods"])
	methods := make([]ConfigEasyPayCustomMethodConfig, 0)
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &methods); err != nil {
			return infraerrors.BadRequest("VALIDATION_ERROR", "customMethods must be a JSON array")
		}
	}

	customTypes := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		method.Type = strings.TrimSpace(method.Type)
		method.UpstreamType = strings.TrimSpace(method.UpstreamType)
		if method.Type == "" || method.UpstreamType == "" {
			return infraerrors.BadRequest("VALIDATION_ERROR", "customMethods upstreamType is required")
		}
		if !ConfigEasyPayCustomMethodCodePattern.MatchString(method.Type) {
			return infraerrors.BadRequest("VALIDATION_ERROR", "customMethods type may only contain lowercase letters, digits, underscores, and hyphens")
		}
		if !ConfigEasyPayCustomMethodCodePattern.MatchString(method.UpstreamType) {
			return infraerrors.BadRequest("VALIDATION_ERROR", "customMethods upstreamType may only contain lowercase letters, digits, underscores, and hyphens")
		}
		if ConfigEasyPayCustomMethodTypeConflictsWithBuiltin(method.Type) {
			return infraerrors.BadRequest("VALIDATION_ERROR", "customMethods type cannot start with alipay or wxpay")
		}
		if _, exists := customTypes[method.Type]; exists {
			return infraerrors.BadRequest("VALIDATION_ERROR", "duplicate customMethods type")
		}
		customTypes[method.Type] = struct{}{}
	}

	for _, supportedType := range ConfigSplitTypes(supportedTypes) {
		supportedType = strings.TrimSpace(supportedType)
		if supportedType == "" || supportedType == TypeAlipay || supportedType == TypeWxpay {
			continue
		}
		if !ConfigEasyPayCustomMethodCodePattern.MatchString(supportedType) {
			return infraerrors.BadRequest("VALIDATION_ERROR", fmt.Sprintf("supported EasyPay custom type %s may only contain lowercase letters, digits, underscores, and hyphens", supportedType))
		}
		if _, exists := customTypes[supportedType]; !exists {
			return infraerrors.BadRequest("VALIDATION_ERROR", fmt.Sprintf("supported EasyPay custom type %s has no customMethods mapping", supportedType))
		}
	}
	return nil
}

func ConfigEasyPayCustomMethodTypeConflictsWithBuiltin(methodType string) bool {
	return strings.HasPrefix(methodType, TypeAlipay) || strings.HasPrefix(methodType, TypeWxpay)
}

// UpdateProviderInstance updates a provider instance by ID (patch semantics).
func (s *ConfigService) UpdateProviderInstance(ctx context.Context, id int64, req UpdateProviderInstanceRequest) (*ProviderInstance, error) {
	current, err := s.store.Instance(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load provider instance: %w", err)
	}
	var pendingOrderCount *int
	getPendingOrderCount := func() (int, error) {
		if pendingOrderCount != nil {
			return *pendingOrderCount, nil
		}
		count, err := s.ConfigCountPendingOrders(ctx, id)
		if err != nil {
			return 0, fmt.Errorf("check pending orders: %w", err)
		}
		pendingOrderCount = &count
		return count, nil
	}
	nextEnabled := current.Enabled
	if req.Enabled != nil {
		nextEnabled = *req.Enabled
	}
	nextSupportedTypes := current.SupportedTypes
	if req.SupportedTypes != nil {
		nextSupportedTypes = ConfigJoinTypes(req.SupportedTypes)
	}
	if err := s.ConfigValidateVisibleMethodEnablementConflicts(ctx, id, current.ProviderKey, nextSupportedTypes, nextEnabled); err != nil {
		return nil, err
	}
	var mergedConfig map[string]string
	if req.Config != nil {
		currentConfig := s.ConfigDecryptConfig(current.Config)
		mergedConfig, err = s.ConfigMergeConfig(ctx, id, req.Config)
		if err != nil {
			return nil, err
		}
		if current.ProviderKey == TypeEasyPay {
			if err := prepareMethodNames(mergedConfig, currentConfig); err != nil {
				return nil, err
			}
		}
		if ConfigHasPendingOrderProtectedConfigChange(current.ProviderKey, currentConfig, mergedConfig) {
			count, err := getPendingOrderCount()
			if err != nil {
				return nil, err
			}
			if count > 0 {
				return nil, infraerrors.Conflict("PENDING_ORDERS", "instance has pending orders").
					WithMetadata(map[string]string{"count": strconv.Itoa(count)})
			}
		}
	}
	if req.Enabled != nil && !*req.Enabled {
		count, err := getPendingOrderCount()
		if err != nil {
			return nil, err
		}
		if count > 0 {
			return nil, infraerrors.Conflict("PENDING_ORDERS", "instance has pending orders").
				WithMetadata(map[string]string{"count": strconv.Itoa(count)})
		}
	}
	configToValidate := mergedConfig
	if configToValidate == nil {
		configToValidate = s.ConfigDecryptConfig(current.Config)
	}
	if current.ProviderKey == TypeEasyPay {
		if err := ConfigValidateEasyPayCustomMethods(configToValidate, nextSupportedTypes); err != nil {
			return nil, err
		}
	}
	// Validate merged config when the instance will end up enabled.
	// This surfaces provider-level errors (e.g. wxpay missing certSerial) at save time,
	// so admins see them in the dialog instead of only when an order is created.
	finalEnabled := current.Enabled
	if req.Enabled != nil {
		finalEnabled = *req.Enabled
	}
	if finalEnabled {
		if err := s.ConfigValidateProviderConfig(current.ProviderKey, configToValidate); err != nil {
			return nil, err
		}
	}
	u := InstancePatch{}
	if req.Config != nil {
		u.ExpectedConfig = &current.Config
	}
	if req.Name != nil {
		u.Name = configPointer(*req.Name)
	}
	if mergedConfig != nil {
		enc, err := s.ConfigEncryptConfig(mergedConfig)
		if err != nil {
			return nil, err
		}
		u.Config = configPointer(enc)
	}
	if req.SupportedTypes != nil {
		// Check pending orders before removing payment types
		count, err := getPendingOrderCount()
		if err != nil {
			return nil, err
		}
		if count > 0 {
			// Load current instance to compare types
			oldTypes := strings.Split(current.SupportedTypes, ",")
			newTypes := req.SupportedTypes
			for _, ot := range oldTypes {
				ot = strings.TrimSpace(ot)
				if ot == "" {
					continue
				}
				found := false
				for _, nt := range newTypes {
					if strings.TrimSpace(nt) == ot {
						found = true
						break
					}
				}
				if !found {
					return nil, infraerrors.Conflict("PENDING_ORDERS", "cannot remove payment types while instance has pending orders").
						WithMetadata(map[string]string{"count": strconv.Itoa(count)})
				}
			}
		}
		u.SupportedTypes = configPointer(ConfigJoinTypes(req.SupportedTypes))
	}
	if req.Enabled != nil {
		u.Enabled = configPointer(*req.Enabled)
	}
	if req.SortOrder != nil {
		u.SortOrder = configPointer(*req.SortOrder)
	}
	if req.Limits != nil {
		u.Limits = configPointer(*req.Limits)
	}
	if req.RefundEnabled != nil {
		u.RefundEnabled = configPointer(*req.RefundEnabled)
		// Cascade: turning off refund_enabled also disables allow_user_refund
		if !*req.RefundEnabled {
			u.AllowUserRefund = configPointer(false)
		}
	}
	if req.AllowUserRefund != nil {
		// Only allow enabling when refund_enabled is (or will be) true
		if *req.AllowUserRefund {
			refundEnabled := false
			if req.RefundEnabled != nil {
				refundEnabled = *req.RefundEnabled
			} else {
				refundEnabled = current.RefundEnabled
			}
			if refundEnabled {
				u.AllowUserRefund = configPointer(true)
			}
		} else {
			u.AllowUserRefund = configPointer(false)
		}
	}
	if req.PaymentMode != nil {
		u.PaymentMode = configPointer(*req.PaymentMode)
	}
	return s.store.UpdateInstance(ctx, id, u)
}

// GetUserRefundEligibleInstanceIDs returns provider instance IDs that allow user refund.
func (s *ConfigService) GetUserRefundEligibleInstanceIDs(ctx context.Context) ([]string, error) {
	instances, err := s.store.ListInstances(ctx, InstanceFilter{UserRefundEligible: true})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(instances))
	for _, inst := range instances {
		ids = append(ids, strconv.FormatInt(inst.ID, 10))
	}
	return ids, nil
}

func (s *ConfigService) ConfigMergeConfig(ctx context.Context, id int64, newConfig map[string]string) (map[string]string, error) {
	inst, err := s.store.Instance(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load existing provider: %w", err)
	}
	existing := s.ConfigDecryptConfig(inst.Config)
	if existing == nil {
		existing = map[string]string{}
	}
	for k, v := range newConfig {
		// Preserve existing secrets when the client submits an empty value
		// (admin UI omits the value to indicate "leave unchanged").
		if v == "" && ConfigIsSensitiveProviderConfigField(inst.ProviderKey, k) {
			continue
		}
		existing[k] = v
	}
	return existing, nil
}

// ConfigDecryptConfig reads provider configuration, logging and returning nil on parse failure.
func (s *ConfigService) ConfigDecryptConfig(stored string) map[string]string {
	config, ok := parseProviderConfig(stored, s.encryptionKey)
	if !ok {
		s.warn("payment provider config unreadable, treating as empty for re-entry",
			"stored_len", len(stored))
	}
	return config
}

func (s *ConfigService) DeleteProviderInstance(ctx context.Context, id int64) error {
	count, err := s.ConfigCountPendingOrders(ctx, id)
	if err != nil {
		return fmt.Errorf("check pending orders: %w", err)
	}
	if count > 0 {
		return infraerrors.Conflict("PENDING_ORDERS",
			fmt.Sprintf("this instance has %d in-progress orders and cannot be deleted", count))
	}
	forcedExpiredCount, err := s.ConfigCountForcedExpiredOrders(ctx, id)
	if err != nil {
		return fmt.Errorf("check force expired orders: %w", err)
	}
	if forcedExpiredCount > 0 {
		return infraerrors.Conflict("FORCED_EXPIRED_ORDERS",
			fmt.Sprintf("this instance has %d force-expired orders and must remain available for late payment recovery", forcedExpiredCount)).
			WithMetadata(map[string]string{"count": strconv.Itoa(forcedExpiredCount)})
	}
	return s.store.DeleteInstance(ctx, id)
}

func (s *ConfigService) ConfigCountForcedExpiredOrders(ctx context.Context, providerInstanceID int64) (int, error) {
	return s.store.CountForcedExpiredByProvider(ctx, providerInstanceID)
}

// ConfigEncryptConfig serializes provider configuration as plaintext JSON.
// ConfigDecryptConfig also accepts legacy AES-GCM ciphertext.
func (s *ConfigService) ConfigEncryptConfig(cfg map[string]string) (string, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	return string(data), nil
}
