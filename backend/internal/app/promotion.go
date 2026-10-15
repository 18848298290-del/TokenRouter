package app

import (
	"context"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	promotionhttp "github.com/TokenFlux/TokenRouter/internal/promotion/httpapi"
	promotionpostgres "github.com/TokenFlux/TokenRouter/internal/promotion/postgres"
)

// promotion.go assembles affiliate and promo services and connects them to
// billing, identity, and administration. Affiliate transfers and promo grants
// receive billing writers from their own storage transaction. Identity consumes
// the affiliate operations and promo preview through narrow adapters, while
// background promo effects use application tracking and administrator recipient
// search returns one bounded page.

// identityPromotion adapts affiliate operations for identity registration and
// profile consumers.
type identityPromotion struct {
	Service *promotion.AffiliateService // affiliate registration operations
}

// providePromotionAffiliateStore binds affiliate persistence to
// same-transaction billing balance transfer.
func providePromotionAffiliateStore(client *dbent.Client) promotion.AffiliateRepository {
	return promotionpostgres.NewAffiliateRepository(client, func(tx *dbent.Tx) promotionpostgres.TransferBalance { return billingpostgres.BalanceInTx(tx) })
}

// providePromotionAffiliate constructs affiliate rules with shared settings and
// cache invalidation.
func providePromotionAffiliate(repo promotion.AffiliateRepository, settings *promotion.RuntimeSettings, auth apikey.APIKeyAuthCacheInvalidator, balances *billing.Eligibility) *promotion.AffiliateService {
	return promotion.NewAffiliateService(repo, settings, auth, balances, promotion.Runtime{Now: time.Now, Warn: func(id int64, err error) {
		logging.LegacyPrintf("service.affiliate", "[Affiliate] Failed to invalidate billing cache for user %d: %v", id, err)
	}})
}

// EnsureUserAffiliate delegates affiliate profile creation and discards its
// returned profile.
func (p identityPromotion) EnsureUserAffiliate(ctx context.Context, id int64) error {
	_, err := p.Service.EnsureUserAffiliate(ctx, id)
	return err
}

// BindInviterByCode delegates registration inviter binding to the shared
// affiliate service.
func (p identityPromotion) BindInviterByCode(ctx context.Context, id int64, code string) error {
	return p.Service.BindInviterByCode(ctx, id, code)
}

// providePromotionPromoStore constructs promo-code persistence from the shared
// Ent client.
func providePromotionPromoStore(client *dbent.Client) promotion.PromoCodeRepository {
	return promotionpostgres.NewPromoCodeRepository(client)
}

// providePromotionPromo binds promo grants to transactional balances and
// tracked background effects.
func providePromotionPromo(client *dbent.Client, repo promotion.PromoCodeRepository, auth apikey.APIKeyAuthCacheInvalidator, balances *billing.Eligibility, tasks *lifecycle.Tasks) *promotion.PromoService {
	mutations := promotionpostgres.NewPromoMutations(client, func(tx *dbent.Tx) promotionpostgres.PromoBalance { return billingpostgres.BalanceInTx(tx) })
	return promotion.NewPromoService(repo, mutations, auth, balances, promotion.Runtime{Now: time.Now, Background: func(name string, fn func()) { tasks.Go(name, fn) }})
}

// identityPromotionPreview returns public registration preview data from the
// promo service.
func identityPromotionPreview(s *promotion.PromoService) func(context.Context, string) identityhttp.PromotionPreview {
	return func(ctx context.Context, code string) identityhttp.PromotionPreview {
		v := s.PreviewRegistrationPromotion(ctx, code)
		return identityhttp.PromotionPreview{Valid: v.Valid, BonusAmount: v.BonusAmount, ErrorCode: v.ErrorCode}
	}
}

// providePromotionPromoHTTP exposes promo-code administration through the
// assembled handler.
func providePromotionPromoHTTP(s *promotion.PromoService) *promotionhttp.PromoHandler {
	return promotionhttp.NewPromoHandler(s)
}

// providePromotionAffiliateHTTP binds affiliate administration and recipient
// search using the application calendar. The search callback requests the first
// twenty users ordered by email and returns identity fields, discarding the
// pagination total.
func providePromotionAffiliateHTTP(s *promotion.AffiliateService, users *identity.UserAdmin, calendar timezone.Calendar) *promotionhttp.AffiliateHandler {
	return promotionhttp.NewAffiliateHandler(s, func(ctx context.Context, keyword string) ([]promotionhttp.AffiliateUserSummary, error) {
		// The administrator recipient selector needs a bounded suggestion page,
		// so query the first twenty matching users and omit the total from its callback result.
		values, _, err := users.ListUsers(ctx, 1, 20, identity.UserListFilters{Search: keyword}, "email", "asc")
		if err != nil {
			return nil, err
		}
		out := make([]promotionhttp.AffiliateUserSummary, len(values))
		for i, u := range values {
			out[i] = promotionhttp.AffiliateUserSummary{ID: u.ID, Email: u.Email, Username: u.Username}
		}
		return out, nil
	}, calendar)
}
