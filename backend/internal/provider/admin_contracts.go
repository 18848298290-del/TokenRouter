package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// AdminStore supplies provider reads, configuration writes, health updates, and deletion for administration.
type AdminStore interface {
	UpdateExtra(context.Context, int64, map[string]any) error
	BulkUpdate(context.Context, []int64, ProviderBulkUpdate) (int64, error)
	RevertProxyFallback(context.Context, int64) error
	Update(context.Context, *Record) error
	FindByExtraField(context.Context, string, any) ([]Record, error)
	Create(context.Context, *Record) error
	BindGroups(context.Context, int64, []int64) error
	ListByGroup(context.Context, int64) ([]Record, error)
	GetByID(context.Context, int64) (*Record, error)
	GetByIDs(context.Context, []int64) ([]*Record, error)
	ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, string, int64, string) ([]Record, *pagination.PaginationResult, error)
	ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Record, error)
	ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]Record, error)
	ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]Record, error)
	ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Record, error)
	ListSchedulableUngroupedByPlatform(context.Context, string) ([]Record, error)
	ListShadowsByParent(context.Context, int64) ([]*Record, error)
	Delete(context.Context, int64) error
	ClearError(context.Context, int64) error
	ClearRateLimit(context.Context, int64) error
	ClearAntigravityQuotaScopes(context.Context, int64) error
	ClearModelRateLimits(context.Context, int64) error
	ClearTempUnschedulable(context.Context, int64) error
	SetError(context.Context, int64, string) error
	SetSchedulable(context.Context, int64, bool) error
}

type ProviderQuotaResetter interface {
	ResetQuotaUsedAndClearRateLimitCooldown(context.Context, int64) error
}

type (
	RuntimeUnblocker interface{ ClearProviderSchedulingBlock(int64) }
	AdminOptions     struct {
		ShadowModels   func() map[string]any
		Duplicates     DuplicateStore
		Groups         AdminGroups
		Proxies        PrivacyProxyReader
		Creation       CreationOptions
		Credentials    CreateCredentialHooks
		Background     func(string, func()) bool
		Error          func(string, ...any)
		Privacy        *PrivacyService
		Quotas         ProviderQuotaResetter
		RuntimeBlocker RuntimeUnblocker
	}
)

// Admin manages providers through injected storage, quota, and runtime-block interfaces.
type Admin struct {
	providerRepo AdminStore
	options      AdminOptions
}

type GroupReference struct {
	RequireOAuthOnly bool
	ID               int64
	Name             string
}

type AdminGroups interface {
	ActiveGroups(context.Context, string) ([]GroupReference, error)
	ValidateGroups(context.Context, []int64) error
	GetGroup(context.Context, int64) (*GroupReference, error)
}

type CreateCredentialHooks struct {
	// Editing checks the Qoder site before validating its PAT.
	Site         func(*Record) (string, error)
	ValidateEdit func(context.Context, *Record, bool) error
	Prepare      func(*Record)
	Validate     func(context.Context, *Record) error
}

// DuplicateStore commits a copied provider and its group memberships together.
type DuplicateStore interface {
	CreateWithProviderGroups(context.Context, *Record, []GroupMembership) error
}

// ShadowProxyStore supplies reads and writes for propagating a parent's proxy to its shadows.
type ShadowProxyStore interface {
	ListShadowsByParent(context.Context, int64) ([]*Record, error)
	Update(context.Context, *Record) error
}

func NewAdmin(store AdminStore, options AdminOptions) *Admin {
	return &Admin{providerRepo: store, options: options}
}
