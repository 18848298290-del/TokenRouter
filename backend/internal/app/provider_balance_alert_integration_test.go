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
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/notification/smtp"
	mailtest "github.com/TokenFlux/TokenRouter/internal/notification/testkit"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usagecontract"
)

// TestProviderBalanceAlertDelivery runs real DeepSeek parsing and SMTP against local fixtures.
// Two monitor instances share PostgreSQL deadlines, so concurrent scans send one
// message. A later low-balance round sends again, with numeric and unit values
// carried from the upstream response into the rendered email.
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
	execution := provideradapter.NewUpstreamUsageExecution(provideradapter.UpstreamUsageExecutionOptions{
		Available: func() bool { return true }, BaseURL: func(*provider.Record) string { return upstream.URL },
		Request: func(p *provider.Record, _ provider.UpstreamUsageQueryConfig) (*usagecontract.Request, error) {
			return &usagecontract.Request{BaseURL: upstream.URL, APIKey: p.GetCredential("api_key"), Do: upstream.Client().Do, Context: func(ctx context.Context) context.Context { return ctx }, ApplyHeaders: func(http.Header) {}}, nil
		},
	})
	queries := provider.NewUpstreamUsageService(store, execution, provider.UpstreamUsageOptions{})
	defer func() { require.NoError(t, queries.StopContext(ctx)) }()
	server := mailtest.StartSMTPServer(t)
	settings := mailtest.NewMemorySettings()
	require.NoError(t, settings.SetMultiple(ctx, server.Settings()))
	mail := notification.NewNotificationEmailService(settings, notification.NewMailer(settings, smtp.New()))
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.UpstreamHosts = []string{"127.0.0.1"}
	a := provideBalanceAlertMonitor(store, queries, mail, cfg, lifecycle.New())
	b := provideBalanceAlertMonitor(store, queries, mail, cfg, lifecycle.New())
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
