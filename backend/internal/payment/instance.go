package payment

import "time"

// instance.go defines persisted payment instance configuration and the
// selection and refund flags read by payment services.
// New-order selection excludes disabled payment instances. Historical
// recovery can still need a disabled instance's recorded credentials and
// merchant identity, while current configuration writers store plaintext
// JSON and readers retain support for legacy encrypted settings.

// ProviderInstance retains persisted channel configuration, selection options,
// and refund permissions.
type ProviderInstance struct {
	ID          int64  `json:"id,omitempty"`           // local instance identifier
	ProviderKey string `json:"provider_key,omitempty"` // provider implementation key
	Name        string `json:"name,omitempty"`         // administrator instance label
	// Config contains serialized provider settings. ConfigEncryptConfig
	// currently writes plaintext JSON; the reader also accepts legacy
	// AES-GCM text when the deployment key can decrypt it.
	Config string `json:"config,omitempty"`
	// SupportedTypes selects methods for this instance using comma-delimited
	// text. InstanceSupportsType treats an exactly empty value as unrestricted
	// within its caller's eligible provider selection and normalizes supported
	// visible method aliases.
	SupportedTypes string `json:"supported_types,omitempty"` // comma-delimited selected methods
	// Enabled filters new checkout selection. ProviderBindings can still
	// resolve an existing order from a disabled instance because its
	// historical channel identity remains needed for recovery.
	Enabled     bool   `json:"enabled,omitempty"`      // new-order selection flag
	PaymentMode string `json:"payment_mode,omitempty"` // configured checkout mode
	SortOrder   int    `json:"sort_order,omitempty"`   // instance display selection order
	// Limits stores per-method JSON channel limits.
	// ConfigPcInstanceTypeLimits treats empty or unreadable JSON as absent
	// configuration when calculating available-method ranges.
	Limits string `json:"limits,omitempty"` // JSON channel limits
	// RefundEnabled and AllowUserRefund govern channel and user refund
	// permissions together. ConfigService couples user permission to enabled
	// channel refunds, and refund preparation rechecks the resolved instance
	// flags before proceeding.
	RefundEnabled   bool      `json:"refund_enabled,omitempty"`    // channel refund permission
	AllowUserRefund bool      `json:"allow_user_refund,omitempty"` // user self-refund permission
	CreatedAt       time.Time `json:"created_at,omitempty"`        // instance creation time
	UpdatedAt       time.Time `json:"updated_at,omitempty"`        // latest instance update
}
