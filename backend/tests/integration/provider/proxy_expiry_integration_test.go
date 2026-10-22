//go:build integration

package provider_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/egress/postgres"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Expired proxy policy may clear a provider's proxy, move it to a healthy
// backup, or retain the selected proxy. Transaction-owned PostgreSQL fixtures
// check those choices, record the original proxy for fallback paths, and
// inspect the affected-provider scheduling event.

// ProxyExpirySuite checks proxy-expiry policy and provider references inside
// PostgreSQL transactions.
type ProxyExpirySuite struct {
	suite.Suite // test assertions and runner
	// ctx, tx, and repo share one transaction for the current suite test.
	// Proxy and provider fixture writes use that transaction, whose rollback
	// is registered by testEntTx at cleanup.
	ctx  context.Context      // current expiry-test query context
	tx   *dbent.Tx            // rollback-owned PostgreSQL fixture transaction
	repo *postgres.ProxyStore // proxy expiry store under test
}

// SetupTest creates a rollback-owned Ent transaction for proxy-expiry
// assertions. The proxy store and provider cleanup participant share that
// executor, and testEntTx registers transaction cleanup after the case
// completes.
func (s *ProxyExpirySuite) SetupTest() {
	s.ctx = context.Background()
	s.tx = testEntTx(s.T())
	s.repo = newProxyStoreContract(s.tx.Client(), s.tx)
}

// TestProxyExpirySuite runs expiry policy cases against transaction-owned
// PostgreSQL fixtures. The suite compares provider proxy references, recorded
// fallback origins, proxy status, and scheduling events after each sweep.
func TestProxyExpirySuite(t *testing.T) { suite.Run(t, new(ProxyExpirySuite)) }

// mkProxy persists an active HTTP proxy with the requested expiry and fallback
// policy, returning its database ID. The suite transaction owns cleanup, and
// backupID may be nil when no backup is selected.
func (s *ProxyExpirySuite) mkProxy(name, mode string, expiresAt *time.Time, backupID *int64) int64 {
	p := &egress.Proxy{
		Name: name, Protocol: "http", Host: "127.0.0.1", Port: 8080,
		Status: billing.StatusActive, FallbackMode: mode, ExpiryWarnDays: 7,
		ExpiresAt: expiresAt, BackupProxyID: backupID,
	}
	s.Require().NoError(s.repo.Create(s.ctx, p))
	return p.ID
}

// mkProviderWithProxy inserts a provider linked to proxyID and returns its
// database identity. It uses legacy fixture platform and type strings because
// these cases inspect proxy references; the suite transaction owns removal.
func (s *ProxyExpirySuite) mkProviderWithProxy(proxyID int64) int64 {
	var id int64
	err := postgresinfra.ScanSingleRow(s.ctx, s.tx, `
		INSERT INTO providers (name, platform, type, credentials, extra, status, proxy_id, created_at, updated_at)
		VALUES ($1,'claude','api','{}','{}','active',$2,NOW(),NOW()) RETURNING id`,
		[]any{"acc-" + time.Now().Format("150405.000000"), proxyID}, &id)
	s.Require().NoError(err)
	return id
}

// providerProxyID reads the provider's nullable proxy reference through the
// suite transaction. A nil result represents direct connectivity; scan errors
// fail the test before its caller inspects the reference.
func (s *ProxyExpirySuite) providerProxyID(id int64) *int64 {
	var pid *int64
	err := postgresinfra.ScanSingleRow(s.ctx, s.tx, `SELECT proxy_id FROM providers WHERE id=$1`, []any{id}, &pid)
	s.Require().NoError(err)
	return pid
}

// TestSweep_DirectMode expires a proxy configured for direct fallback and
// sweeps it. The proxy becomes expired, the provider's selected proxy is
// cleared, and its fallback origin records the expired proxy ID.
func (s *ProxyExpirySuite) TestSweep_DirectMode() {
	past := time.Now().Add(-time.Hour)
	pid := s.mkProxy("p-direct", egress.FallbackModeDirect, &past, nil)
	aid := s.mkProviderWithProxy(pid)

	changed, err := s.repo.SweepExpiredProxies(s.ctx, time.Now())
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(changed, int64(1))

	// This status check discards the lookup error; a nil record fails at the
	// following dereference, while the suite transaction still owns fixture cleanup.
	got, _ := s.repo.GetByID(s.ctx, pid)
	s.Require().Equal(billing.StatusExpired, got.Status)
	s.Require().Nil(s.providerProxyID(aid))
	var origin *int64
	err = postgresinfra.ScanSingleRow(s.ctx, s.tx, `SELECT proxy_fallback_origin_id FROM providers WHERE id=$1`, []any{aid}, &origin)
	s.Require().NoError(err)
	s.Require().NotNil(origin)
	s.Require().Equal(pid, *origin)
}

// TestSweep_EnqueuesChangedProviderIDsWithoutFullRebuild expires two
// direct-fallback proxies with one linked provider each. Their IDs appear in
// the ordered bulk-change payload, and the transaction contains no full-rebuild
// event.
func (s *ProxyExpirySuite) TestSweep_EnqueuesChangedProviderIDsWithoutFullRebuild() {
	past := time.Now().Add(-time.Hour)
	firstProxyID := s.mkProxy("p-bulk-first", egress.FallbackModeDirect, &past, nil)
	secondProxyID := s.mkProxy("p-bulk-second", egress.FallbackModeDirect, &past, nil)
	firstProviderID := s.mkProviderWithProxy(firstProxyID)
	secondProviderID := s.mkProviderWithProxy(secondProxyID)

	changed, err := s.repo.SweepExpiredProxies(s.ctx, time.Now())
	s.Require().NoError(err)
	s.Require().EqualValues(2, changed)

	var payloadRaw []byte
	err = postgresinfra.ScanSingleRow(s.ctx, s.tx, `
		SELECT payload
		FROM scheduler_outbox
		WHERE event_type=$1
		ORDER BY id DESC
		LIMIT 1`, []any{scheduler.SchedulerOutboxEventProviderBulkChanged}, &payloadRaw)
	s.Require().NoError(err)

	var payload struct {
		ProviderIDs []int64 `json:"provider_ids"` // ordered affected-provider ID list
	}
	s.Require().NoError(json.Unmarshal(payloadRaw, &payload))
	s.Require().Equal([]int64{firstProviderID, secondProviderID}, payload.ProviderIDs)

	var fullRebuildCount int
	err = postgresinfra.ScanSingleRow(s.ctx, s.tx, `
		SELECT COUNT(*)
		FROM scheduler_outbox
		WHERE event_type=$1`, []any{scheduler.SchedulerOutboxEventFullRebuild}, &fullRebuildCount)
	s.Require().NoError(err)
	s.Require().Zero(fullRebuildCount)
}

// TestSweep_ProxyMode_Healthy gives an expired proxy an active backup whose
// expiry lies in the future. Sweeping switches the linked provider to that
// backup and records the expired proxy as fallback origin.
func (s *ProxyExpirySuite) TestSweep_ProxyMode_Healthy() {
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-time.Hour)
	backup := s.mkProxy("p-backup", egress.FallbackModeNone, &future, nil)
	pid := s.mkProxy("p-main", egress.FallbackModeProxy, &past, &backup)
	aid := s.mkProviderWithProxy(pid)

	_, err := s.repo.SweepExpiredProxies(s.ctx, time.Now())
	s.Require().NoError(err)
	s.Require().Equal(backup, *s.providerProxyID(aid))
	var origin *int64
	err = postgresinfra.ScanSingleRow(s.ctx, s.tx, `SELECT proxy_fallback_origin_id FROM providers WHERE id=$1`, []any{aid}, &origin)
	s.Require().NoError(err)
	s.Require().NotNil(origin)
	s.Require().Equal(pid, *origin)
}

// TestSweep_NoneMode_KeepsProvider expires a proxy whose fallback policy leaves
// its connection selected. The sweep marks the proxy expired, retains the
// provider reference, and leaves its fallback origin unset.
func (s *ProxyExpirySuite) TestSweep_NoneMode_KeepsProvider() {
	past := time.Now().Add(-time.Hour)
	pid := s.mkProxy("p-none", egress.FallbackModeNone, &past, nil)
	aid := s.mkProviderWithProxy(pid)

	_, err := s.repo.SweepExpiredProxies(s.ctx, time.Now())
	s.Require().NoError(err)
	// This status check discards the lookup error; a nil record fails at the
	// following dereference, while the suite transaction still owns fixture cleanup.
	got, _ := s.repo.GetByID(s.ctx, pid)
	s.Require().Equal(billing.StatusExpired, got.Status)
	s.Require().Equal(pid, *s.providerProxyID(aid))
	var origin *int64
	err = postgresinfra.ScanSingleRow(s.ctx, s.tx, `SELECT proxy_fallback_origin_id FROM providers WHERE id=$1`, []any{aid}, &origin)
	s.Require().NoError(err)
	s.Require().Nil(origin)
}

// TestSweep_ReassignmentPreservesFilteringAndSnapshotRules expires direct and
// backup proxies with varied provider records. Reassignment must retain existing
// origins and deleted rows while clearing the exact managed fields on eligible rows.
func (s *ProxyExpirySuite) TestSweep_ReassignmentPreservesFilteringAndSnapshotRules() {
	for _, mode := range []string{egress.FallbackModeDirect, egress.FallbackModeProxy} {
		s.Run(mode, func() {
			past := time.Now().Add(-time.Hour)
			backup := s.mkProxy("rules-backup-"+mode, egress.FallbackModeNone, nil, nil)
			source := s.mkProxy("rules-source-"+mode, mode, &past, &backup)
			for _, tc := range []struct {
				name           string // provider scenario label
				platform       string // stored provider platform
				kind           string // stored credential kind
				existingOrigin bool   // existing fallback origin
				deleted        bool   // soft deletion state
				clearSnapshot  bool   // expected managed snapshot removal
			}{
				{name: "openai-key", platform: "openai", kind: "apikey", clearSnapshot: true},
				{name: "anthropic-key", platform: "anthropic", kind: "apikey", clearSnapshot: true},
				{name: "other-platform", platform: "grok", kind: "apikey"},
				{name: "other-kind", platform: "openai", kind: "oauth"},
				{name: "existing-origin", platform: "openai", kind: "apikey", existingOrigin: true},
				{name: "deleted", platform: "openai", kind: "apikey", deleted: true},
			} {
				s.Run(tc.name, func() {
					id := s.mkProviderWithProxy(source)
					extra := map[string]any{"ollama_cloud_usage_snapshot": map[string]any{"used": 1.0}, "upstream_billing_probe": true, "upstream_billing_probe_enabled": true, "keep": "value"}
					update := s.tx.Client().Provider.UpdateOneID(id).SetPlatform(tc.platform).SetType(tc.kind).SetExtra(extra)
					if tc.existingOrigin {
						update.SetProxyFallbackOriginID(backup)
					}
					if tc.deleted {
						update.SetDeletedAt(time.Now())
					}
					s.Require().NoError(update.Exec(s.ctx))
					// Invoke the same provider participant as expiry while retaining this
					// test's transaction, so direct and backup targets exercise identical cleanup rules.
					var target *int64
					if mode == egress.FallbackModeProxy {
						target = &backup
					}
					_, err := providerpostgres.ProxyChangesInTx(s.tx).Reassign(s.ctx, source, target)
					s.Require().NoError(err)
					var gotProxy, origin *int64
					var raw []byte
					s.Require().NoError(postgresinfra.ScanSingleRow(s.ctx, s.tx, `SELECT proxy_id,proxy_fallback_origin_id,extra FROM providers WHERE id=$1`, []any{id}, &gotProxy, &origin, &raw))
					var got map[string]any
					s.Require().NoError(json.Unmarshal(raw, &got))
					if tc.existingOrigin || tc.deleted {
						s.Require().Equal(&source, gotProxy)
						s.Require().Equal(extra, got)
						if tc.existingOrigin {
							s.Require().Equal(&backup, origin)
						} else {
							s.Require().Nil(origin)
						}
						return
					}
					s.Require().Equal(target, gotProxy)
					s.Require().Equal(&source, origin)
					s.Require().Equal("value", got["keep"])
					s.Require().NotContains(got, "upstream_billing_probe")
					s.Require().NotContains(got, "upstream_billing_probe_enabled")
					if tc.clearSnapshot {
						s.Require().NotContains(got, "ollama_cloud_usage_snapshot")
					} else {
						s.Require().Equal(extra["ollama_cloud_usage_snapshot"], got["ollama_cloud_usage_snapshot"])
					}
				})
			}
		})
	}
}
