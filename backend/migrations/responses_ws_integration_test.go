//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestResponsesWSMigration checks legacy mode conversion, empty protocol sets, and replay.
func TestResponsesWSMigration(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("responses_ws"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Terminate(ctx)) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	_, err = db.ExecContext(ctx, `INSERT INTO providers(id,name,platform,type,credentials,extra) VALUES
 (9201,'direct','openai','oauth','{"upstream_protocols":["openai_responses_websocket"]}','{"openai_oauth_responses_websockets_v2_mode":"passthrough","openai_ws_enabled":false}'),
 (9202,'bridge','openai','apikey','{"upstream_protocols":["openai_responses_websocket","openai_responses"]}','{"openai_apikey_responses_websockets_v2_mode":"http_bridge"}'),
 (9203,'empty','openai','apikey','{"upstream_protocols":[]}','{"openai_apikey_responses_websockets_v2_mode":"passthrough"}'),
 (9204,'off','openai','apikey','{"upstream_protocols":["openai_responses_websocket"]}','{"openai_ws_enabled":false}'),
 (9205,'new','openai','apikey','{"upstream_protocols":["openai_responses_websocket"]}','{"responses_ws_connection_mode":"per_session","openai_ws_force_http":true}')`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("292_responses_ws_connection_settings.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	for _, tc := range []struct {
		id              int
		mode, protocols string
	}{
		{9201, "per_session", `["openai_responses_websocket"]`},
		{9202, "pooled", `["openai_responses"]`},
		{9203, "per_session", `[]`},
		{9204, "pooled", `["openai_responses_websocket"]`},
		{9205, "per_session", `["openai_responses_websocket"]`},
	} {
		var mode, protocols, extra string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT extra->>'responses_ws_connection_mode', credentials->'upstream_protocols', extra::text FROM providers WHERE id=$1`, tc.id).Scan(&mode, &protocols, &extra))
		require.Equal(t, tc.mode, mode)
		require.JSONEq(t, tc.protocols, protocols)
		require.NotContains(t, extra, "openai_ws_enabled")
		require.NotContains(t, extra, "websockets_v2_mode")
	}
}
