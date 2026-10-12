package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
)

// preAggregationHandlerRepoStub supplies raw setting values for synchronous
// preaggregation HTTP tests.
type preAggregationHandlerRepoStub struct {
	values map[string]string // saved fixture setting values
}

// TestSettingHandlerGetPreAggregationSettings supplies obsolete Ops controls
// while the unified runtime setting is absent. GET succeeds with enabled deployment
// defaults and the sixty-second usage interval in its response.
func TestSettingHandlerGetPreAggregationSettings(t *testing.T) {
	repo := &preAggregationHandlerRepoStub{values: map[string]string{
		ops.SettingKeyOpsAdvancedSettings: `{"aggregation":{"aggregation_enabled":false}}`,
		"ops_query_mode_default":          "raw",
	}}
	handler := newPreAggregationHandler(repo)
	router := gin.New()
	router.GET("/pre-aggregation", handler.GetPreAggregationSettings)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/pre-aggregation", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope struct {
		Data preAggregationSettingsResponse `json:"data"` // decoded administrator response payload
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.True(t, envelope.Data.Settings.Usage.Enabled)
	require.Equal(t, 60, envelope.Data.Settings.Usage.IntervalSeconds)
	require.True(t, envelope.Data.Settings.Ops.Enabled)
}

// TestSettingHandlerUpdatePreAggregationSettingsRejectsInvalidInterval submits
// a usage interval below the accepted range. HTTP returns bad request and leaves
// the unified runtime setting absent from the fixture storage.
func TestSettingHandlerUpdatePreAggregationSettingsRejectsInvalidInterval(t *testing.T) {
	repo := &preAggregationHandlerRepoStub{values: map[string]string{}}
	handler := newPreAggregationHandler(repo)
	router := gin.New()
	router.PUT("/pre-aggregation", handler.UpdatePreAggregationSettings)

	recorder := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"usage":{"enabled":true,"interval_seconds":10},"ops":{"enabled":true}}`)
	request := httptest.NewRequest(http.MethodPut, "/pre-aggregation", body)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	_, persisted := repo.values[preaggregation.SettingKeyPreAggregationSettings]
	require.False(t, persisted)
}

// TestSettingHandlerUpdatePreAggregationSettingsPersistsUnifiedValue submits
// usage cadence and an Ops disable switch together. The request succeeds and
// saves their exact combined configuration under one unified setting key.
func TestSettingHandlerUpdatePreAggregationSettingsPersistsUnifiedValue(t *testing.T) {
	repo := &preAggregationHandlerRepoStub{values: map[string]string{}}
	handler := newPreAggregationHandler(repo)
	router := gin.New()
	router.PUT("/pre-aggregation", handler.UpdatePreAggregationSettings)

	recorder := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"usage":{"enabled":true,"interval_seconds":90},"ops":{"enabled":false}}`)
	request := httptest.NewRequest(http.MethodPut, "/pre-aggregation", body)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var persisted preaggregation.PreAggregationSettings
	require.NoError(t, json.Unmarshal([]byte(repo.values[preaggregation.SettingKeyPreAggregationSettings]), &persisted))
	require.True(t, persisted.Usage.Enabled)
	require.Equal(t, 90, persisted.Usage.IntervalSeconds)
	require.False(t, persisted.Ops.Enabled)
	require.Len(t, repo.values, 1)
}

// Get reports ErrSettingNotFound for every fixture record lookup, leaving
// GetValue to supply the raw values used by these controller tests. The context
// is ignored and no saved-record object is constructed.
func (s *preAggregationHandlerRepoStub) Get(context.Context, string) (*settingscore.Setting, error) {
	return nil, settingscore.ErrSettingNotFound
}

// GetValue returns fixture text or ErrSettingNotFound for an absent key.
// Contexts are ignored, and the handler tests keep the unsynchronized map within
// their synchronous request and response assertions.
func (s *preAggregationHandlerRepoStub) GetValue(_ context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", settingscore.ErrSettingNotFound
	}
	return value, nil
}

// Set creates fixture storage when needed and saves the raw value. The
// fixture ignores context cancellation and injects no write failure, allowing
// HTTP tests to inspect persistence after a synchronous controller update.
func (s *preAggregationHandlerRepoStub) Set(_ context.Context, key, value string) error {
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[key] = value
	return nil
}

// GetMultiple returns a new map with fixture values for the requested keys.
// Absent keys are omitted, contexts are ignored, and the returned map can be
// changed without replacing the fixture's saved entries.
func (s *preAggregationHandlerRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

// SetMultiple delegates each entry to the fixture's Set implementation in map
// iteration order. It stops on a returned error; these fixtures supply no failing
// Set path and model synchronous saved values for the HTTP tests.
func (s *preAggregationHandlerRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	for key, value := range settings {
		if err := s.Set(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

// GetAll copies saved fixture entries into a new map for synchronous request
// assertions. Contexts are ignored, and callers can change this returned map
// while retaining the fixture's existing string entries.
func (s *preAggregationHandlerRepoStub) GetAll(context.Context) (map[string]string, error) {
	result := make(map[string]string, len(s.values))
	maps.Copy(result, s.values)
	return result, nil
}

// Delete removes a fixture key in place and accepts absence. The context
// is ignored and this fixture injects no deletion failure, keeping cleanup
// synchronous for these preaggregation controller requests.
func (s *preAggregationHandlerRepoStub) Delete(_ context.Context, key string) error {
	delete(s.values, key)
	return nil
}

// newPreAggregationHandler binds enabled deployment defaults with a sixty-second
// usage interval and 31-day manual backfill permission. Task readers are nil,
// so these fixtures exercise settings responses and updates rather than triggering work.
func newPreAggregationHandler(repo settingscore.Repository) *PreAggregationHandler {
	options := &preaggregation.Options{Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60, BackfillEnabled: true, BackfillMaxDays: 31}, OpsEnabled: true, OpsAggregationEnabled: true}
	return NewPreAggregationHandler(preaggregation.NewPreAggregationSettingsService(repo, options), nil, nil)
}
