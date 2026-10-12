//go:build integration

package billing_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	batchpostgres "github.com/TokenFlux/TokenRouter/internal/batchimage/postgres"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	creativepostgres "github.com/TokenFlux/TokenRouter/internal/creative/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	schedulerpostgres "github.com/TokenFlux/TokenRouter/internal/scheduler/postgres"
)

// subscriptionContractEmptyGroups returns no group details for these entitlement fixtures.
type subscriptionContractEmptyGroups struct{}

// billingUsersForContract reads paying-user details for balance and entitlement tests.
type billingUsersForContract struct{ repository identity.UserRepository }

// committedEntitlementClient returns the shared client and registers user-table cleanup for the test.
func committedEntitlementClient(t *testing.T) *dbent.Client {
	t.Helper()
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), "TRUNCATE users RESTART IDENTITY CASCADE")
		require.NoError(t, err)
	})
	return integrationEntClient
}

// entitlementTx starts a transaction and registers rollback at test cleanup.
func entitlementTx(t *testing.T) *dbent.Tx {
	t.Helper()
	tx, err := integrationEntClient.Tx(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// billingUserForContract copies the user details needed by billing tests.
func billingUserForContract(u *identity.User) *billing.UserSummary {
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

func (subscriptionContractEmptyGroups) GetByIDLite(context.Context, int64) (*billing.SubscriptionPlanGroup, error) {
	return nil, nil
}

// newSettlementFixture wires creative and batch-image task persistence into the settlement store.
func newSettlementFixture(db *sql.DB) *billingpostgres.SettlementStore {
	store := billingpostgres.NewSettlementStore(db, timezone.NewCalendar(time.Local), schedulerpostgres.EnqueueProviderQuotaChangedInTx, billingpostgres.TaskProjectionFactories{
		creative.FundingScope: func(tx *sql.Tx, ref billing.TaskReference) billingpostgres.TaskProjection {
			return creativepostgres.NewFundingParticipant(tx, ref.ID)
		},
		batchimage.FundingScope: func(tx *sql.Tx, ref billing.TaskReference) billingpostgres.TaskProjection {
			return batchpostgres.NewFundingParticipant(tx, ref.ID)
		},
	})
	return store
}

func testEntClient(t *testing.T) *dbent.Client { t.Helper(); return integrationEntClient }

func (r billingUsersForContract) GetByID(ctx context.Context, id int64) (*billing.UserSummary, error) {
	user, err := r.repository.GetByID(ctx, id)
	return billingUserForContract(user), err
}

func uniqueTeamTestEmail(prefix string) string {
	return fmt.Sprintf("team-%s-%s@example.com", prefix, uuid.NewString())
}

func mustCreateUser(t *testing.T, client *dbent.Client, u *identity.User) *identity.User {
	t.Helper()
	ctx := context.Background()

	if u.Email == "" {
		u.Email = "user-" + time.Now().Format(time.RFC3339Nano) + "@example.com"
	}
	if u.PasswordHash == "" {
		u.PasswordHash = "test-password-hash"
	}
	if u.Role == "" {
		u.Role = identity.RoleUser
	}
	if u.Status == "" {
		u.Status = billing.StatusActive
	}
	if u.Concurrency == 0 {
		u.Concurrency = 5
	}

	create := client.User.Create().
		SetEmail(u.Email).
		SetPasswordHash(u.PasswordHash).
		SetRole(u.Role).
		SetStatus(u.Status).
		SetBalance(u.Balance).
		SetConcurrency(u.Concurrency).
		SetUsername(u.Username).
		SetNotes(u.Notes)
	if !u.CreatedAt.IsZero() {
		create.SetCreatedAt(u.CreatedAt)
	}
	if !u.UpdatedAt.IsZero() {
		create.SetUpdatedAt(u.UpdatedAt)
	}

	created, err := create.Save(ctx)
	require.NoError(t, err, "create user")

	u.ID = created.ID
	u.CreatedAt = created.CreatedAt
	u.UpdatedAt = created.UpdatedAt

	if len(u.AllowedGroups) > 0 {
		for _, groupID := range u.AllowedGroups {
			_, err := client.UserAllowedGroup.Create().
				SetUserID(u.ID).
				SetGroupID(groupID).
				Save(ctx)
			require.NoError(t, err, "create user_allowed_groups row")
		}
	}

	return u
}

func mustCreateGroup(t *testing.T, client *dbent.Client, g *routing.Group) *routing.Group {
	t.Helper()
	ctx := context.Background()

	if g.Status == "" {
		g.Status = billing.StatusActive
	}

	create := client.Group.Create().
		SetName(g.Name).
		SetStatus(g.Status).
		SetRateMultiplier(g.RateMultiplier).
		SetIsExclusive(g.IsExclusive).
		SetForceOpenaiFast(g.ForceOpenAIFast)
	if g.Description != "" {
		create.SetDescription(g.Description)
	}
	if !g.CreatedAt.IsZero() {
		create.SetCreatedAt(g.CreatedAt)
	}
	if !g.UpdatedAt.IsZero() {
		create.SetUpdatedAt(g.UpdatedAt)
	}

	created, err := create.Save(ctx)
	require.NoError(t, err, "create group")

	g.ID = created.ID
	g.CreatedAt = created.CreatedAt
	g.UpdatedAt = created.UpdatedAt
	return g
}

func mustCreatePlan(t *testing.T, client *dbent.Client, p *billing.SubscriptionPlan) *billing.SubscriptionPlan {
	t.Helper()
	ctx := context.Background()

	if p.ValidityDays == 0 {
		p.ValidityDays = 30
	}
	if p.ValidityUnit == "" {
		p.ValidityUnit = "day"
	}

	create := client.SubscriptionPlan.Create().
		SetName(p.Name).
		SetDescription(p.Description).
		SetPrice(p.Price).
		SetValidityDays(p.ValidityDays).
		SetValidityUnit(p.ValidityUnit).
		SetFeatures(p.Features).
		SetProductName(p.ProductName).
		SetForSale(p.ForSale).
		SetSortOrder(p.SortOrder)
	if p.OriginalPrice != nil {
		create.SetOriginalPrice(*p.OriginalPrice)
	}
	if p.DailyLimitUSD != nil {
		create.SetDailyLimitUsd(*p.DailyLimitUSD)
	}
	if p.WeeklyLimitUSD != nil {
		create.SetWeeklyLimitUsd(*p.WeeklyLimitUSD)
	}
	if p.MonthlyLimitUSD != nil {
		create.SetMonthlyLimitUsd(*p.MonthlyLimitUSD)
	}
	if p.GroupIDs != nil {
		create.SetGroupIds(p.GroupIDs)
	}
	if !p.CreatedAt.IsZero() {
		create.SetCreatedAt(p.CreatedAt)
	}
	if !p.UpdatedAt.IsZero() {
		create.SetUpdatedAt(p.UpdatedAt)
	}

	created, err := create.Save(ctx)
	require.NoError(t, err, "create subscription plan")
	for _, groupID := range p.GroupIDs {
		var rate any
		if p.GroupRateMultipliers != nil {
			if value, ok := p.GroupRateMultipliers[groupID]; ok && value > 0 {
				rate = value
			}
		}
		_, err := client.ExecContext(ctx, `
			INSERT INTO subscription_plan_groups (plan_id, group_id, rate_multiplier)
			VALUES ($1, $2, $3)
			ON CONFLICT (plan_id, group_id)
			DO UPDATE SET rate_multiplier = EXCLUDED.rate_multiplier
		`, created.ID, groupID, rate)
		require.NoError(t, err, "create subscription plan group mapping")
	}

	p.ID = created.ID
	p.CreatedAt = created.CreatedAt
	p.UpdatedAt = created.UpdatedAt
	return p
}

func mustCreateApiKey(t *testing.T, client *dbent.Client, k *apikey.APIKey) *apikey.APIKey {
	t.Helper()
	ctx := context.Background()

	if k.Status == "" {
		k.Status = billing.StatusActive
	}
	if k.Key == "" {
		k.Key = "sk-" + time.Now().Format("150405.000000")
	}
	if k.Name == "" {
		k.Name = "default"
	}

	create := client.APIKey.Create().
		SetUserID(k.UserID).
		SetKey(k.Key).
		SetName(k.Name).
		SetStatus(k.Status)
	if k.Quota != 0 {
		create.SetQuota(k.Quota)
	}
	if k.QuotaUsed != 0 {
		create.SetQuotaUsed(k.QuotaUsed)
	}
	if k.RateLimit5h != 0 {
		create.SetRateLimit5h(k.RateLimit5h)
	}
	if k.RateLimit1d != 0 {
		create.SetRateLimit1d(k.RateLimit1d)
	}
	if k.RateLimit7d != 0 {
		create.SetRateLimit7d(k.RateLimit7d)
	}
	if k.Usage5h != 0 {
		create.SetUsage5h(k.Usage5h)
	}
	if k.Usage1d != 0 {
		create.SetUsage1d(k.Usage1d)
	}
	if k.Usage7d != 0 {
		create.SetUsage7d(k.Usage7d)
	}
	if k.Window5hStart != nil {
		create.SetWindow5hStart(*k.Window5hStart)
	}
	if k.Window1dStart != nil {
		create.SetWindow1dStart(*k.Window1dStart)
	}
	if k.Window7dStart != nil {
		create.SetWindow7dStart(*k.Window7dStart)
	}
	if k.ExpiresAt != nil {
		create.SetExpiresAt(*k.ExpiresAt)
	}
	if k.GroupID != nil {
		create.SetGroupID(*k.GroupID)
	}
	if k.TeamID != nil {
		create.SetTeamID(*k.TeamID)
	}
	if !k.CreatedAt.IsZero() {
		create.SetCreatedAt(k.CreatedAt)
	}
	if !k.UpdatedAt.IsZero() {
		create.SetUpdatedAt(k.UpdatedAt)
	}

	created, err := create.Save(ctx)
	require.NoError(t, err, "create api key")

	k.ID = created.ID
	k.CreatedAt = created.CreatedAt
	k.UpdatedAt = created.UpdatedAt
	return k
}

func mustCreateSubscription(t *testing.T, client *dbent.Client, s *billing.UserSubscription) *billing.UserSubscription {
	t.Helper()
	ctx := context.Background()

	if s.Status == "" {
		s.Status = billing.SubscriptionStatusActive
	}
	now := time.Now()
	if s.StartsAt.IsZero() {
		s.StartsAt = now.Add(-1 * time.Hour)
	}
	if s.ExpiresAt.IsZero() {
		s.ExpiresAt = now.Add(24 * time.Hour)
	}
	if s.AssignedAt.IsZero() {
		s.AssignedAt = now
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = now
	}

	create := client.UserSubscription.Create().
		SetUserID(s.UserID).
		SetPlanID(s.PlanID).
		SetStartsAt(s.StartsAt).
		SetExpiresAt(s.ExpiresAt).
		SetStatus(s.Status).
		SetAssignedAt(s.AssignedAt).
		SetNotes(s.Notes).
		SetDailyUsageUsd(s.DailyUsageUSD).
		SetWeeklyUsageUsd(s.WeeklyUsageUSD).
		SetMonthlyUsageUsd(s.MonthlyUsageUSD)

	if s.AssignedBy != nil {
		create.SetAssignedBy(*s.AssignedBy)
	}
	if s.DailyWindowStart != nil {
		create.SetDailyWindowStart(*s.DailyWindowStart)
	}
	if s.WeeklyWindowStart != nil {
		create.SetWeeklyWindowStart(*s.WeeklyWindowStart)
	}
	if s.MonthlyWindowStart != nil {
		create.SetMonthlyWindowStart(*s.MonthlyWindowStart)
	}
	if s.DailyLimitUSD != nil {
		create.SetDailyLimitUsd(*s.DailyLimitUSD)
	}
	if s.WeeklyLimitUSD != nil {
		create.SetWeeklyLimitUsd(*s.WeeklyLimitUSD)
	}
	if s.MonthlyLimitUSD != nil {
		create.SetMonthlyLimitUsd(*s.MonthlyLimitUSD)
	}
	if s.SourceOrderID != nil {
		create.SetSourceOrderID(*s.SourceOrderID)
	}
	if !s.CreatedAt.IsZero() {
		create.SetCreatedAt(s.CreatedAt)
	}
	if !s.UpdatedAt.IsZero() {
		create.SetUpdatedAt(s.UpdatedAt)
	}

	created, err := create.Save(ctx)
	require.NoError(t, err, "create user subscription")

	s.ID = created.ID
	s.CreatedAt = created.CreatedAt
	s.UpdatedAt = created.UpdatedAt
	return s
}

func float64Ptr(v float64) *float64 {
	return &v
}
