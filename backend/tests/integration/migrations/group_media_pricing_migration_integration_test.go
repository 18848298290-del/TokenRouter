//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/migrations"
)

// Removing independent media prices leaves the group model price and ordinary
// multiplier available. A temporary groups table supplies exactly the retired
// columns alongside those retained values, and two SQL executions are followed
// by checks of the chosen model price and three remaining columns.

// TestRemoveGroupMediaPricingMigration creates a temporary group with an
// explicit zero image model price and independent media settings. It checks
// replay, the retained model entry and multiplier, and the remaining column
// count.
func TestRemoveGroupMediaPricingMigration(t *testing.T) {
	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	// Cleanup attempts rollback even when a require assertion stops the
	// test. Its error is discarded; the rollback attempt removes this
	// temporary-table fixture from the shared connection.
	t.Cleanup(func() { _ = tx.Rollback() })
	// Unqualified groups SQL resolves to this temporary table, allowing
	// migration 272 to remove its eleven retired columns while
	// public.groups stays outside the fixture.
	_, err = tx.Exec(`CREATE TEMP TABLE groups (id bigint, model_pricing jsonb, rate_multiplier numeric, image_price_1k numeric, image_price_2k numeric, image_price_4k numeric, video_price_480p numeric, video_price_720p numeric, video_price_1080p numeric, video_model_prices jsonb, image_rate_independent boolean, image_rate_multiplier numeric, video_rate_independent boolean, video_rate_multiplier numeric)`)
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT INTO groups (id,model_pricing,rate_multiplier,image_price_1k,video_model_prices,image_rate_multiplier) VALUES (1,'[{"models":["gpt-image-2"],"billing_mode":"image","per_request_price":0}]',1.5,0.4,'{"grok-imagine-video":{"480p":0.2}}',0.5)`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("272_remove_group_media_pricing.sql")
	require.NoError(t, err)
	// Execute the removal again after the columns are gone to exercise its
	// IF EXISTS clauses before inspecting retained model values and
	// ordinary group pricing.
	for range 2 {
		_, err = tx.Exec(string(migration))
		require.NoError(t, err)
	}
	var model, mode string
	var price, rate float64
	err = tx.QueryRow(`SELECT model_pricing->0->'models'->>0,model_pricing->0->>'billing_mode',(model_pricing->0->>'per_request_price')::numeric,rate_multiplier FROM groups WHERE id=1`).Scan(&model, &mode, &price, &rate)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", model)
	require.Equal(t, "image", mode)
	require.Zero(t, price)
	require.Equal(t, 1.5, rate)
	var columns int
	err = tx.QueryRow(`SELECT count(*) FROM pg_attribute WHERE attrelid='pg_temp.groups'::regclass AND attnum>0 AND NOT attisdropped`).Scan(&columns)
	require.NoError(t, err)
	require.Equal(t, 3, columns)
}
