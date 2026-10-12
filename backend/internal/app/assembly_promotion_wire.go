//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// promotionAssemblyProviders groups Wire providers for the promotion module.
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
