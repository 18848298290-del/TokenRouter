package payment

import (
	"context"
)

// management_ports.go defines provider instance queries and partial updates
// used by payment configuration management.
// Payment instances retain credentials and merchant identity needed by orders,
// so management checks unfinished and force-expired references before
// deletion. Storage filters and optional replacement fields express the
// requested query or update, while ConfigService applies the business checks
// before persistence.

// InstanceFilter selects payment instances for management, selection, and
// historical lookup.
type InstanceFilter struct {
	EnabledOnly bool   // enabled instance filter
	ProviderKey string // exact provider key filter
	SortByOrder bool   // configured sort-order selection
	// Limit bounds returned rows when positive. A nonpositive value leaves the
	// instance query unrestricted, independently of whether configured sort order
	// is requested.
	Limit int
	// UserRefundEligible requires both refund switches and selects instance IDs
	// for eligibility responses. Other instance fields in those returned values
	// keep their zero values.
	UserRefundEligible bool
	// ExcludeID removes that instance from results when positive. Zero or a
	// negative value adds no ID-exclusion predicate to the persisted instance
	// query.
	ExcludeID int64
}

// InstancePatch carries optional instance changes and an expected
// configuration value.
type InstancePatch struct {
	// ExpectedConfig requires the saved config string to match during an instance
	// update. With a supplied expectation, a missing or changed row becomes the
	// localization conflict error.
	ExpectedConfig *string
	// Name, Config, SupportedTypes, Enabled, SortOrder, Limits, RefundEnabled,
	// AllowUserRefund, and PaymentMode are independent optional replacements. Nil
	// retains the corresponding persisted value; a supplied pointer stages its
	// value, including empty text or false.
	Name            *string // replacement instance display name
	Config          *string // replacement serialized provider configuration
	SupportedTypes  *string // replacement comma-separated payment methods
	Enabled         *bool   // replacement instance availability flag
	SortOrder       *int    // replacement configured sort order
	Limits          *string // replacement channel limits JSON
	RefundEnabled   *bool   // replacement administrative refund flag
	AllowUserRefund *bool   // replacement user refund flag
	PaymentMode     *string // replacement checkout display mode
}

// ConfigurationStore persists payment instances and reads orders that
// constrain their management.
type ConfigurationStore interface {
	// ListInstances returns instances matching the supplied independent filters.
	// UserRefundEligible selects IDs after checking both refund flags; no
	// implicit enabled filter or sort order is added for other callers.
	ListInstances(context.Context, InstanceFilter) ([]*ProviderInstance, error)
	// Instance reads the persisted payment instance by its local ID.
	Instance(context.Context, int64) (*ProviderInstance, error)
	// CreateInstance persists the supplied payment instance and returns its
	// stored value.
	CreateInstance(context.Context, ProviderInstance) (*ProviderInstance, error)
	// UpdateInstance applies the supplied optional field replacements to the
	// addressed instance. ExpectedConfig adds a compare condition and maps no
	// matching row to a localization conflict; management validation runs before
	// this call.
	UpdateInstance(context.Context, int64, InstancePatch) (*ProviderInstance, error)
	// DeleteInstance physically removes the addressed payment instance from
	// storage. ConfigService checks in-progress and force-expired references
	// before calling it; the persistence method itself delegates deletion of that
	// row.
	DeleteInstance(context.Context, int64) error
	// CountInProgressByProvider counts orders bound to the supplied instance in
	// InProgressOrderStatuses. Management uses this separate read before changing
	// protected configuration, disabling an instance, or deleting its persisted
	// row.
	CountInProgressByProvider(context.Context, int64) (int, error)
	// CountInProgressByPlan counts orders for the supplied plan in
	// InProgressOrderStatuses. Billing plan management uses the count to reject
	// deletion while those payment stages still need the plan reference.
	CountInProgressByPlan(context.Context, int64) (int, error)
	// CountForcedExpiredByProvider counts force-expiration audit rows for
	// currently expired orders bound to the instance. ConfigService retains that
	// instance for late recovery until those orders leave the expired state.
	CountForcedExpiredByProvider(context.Context, int64) (int, error)
	// IsNotFound reports whether the storage error identifies an absent entity.
	IsNotFound(error) bool
}

// InProgressOrderStatuses returns a new list of pending, processing, paid, and
// recharging states. Instance management and plan deletion count these states;
// callers can modify the returned slice independently.
func InProgressOrderStatuses() []string {
	return []string{OrderStatusPending, OrderStatusProcessing, OrderStatusPaid, OrderStatusRecharging}
}
