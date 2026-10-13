package app

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// Announcement eligibility reads a narrow user snapshot, while billing needs
// balances, entitlements, and notification data. These adapters select fields
// from identity users; billing copies extra notification-email entries but
// retains group lists and time pointers supplied by the identity record.

// announcementUsers adapts identity user storage for announcement eligibility
// and lookup.
type announcementUsers struct {
	Repository identity.UserRepository // shared identity repository
}

// billingIdentityUsers adapts identity user reads for billing entitlement
// checks.
type billingIdentityUsers struct {
	Repository identity.UserRepository // shared identity repository
}

// provideAnnouncementUsers binds the shared identity store for announcement
// user queries.
func provideAnnouncementUsers(users *identitypostgres.UserStore) site.UserReader {
	return &announcementUsers{users}
}

// GetByID returns announcement identity and balance data for a stored user.
// Missing users remain nil, and repository errors return before any snapshot is
// created for announcement eligibility.
func (a *announcementUsers) GetByID(ctx context.Context, id int64) (*site.UserSnapshot, error) {
	u, e := a.Repository.GetByID(ctx, id)
	if e != nil || u == nil {
		return nil, e
	}
	return &site.UserSnapshot{ID: u.ID, Email: u.Email, Username: u.Username, Balance: u.Balance}, nil
}

// ListWithFilters queries identity users by the announcement search field and
// pagination. It returns narrowed user snapshots and the repository page,
// retaining that page when the repository returns an error.
func (a *announcementUsers) ListWithFilters(ctx context.Context, p pagination.PaginationParams, f site.UserListFilters) ([]site.UserSnapshot, *pagination.PaginationResult, error) {
	users, page, e := a.Repository.ListWithFilters(ctx, p, identity.UserListFilters{Search: f.Search})
	if e != nil {
		return nil, page, e
	}
	out := make([]site.UserSnapshot, len(users))
	for i, u := range users {
		out[i] = site.UserSnapshot{ID: u.ID, Email: u.Email, Username: u.Username, Balance: u.Balance}
	}
	return out, page, nil
}

// GetByID converts the identity user into a billing summary and retains the
// read error.
func (b billingIdentityUsers) GetByID(ctx context.Context, id int64) (*billing.UserSummary, error) {
	u, e := b.Repository.GetByID(ctx, id)
	return billingIdentitySummary(u), e
}

// billingIdentitySummary returns nil for an absent user or a billing field
// snapshot. Extra notification emails receive an outer slice copy;
// AllowedGroups, DisabledPublicGroups, and the selected time pointers remain
// shared with u.
func billingIdentitySummary(u *identity.User) *billing.UserSummary {
	if u == nil {
		return nil
	}
	out := &billing.UserSummary{
		ID:                         u.ID,
		Email:                      u.Email,
		Username:                   u.Username,
		Role:                       u.Role,
		Balance:                    u.Balance,
		FrozenBalance:              u.FrozenBalance,
		Concurrency:                u.Concurrency,
		Status:                     u.Status,
		AllowedGroups:              u.AllowedGroups,
		DisabledPublicGroups:       u.DisabledPublicGroups,
		LastActiveAt:               u.LastActiveAt,
		CreatedAt:                  u.CreatedAt,
		UpdatedAt:                  u.UpdatedAt,
		BalanceNotifyEnabled:       u.BalanceNotifyEnabled,
		BalanceNotifyThresholdType: u.BalanceNotifyThresholdType,
		BalanceNotifyThreshold:     u.BalanceNotifyThreshold,
		TotalRecharged:             u.TotalRecharged,
		RPMLimit:                   u.RPMLimit,
		APIKeyLimit:                u.APIKeyLimit,
		DeletedAt:                  u.DeletedAt,
	}
	if u.BalanceNotifyExtraEmails != nil {
		out.BalanceNotifyExtraEmails = make([]billing.NotifyEmailSummary, len(u.BalanceNotifyExtraEmails))
		copy(out.BalanceNotifyExtraEmails, u.BalanceNotifyExtraEmails)
	}
	return out
}
