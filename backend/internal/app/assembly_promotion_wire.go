//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// Registration promo codes and affiliate rewards use separate services and
// persistence. Identity registration and promotion HTTP handlers receive those
// same services, while promotion settings govern which invitation and reward
// features can be offered.

// promotionAssemblyProviders supplies promo-code and affiliate services, their
// stores, promotion settings, and user HTTP entry points.
var promotionAssemblyProviders = wire.NewSet(
	providePromotionPromoHTTP,
	providePromotionAffiliateHTTP,
	providePromotionPromoStore,
	providePromotionPromo,
	providePromotionAffiliateStore,
	providePromotionAffiliate,
	providePromotionSettings,
	providePromotionUserHTTP,
)
