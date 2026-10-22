//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestUserLocalizationMigrations applies localization to fresh and historical
// PostgreSQL schemas. It checks revision-sensitive group display, migration
// replay, canonical user language, migrated site and entity text, retained
// announcement receipts, retired setting keys, and rollback of the preferred
// language column after an intentional transaction failure.
func TestUserLocalizationMigrations(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("localization"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	// Run may return a container alongside a startup error. Register cleanup
	// before checking the error, and give termination its own deadline so test
	// cancellation still allows Docker to remove the partially started instance.
	if pg != nil {
		t.Cleanup(func() {
			cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancelCleanup()
			if err := pg.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate PostgreSQL container: %v", err)
			}
		})
	}
	require.NoError(t, err)
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	// The SQL cleanup is registered after container cleanup, so testing closes
	// the pool before terminating the PostgreSQL instance used by these scenarios.
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	// Exercise LocalizedTextExpression against missing and empty content, a current
	// English translation, and a stale translation that must resolve to source
	// text. These fixtures check the SQL expression's selected display value.
	t.Run("group display expression", func(t *testing.T) {
		userContext := locale.WithUserPresentation(locale.WithLanguage(ctx, "en"), true)
		expression := postgres.LocalizedTextExpression(userContext, "g.localization", "display_name", "g.name")
		// content supplies the stored localization envelope, while want identifies
		// the selected display text. Revision mismatch in the final fixture
		// makes the expression choose source text over the stale translation.
		for _, sample := range []struct {
			content, want string /* content is persisted JSON; want is the resolved display text. */
		}{
			{`null`, "business"},
			{`{"revision":0,"source":{"display_name":""},"translations":null}`, "business"},
			{`{"revision":1,"source_revision":1,"source":{"display_name":"Original"},"translations":{"en":{"source_revision":1,"value":{"display_name":"English"}}}}`, "English"},
			{`{"revision":1,"source_revision":2,"source":{"display_name":"Original"},"translations":{"en":{"source_revision":1,"value":{"display_name":"Stale"}}}}`, "Original"},
		} {
			var actual string
			require.NoError(t, db.QueryRowContext(userContext, "SELECT "+expression+" FROM (SELECT 'business'::text AS name, $1::jsonb AS localization) g", sample.content).Scan(&actual))
			require.Equal(t, sample.want, actual)
		}
	})

	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name='users' AND column_name='preferred_locale'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE filename = '290_user_localization.sql'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name='error_passthrough_rules' AND column_name='message_localization'`).Scan(&count))
	require.Zero(t, count)
	// Replace the fresh schema with the pre-290 migration state before seeding
	// legacy language settings, so the next upgrade exercises historical content conversion.
	_, err = db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`)
	require.NoError(t, err)
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= "290" {
			continue
		}
		data, err := migrations.FS.ReadFile(entry.Name())
		require.NoError(t, err)
		prior[entry.Name()] = &fstest.MapFile{Data: data}
	}
	require.NoError(t, postgres.ApplyMigrations(ctx, db, prior))
	_, err = db.Exec(`INSERT INTO users(id,email,password_hash) VALUES(991,'locale@example.com','hash');
 INSERT INTO announcements(id,title,content) VALUES(991,'公告','正文');
 INSERT INTO announcement_reads(announcement_id,user_id) VALUES(991,991);
 INSERT INTO groups(id,name) VALUES(991,'business-group');
 INSERT INTO settings(key,value) VALUES
 ('site_name','Original'),('site_name_zh','中文站点'),('site_name_en','English site'),
 ('site_title_zh','中文标题'),('site_subtitle','  '),('site_subtitle_zh',''),('site_subtitle_en',''),('notification_email_locale:user:991','zh'),
 ('notification_email_template:auth.verify_code:zh','{"subject":"验证码","html":"<p>正文</p>"}');`)
	require.NoError(t, err)
	// Fail after localization SQL has completed, then roll back that transaction.
	// The following column query must still observe the historical schema
	// before the runner retries the migration successfully.
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("290_user_localization.sql")
	require.NoError(t, err)
	_, err = tx.Exec(string(migration))
	require.NoError(t, err)
	_, err = tx.Exec(`SELECT localization_intentional_failure()`)
	require.Error(t, err)
	require.NoError(t, tx.Rollback())
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name='users' AND column_name='preferred_locale'`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	var language, raw string
	require.NoError(t, db.QueryRow(`SELECT preferred_locale FROM users WHERE id=991`).Scan(&language))
	require.Equal(t, "zh-Hans", language)
	require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key='site_texts'`).Scan(&raw))
	var texts map[string]locale.Content[string]
	require.NoError(t, json.Unmarshal([]byte(raw), &texts))
	require.Nil(t, texts["site_name"].SourceLocale)
	require.Equal(t, "Original", texts["site_name"].Source)
	for code, expected := range map[string]string{"en": "English site", "zh-Hans": "中文站点"} {
		value, info := texts["site_name"].Resolve(code)
		require.Equal(t, expected, value)
		require.False(t, info.Fallback)
	}
	require.NotContains(t, texts, "site_subtitle")
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM announcement_reads WHERE user_id=991 AND announcement_id=991`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow(`SELECT localization->'source'->>'title' FROM announcements WHERE id=991`).Scan(&raw))
	require.Equal(t, "公告", raw)
	require.NoError(t, db.QueryRow(`SELECT localization->'source'->>'display_name' FROM groups WHERE id=991`).Scan(&raw))
	require.Equal(t, "business-group", raw)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM settings WHERE key IN ('site_name_zh','site_name_en','notification_email_template:auth.verify_code:zh')`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM settings WHERE key='notification_email_template:auth.verify_code:zh-Hans'`).Scan(&count))
	require.Equal(t, 1, count)
}

// TestHomeTextMigration converts missing, empty, whitespace, and translated home
// settings in a minimal PostgreSQL schema. It checks whether usable content
// exists, the selected source and initial revision, preservation of an unrelated
// setting, and stable results after two executions.
func TestHomeTextMigration(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("home_text"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	// Run may return a container alongside a startup error. Register cleanup
	// before checking the error, and give termination its own deadline so test
	// cancellation still allows Docker to remove the partially started instance.
	if pg != nil {
		t.Cleanup(func() {
			cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancelCleanup()
			if err := pg.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate PostgreSQL container: %v", err)
			}
		})
	}
	require.NoError(t, err)
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	// Register pool cleanup after container cleanup so the shared fixture's SQL
	// connections close before testing terminates the isolated PostgreSQL instance.
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec(`CREATE TABLE settings(key text PRIMARY KEY, value text NOT NULL, updated_at timestamptz DEFAULT now());
CREATE TABLE users(id bigint);
CREATE TABLE announcements(title text, content text);
CREATE TABLE groups(name text, description text);
CREATE TABLE subscription_plans(name text, description text, features text, product_name text);`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("290_user_localization.sql")
	require.NoError(t, err)
	// source, chinese, and english supply the legacy text inputs; wantSource
	// identifies the selected usable text. missing distinguishes absent keys
	// from stored empty strings, while configured records expected content availability.
	for _, tc := range []struct {
		// name labels the subtest; source is legacy default text; chinese is
		// legacy zh text; english is legacy en text; wantSource is the expected
		// selected source text after conversion.
		name, source, chinese, english, wantSource string
		// missing skips all three legacy language keys for both title and
		// subtitle. This case models absent input settings, while other cases
		// insert their supplied values.
		//
		// configured selects the expected existence of migrated home content.
		// Empty and whitespace inputs produce no entry when no translated
		// value is usable.
		missing, configured bool // missing: skip legacy inputs; configured: expect usable content
	}{
		{name: "missing", missing: true},
		{name: "empty"},
		{name: "whitespace", source: " \t\n", chinese: "\n", english: "\t"},
		{name: "custom", source: "Custom title", wantSource: "Custom title", configured: true},
		{name: "chinese translation", chinese: "中文标题", wantSource: "中文标题", configured: true},
		{name: "english translation", english: "English title", wantSource: "English title", configured: true},
		{name: "blank source with translation", source: " \t", english: "English title", wantSource: "English title", configured: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Clear settings from the previous scenario and seed an unrelated value
			// before inserting this case's legacy inputs. The reused schema stays
			// available while each case receives an independent setting fixture.
			_, err := db.Exec(`DELETE FROM settings; INSERT INTO settings(key,value) VALUES('unrelated','plain text')`)
			require.NoError(t, err)
			if !tc.missing {
				for _, field := range []string{"site_title", "site_subtitle"} {
					_, err = db.Exec(`INSERT INTO settings(key,value) VALUES($1,$2),($3,$4),($5,$6)`, field, tc.source, field+"_zh", tc.chinese, field+"_en", tc.english)
					require.NoError(t, err)
				}
			}
			// Repeat the conversion after legacy keys have been migrated. The
			// result must retain its chosen source and initial revision on replay.
			for range 2 {
				_, err = db.Exec(string(migration))
				require.NoError(t, err)
			}
			var raw string
			require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key='site_texts'`).Scan(&raw))
			var texts map[string]locale.Content[string]
			require.NoError(t, json.Unmarshal([]byte(raw), &texts))
			for _, field := range []string{"site_title", "site_subtitle"} {
				content, exists := texts[field]
				require.Equal(t, tc.configured, exists)
				if exists {
					require.Equal(t, tc.wantSource, content.Source)
					require.EqualValues(t, 1, content.Revision)
				}
			}
			require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key='unrelated'`).Scan(&raw))
			require.Equal(t, "plain text", raw)
		})
	}
}
