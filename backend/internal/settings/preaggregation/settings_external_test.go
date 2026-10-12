package preaggregation_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
)

// preaggregationFixture stores runtime JSON and counts synchronous reads for controller tests.
// The controller publishes update notifications after this fixture accepts writes.
type preaggregationFixture struct {
	values        map[string]string // saved fixture setting values
	getValueCalls int               // observed GetValue invocation count
}

// TestPreAggregationSettingsDefaultsFromDeployment leaves the unified runtime
// key absent while obsolete Ops controls are present. One storage read returns
// the deployment-derived usage cadence and enabled task switches.
func TestPreAggregationSettingsDefaultsFromDeployment(t *testing.T) {
	repo := newPreaggregationFixture()
	repo.values[ops.SettingKeyOpsAdvancedSettings] = `{"aggregation":{"aggregation_enabled":false}}`
	repo.values["ops_query_mode_default"] = "raw"
	cfg := &preaggregation.Options{Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 120}, OpsEnabled: true, OpsAggregationEnabled: true}

	settings, err := preaggregation.NewPreAggregationSettingsService(repo, cfg).Get(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Usage.Enabled)
	require.Equal(t, 120, settings.Usage.IntervalSeconds)
	require.True(t, settings.Ops.Enabled)
	require.Equal(t, 1, repo.getValueCalls)
}

// TestPreAggregationSettingsDeploymentCeiling stores enabled runtime switches
// under deployment-disabled usage and Ops capabilities. Get and the boolean
// readers return disabled decisions, protecting the deployment availability ceiling.
func TestPreAggregationSettingsDeploymentCeiling(t *testing.T) {
	repo := newPreaggregationFixture()
	repo.values[preaggregation.SettingKeyPreAggregationSettings] = `{"usage":{"enabled":true,"interval_seconds":60},"ops":{"enabled":true}}`
	cfg := &preaggregation.Options{Usage: preaggregation.UsageOptions{Enabled: false}, OpsEnabled: true, OpsAggregationEnabled: false}

	service := preaggregation.NewPreAggregationSettingsService(repo, cfg)
	settings, err := service.Get(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Usage.Enabled)
	require.False(t, settings.Ops.Enabled)
	require.False(t, service.UsageEnabled(context.Background()))
	require.False(t, service.OpsEnabled(context.Background()))
}

// TestPreAggregationSettingsUpdateNotifies submits disabled controls with a short
// usage interval. The normalized persisted result uses the minimum cadence and
// reaches the listener synchronously with the previous enabled usage state.
func TestPreAggregationSettingsUpdateNotifies(t *testing.T) {
	repo := newPreaggregationFixture()
	cfg := &preaggregation.Options{Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60}, OpsEnabled: true, OpsAggregationEnabled: true}
	service := preaggregation.NewPreAggregationSettingsService(repo, cfg)
	var previous, next preaggregation.PreAggregationSettings
	called := false
	service.RegisterListener(func(before, after preaggregation.PreAggregationSettings) {
		called = true
		previous = before
		next = after
	})

	updated, err := service.Update(context.Background(), preaggregation.PreAggregationSettings{
		Usage: preaggregation.PreAggregationUsageSettings{Enabled: false, IntervalSeconds: 10},
		Ops:   preaggregation.PreAggregationOpsSettings{Enabled: false},
	})
	require.NoError(t, err)
	require.True(t, called)
	require.True(t, previous.Usage.Enabled)
	require.Equal(t, preaggregation.PreAggregationMinIntervalSeconds, updated.Usage.IntervalSeconds)
	require.Equal(t, updated, next)

	var persisted preaggregation.PreAggregationSettings
	require.NoError(t, json.Unmarshal([]byte(repo.values[preaggregation.SettingKeyPreAggregationSettings]), &persisted))
	require.Equal(t, updated, persisted)
}

// newPreaggregationFixture allocates empty saved state for synchronous
// preaggregation controller tests.
func newPreaggregationFixture() *preaggregationFixture {
	return &preaggregationFixture{values: map[string]string{}}
}

// GetValue counts the read and returns saved fixture text or ErrSettingNotFound.
// The fixture ignores contexts and uses no lock, so tests keep its map and
// read count within their synchronous controller calls.
func (r *preaggregationFixture) GetValue(_ context.Context, key string) (string, error) {
	r.getValueCalls++
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", settings.ErrSettingNotFound
}

// Set replaces one fixture value for controller persistence assertions. Contexts
// are ignored and writes have no injected failure; tests use the allocated map
// synchronously before examining the normalized saved JSON.
func (r *preaggregationFixture) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
