//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// Price-config billing settings start with defaults even when an associated
// group has different legacy values. The historical fixture checks those
// defaults alongside shared price rows and links; a current-store fixture then
// checks the distinction between a stored zero override and a cleared nullable
// price through create, update, and list operations.

// TestMigration281MovesSettingsWithoutCopyingGroupValues supplies divergent
// group prices and a linked shared price config before migration 281. It checks
// new defaults, selected removed columns, and retained price, multiplier, and
// association.
func TestMigration281MovesSettingsWithoutCopyingGroupValues(t *testing.T) {
	ctx := context.Background()
	tx := historicalTx(t, "281_")
	_, err := tx.ExecContext(ctx, `INSERT INTO groups(id,name,rate_multiplier,free_openai_fast,long_context_pricing_enabled,web_search_price_per_call,model_pricing) VALUES(98101,'priced',2,true,false,8,'[{"models":["old"],"input_price":9}]')`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO pricing_configs(id,name) VALUES(98101,'shared')`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO pricing_config_groups(pricing_config_id,group_id) VALUES(98101,98101)`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO pricing_config_model_pricing(pricing_config_id,models,billing_mode,input_price) VALUES(98101,'["shared-model"]','token',0.125)`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("281_pricing_config_billing_settings.sql")
	require.NoError(t, err)
	// The first pass adds config settings and drops group price columns.
	// Replay then checks the same SQL can finish after those structural
	// changes already exist.
	for range 2 {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	var longContext, free bool
	var discount, hold, rate float64
	var search *float64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT long_context_pricing_enabled,free_openai_fast,batch_image_discount_multiplier,batch_image_hold_multiplier,web_search_price_per_call FROM pricing_configs WHERE id=98101`).Scan(&longContext, &free, &discount, &hold, &search))
	require.True(t, longContext)
	require.False(t, free)
	require.Equal(t, 0.5, discount)
	require.Equal(t, 0.6, hold)
	require.Nil(t, search)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT rate_multiplier FROM groups WHERE id=98101`).Scan(&rate))
	require.Equal(t, 2.0, rate)
	var sharedPrice float64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT input_price FROM pricing_config_model_pricing WHERE pricing_config_id=98101`).Scan(&sharedPrice))
	require.Equal(t, 0.125, sharedPrice)
	var linkedGroup int64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT group_id FROM pricing_config_groups WHERE pricing_config_id=98101`).Scan(&linkedGroup))
	require.Equal(t, int64(98101), linkedGroup)
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='groups' AND column_name IN ('model_pricing','free_openai_fast','peak_rate_enabled','web_search_price_per_call')`).Scan(&count))
	require.Zero(t, count)
}

// TestPricingConfigBillingSettingsRoundTrip creates a stored zero search-price
// override and a model price through the service. It checks override clearing,
// updated flags, retained model price, and billing settings returned by the
// filtered list.
func TestPricingConfigBillingSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := routingpostgres.NewPricingConfigStore(integrationDB)
	svc := routing.NewPricingConfigService(repo, nil)
	// A nonnil pointer to zero stores an explicit free search call. The
	// later Set flag with a nil Value clears that override back to
	// inherited pricing.
	zero, price := 0.0, 0.15
	config, err := svc.Create(ctx, &routing.CreatePricingConfigInput{
		Name:                 "billing-settings-roundtrip",
		BillingSettingsPatch: routing.BillingSettingsPatch{WebSearchPricePerCall: routing.PriceUpdate{Set: true, Value: &zero}},
		ModelPricing:         []routing.ModelPricingEntry{{Models: []string{"custom-image"}, BillingMode: routing.BillingModeImage, PerRequestPrice: &price}},
	})
	require.NoError(t, err)
	// This service test commits through the shared repository, so cleanup
	// deletes its created config even when a later assertion stops the test
	// before completion.
	t.Cleanup(func() { require.NoError(t, repo.Delete(ctx, config.ID)) })
	require.True(t, config.LongContextPricingEnabled)
	require.Equal(t, 0.5, config.BatchImageDiscountMultiplier)
	require.NotNil(t, config.WebSearchPricePerCall)
	require.Zero(t, *config.WebSearchPricePerCall)
	require.Equal(t, price, *config.ModelPricing[0].PerRequestPrice)
	enabled, disabled := true, false
	updated, err := svc.Update(ctx, config.ID, &routing.UpdatePricingConfigInput{BillingSettingsPatch: routing.BillingSettingsPatch{
		WebSearchPricePerCall: routing.PriceUpdate{Set: true}, FreeOpenAIFast: &enabled, LongContextPricingEnabled: &disabled,
	}})
	require.NoError(t, err)
	require.Nil(t, updated.WebSearchPricePerCall)
	require.True(t, updated.FreeOpenAIFast)
	require.False(t, updated.LongContextPricingEnabled)
	require.Equal(t, price, *updated.ModelPricing[0].PerRequestPrice)
	all, err := repo.ListAll(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, all)
	listed, _, err := repo.List(ctx, pagination.PaginationParams{Page: 1, PageSize: 20}, "", "billing-settings-roundtrip")
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, updated.BillingSettings, listed[0].BillingSettings)
}
