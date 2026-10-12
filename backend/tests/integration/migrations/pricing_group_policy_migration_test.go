//go:build integration

package migrations_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/migrations"
)

func TestMigration274PreservesPricesAndCopiesPolicies(t *testing.T) {
	tx := historicalTx(t, "274_")
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `
INSERT INTO groups (id,name,platform) VALUES (91001,'migration274-a','openai'),(91002,'migration274-b','openai'),(91003,'migration274-c','anthropic');
INSERT INTO channels (id,name,status,billing_model_source,restrict_models,model_mapping,features_config) VALUES
 (92001,'migration274-active','active','upstream',true,'{"openai":{"alias":"real"}}','{"codex_image_generation_bridge":{"openai":false}}'),
 (92002,'migration274-disabled','disabled','channel_mapped',true,'{"anthropic":{"alias":"claude-sonnet-4"}}','{}'),
 (92003,'migration274-unbound','active','requested',false,'{"openai":{"unused":"model"}}','{}');
INSERT INTO channel_groups (channel_id,group_id) VALUES (92001,91001),(92001,91002),(92002,91003);
INSERT INTO channel_model_pricing (id,channel_id,platform,models,input_price,output_price) VALUES
 (93001,92001,'openai','["real","gpt-*"]',0,0.000123),
 (93002,92002,'anthropic','["claude-*"]',NULL,0.000456),
 (93003,92001,'openai','null',NULL,NULL);
INSERT INTO channel_pricing_intervals (id,pricing_id,min_tokens,max_tokens,input_price,sort_order) VALUES (94001,93001,100,200,0,2);
INSERT INTO users(id,email,password_hash) VALUES (95001,'migration274@example.test','test');
INSERT INTO accounts(id,name,platform,type,credentials) VALUES (95001,'migration274','openai','apikey','{}');
INSERT INTO api_keys(id,user_id,key,name) VALUES (95001,95001,'migration274-key','migration274');
INSERT INTO usage_logs(user_id,api_key_id,account_id,model,channel_id,total_cost,actual_cost) VALUES (95001,95001,95001,'real',92001,1.23,2.34);
`)
	require.NoError(t, err)
	sql, err := migrations.FS.ReadFile("274_split_pricing_configs_and_group_policy.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(sql))
	require.NoError(t, err)
	for _, id := range []int64{91001, 91002, 91003} {
		var raw []byte
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT routing_policy FROM groups WHERE id=$1`, id).Scan(&raw))
		// Decode the historical platform-nested policy with its own shape; current entities use flat fields.
		var policy struct {
			Enabled                bool                         `json:"enabled"`
			RestrictModels         bool                         `json:"restrict_models"`
			RestrictionModelSource string                       `json:"restriction_model_source"`
			AllowedModels          map[string][]string          `json:"allowed_models"`
			ModelMapping           map[string]map[string]string `json:"model_mapping"`
		}
		require.NoError(t, json.Unmarshal(raw, &policy))
		require.Equal(t, id != 91003, policy.Enabled)
		require.True(t, policy.RestrictModels)
		if id != 91003 {
			require.ElementsMatch(t, []string{"real", "gpt-*"}, policy.AllowedModels["openai"])
			require.Equal(t, "upstream", policy.RestrictionModelSource)
			require.Equal(t, "real", policy.ModelMapping["openai"]["alias"])
		} else {
			require.Equal(t, "group_mapped", policy.RestrictionModelSource)
		}
	}
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM pricing_policy_migration_archive WHERE original_config_id BETWEEN 92001 AND 92003`).Scan(&count))
	require.Equal(t, 3, count)
	var input, output float64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT input_price,output_price FROM pricing_config_model_pricing WHERE id=93001 AND pricing_config_id=92001`).Scan(&input, &output))
	require.Zero(t, input)
	require.InDelta(t, 0.000123, output, 1e-12)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM pricing_config_pricing_intervals WHERE id=94001 AND pricing_id=93001 AND sort_order=2 AND input_price=0`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM usage_logs WHERE pricing_config_id=92001 AND total_cost=1.23 AND actual_cost=2.34`).Scan(&count))
	require.Equal(t, 1, count)
	_, err = tx.ExecContext(ctx, `UPDATE groups SET routing_policy='{"enabled":false}' WHERE id=91001`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(sql))
	require.NoError(t, err)
	var enabled bool
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT (routing_policy->>'enabled')::boolean FROM groups WHERE id=91001`).Scan(&enabled))
	require.False(t, enabled, "重放不能覆盖管理员已修改的策略")
}
