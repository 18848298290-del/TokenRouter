//go:build integration

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/notification/smtp"
	mailtest "github.com/TokenFlux/TokenRouter/internal/notification/testkit"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// TestProviderBalanceAlertDelivery runs real DeepSeek parsing and SMTP against local fixtures.
// Two monitor instances share PostgreSQL deadlines, so concurrent scans send one
// message. The custom fixture host works with the URL allowlist disabled, just
// like a manual query. A later low-balance round sends another rendered email.
func TestProviderBalanceAlertDelivery(t *testing.T) {
	ctx := context.Background()
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/user/balance" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			http.Error(w, "invalid request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"999"}]}`))
	}))
	defer upstream.Close()
	db := postgrescontainer.New(t)
	client := ent.NewClient(ent.Driver(sql.OpenDB(dialect.Postgres, db)))
	store := providerpostgres.NewProviderStore(client, db, providerpostgres.ProviderStoreOptions{})
	c := provider.BalanceAlertConfig{Enabled: true, Threshold: 1000, IntervalMinutes: 60, RecipientEmail: "alerts@example.com"}
	p, err := client.Provider.Create().SetName("Example wallet").SetPlatform("deepseek").SetType("apikey").SetCredentials(map[string]any{"api_key": "fixture-key", "base_url": upstream.URL}).SetExtra(map[string]any{provider.UpstreamBalanceAlertExtraKey: c}).Save(ctx)
	require.NoError(t, err)
	execution := provideradapter.NewUpstreamUsageHTTPExecution(provideradapter.UsageHTTPOptions{
		Available: true,
		Policy:    egress.UsageURLPolicy{Configured: true, AllowInsecureHTTP: true},
		Do: func(request *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
			return upstream.Client().Do(request)
		},
	})
	queries := provider.NewUpstreamUsageService(store, execution, provider.UpstreamUsageOptions{})
	defer func() { require.NoError(t, queries.StopContext(ctx)) }()
	server := mailtest.StartSMTPServer(t)
	settings := mailtest.NewMemorySettings()
	require.NoError(t, settings.SetMultiple(ctx, server.Settings()))
	mail := notification.NewNotificationEmailService(settings, notification.NewMailer(settings, smtp.New()))
	a := provideBalanceAlertMonitor(store, queries, mail, lifecycle.New())
	b := provideBalanceAlertMonitor(store, queries, mail, lifecycle.New())
	defer func() { require.NoError(t, a.StopContext(ctx)); require.NoError(t, b.StopContext(ctx)) }()
	var group sync.WaitGroup
	group.Go(func() { a.RunOnce(ctx) })
	group.Go(func() { b.RunOnce(ctx) })
	group.Wait()
	require.Equal(t, int64(1), server.MessageCount())
	require.Equal(t, int32(1), requests.Load())
	body := server.LastMessageBody(t)
	require.Contains(t, body, "999 CNY")
	require.Contains(t, body, "1000")
	require.Contains(t, body, "Example wallet")
	router := gin.New()
	handler := providerhttp.NewUpstreamUsageHandler(queries, a)
	router.GET("/admin/providers/:id/upstream-balance-alert", handler.BalanceAlertStatus)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/providers/"+strconv.FormatInt(p.ID, 10)+"/upstream-balance-alert", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"balance":999`)
	require.NotContains(t, recorder.Body.String(), "fixture-key")
	require.NotContains(t, recorder.Body.String(), "identity_hash")
	require.Equal(t, int32(1), requests.Load())
	require.Equal(t, int64(1), server.MessageCount())
	state, err := a.Status(ctx, p.ID)
	require.NoError(t, err)
	require.NotNil(t, state.SentAt)
	_, err = db.ExecContext(ctx, "UPDATE provider_balance_alert_states SET next_check_at=now()-interval '1 second' WHERE provider_id=$1", p.ID)
	require.NoError(t, err)
	b.RunOnce(ctx)
	require.Equal(t, int64(2), server.MessageCount())
	values, err := settings.GetAll(ctx)
	require.NoError(t, err)
	for key := range values {
		require.False(t, strings.HasPrefix(key, "notification_email_delivery:"))
	}
}

// TestBalanceAlertUsesUsageURLPolicy compares manual and scheduled destination validation.
// Disabled allowlists admit configured custom hosts, enabled lists still reject
// unlisted hosts, and scheme/private-host checks remain owned by the query service.
func TestBalanceAlertUsesUsageURLPolicy(t *testing.T) {
	ctx := context.Background()
	db := postgrescontainer.New(t)
	client := ent.NewClient(ent.Driver(sql.OpenDB(dialect.Postgres, db)))
	store := providerpostgres.NewProviderStore(client, db, providerpostgres.ProviderStoreOptions{})
	var requests atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"999"}]}`))
	}))
	defer upstream.Close()
	for _, tc := range []struct {
		name     string                // destination policy scenario
		policy   egress.UsageURLPolicy // actual query policy
		insecure bool                  // HTTP root selection
		allowed  bool                  // expected query admission
	}{
		{name: "allowlist disabled", policy: egress.UsageURLPolicy{Configured: true}, allowed: true},
		{name: "listed host", policy: egress.UsageURLPolicy{Configured: true, Enabled: true, AllowPrivateHosts: true, UpstreamHosts: []string{"127.0.0.1"}}, allowed: true},
		{name: "unlisted host", policy: egress.UsageURLPolicy{Configured: true, Enabled: true, AllowPrivateHosts: true, UpstreamHosts: []string{"other.example"}}},
		{name: "private host denied", policy: egress.UsageURLPolicy{Configured: true, Enabled: true, UpstreamHosts: []string{"127.0.0.1"}}},
		{name: "HTTP denied", policy: egress.UsageURLPolicy{Configured: true}, insecure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := upstream.URL
			if tc.insecure {
				base = strings.Replace(base, "https://", "http://", 1)
			}
			cfg := provider.BalanceAlertConfig{Enabled: true, Threshold: 1000, IntervalMinutes: 60, RecipientEmail: "alerts@example.com"}
			p, err := client.Provider.Create().SetName(tc.name).SetPlatform("deepseek").SetType("apikey").SetCredentials(map[string]any{"api_key": "fixture-key", "base_url": base}).SetExtra(map[string]any{provider.UpstreamBalanceAlertExtraKey: cfg}).Save(ctx)
			require.NoError(t, err)
			execution := provideradapter.NewUpstreamUsageHTTPExecution(provideradapter.UsageHTTPOptions{
				Available: true, Policy: tc.policy,
				Do: func(request *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
					return upstream.Client().Do(request)
				},
			})
			queries := provider.NewUpstreamUsageService(store, execution, provider.UpstreamUsageOptions{})
			defer func() { require.NoError(t, queries.StopContext(ctx)) }()
			sent := 0
			monitor := provider.NewBalanceAlertMonitor(store, queries, func(context.Context, *provider.Record, provider.BalanceAlertConfig, *provider.UpstreamUsageQueryResult) error {
				sent++
				return nil
			})
			defer func() { require.NoError(t, monitor.StopContext(ctx)) }()
			before := requests.Load()
			_, manualErr := queries.QueryProvider(ctx, p.ID)
			monitor.RunOnce(ctx)
			state, err := monitor.Status(ctx, p.ID)
			require.NoError(t, err)
			if tc.allowed {
				require.NoError(t, manualErr)
				require.Empty(t, state.ErrorCode)
				require.Equal(t, 1, sent)
				require.Equal(t, int32(2), requests.Load()-before)
			} else {
				require.Error(t, manualErr)
				require.Equal(t, apperror.FromError(manualErr).Reason, state.ErrorCode)
				require.Zero(t, sent)
				require.Equal(t, before, requests.Load())
			}
		})
	}
}
