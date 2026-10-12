package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	opsprovider "github.com/TokenFlux/TokenRouter/internal/ops/provider"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

type testSettingRepo struct {
	values map[string]string
}

func TestOpsRuntimeLoggingHandler_GetConfig(t *testing.T) {
	h := NewOpsHandler(newRuntimeOpsService(t))
	r := newOpsRuntimeRouter(h, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/runtime/logging", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}
}

func TestOpsRuntimeLoggingHandler_UpdateUnauthorized(t *testing.T) {
	h := NewOpsHandler(newRuntimeOpsService(t))
	r := newOpsRuntimeRouter(h, false)

	body := `{"level":"debug","enable_sampling":false,"sampling_initial":100,"sampling_thereafter":100,"caller":true,"stacktrace_level":"error","retention_days":30}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/runtime/logging", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", w.Code)
	}
}

func TestOpsRuntimeLoggingHandler_UpdateAndResetSuccess(t *testing.T) {
	h := NewOpsHandler(newRuntimeOpsService(t))
	r := newOpsRuntimeRouter(h, true)

	payload := map[string]any{
		"level":               "debug",
		"enable_sampling":     false,
		"sampling_initial":    100,
		"sampling_thereafter": 100,
		"caller":              true,
		"stacktrace_level":    "error",
		"retention_days":      30,
	}
	raw, _ := json.Marshal(payload)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/runtime/logging", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/runtime/logging/reset", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reset status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
}

// TestEmailReportRejectsLegacyProviderFields checks rejection of account_health_enabled and acceptance of provider_health_enabled.
func TestEmailReportRejectsLegacyProviderFields(t *testing.T) {
	handler := &OpsHandler{opsService: newRuntimeOpsService(t)}
	router := gin.New()
	router.PUT("/config", handler.UpdateEmailNotificationConfig)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"report":{"account_health_enabled":false}}`, http.StatusBadRequest},
		{`{"report":{"provider_health_enabled":false}}`, http.StatusOK},
	} {
		request := httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(tc.body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
	}
}

func newTestSettingRepo() *testSettingRepo {
	return &testSettingRepo{values: map[string]string{}}
}

func (s *testSettingRepo) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	v, err := s.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &settingscore.Setting{Key: key, Value: v}, nil
}

func (s *testSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	v, ok := s.values[key]
	if !ok {
		return "", settingscore.ErrSettingNotFound
	}
	return v, nil
}

func (s *testSettingRepo) Set(ctx context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func (s *testSettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if v, ok := s.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (s *testSettingRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	maps.Copy(s.values, settings)
	return nil
}

func (s *testSettingRepo) GetAll(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.values))
	maps.Copy(out, s.values)
	return out, nil
}

func (s *testSettingRepo) Delete(ctx context.Context, key string) error {
	delete(s.values, key)
	return nil
}

func newOpsRuntimeRouter(handler *OpsHandler, withUser bool) *gin.Engine {
	r := gin.New()
	if withUser {
		r.Use(func(c *gin.Context) {
			c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 7})
			c.Next()
		})
	}
	r.GET("/runtime/logging", handler.GetRuntimeLogConfig)
	r.PUT("/runtime/logging", handler.UpdateRuntimeLogConfig)
	r.POST("/runtime/logging/reset", handler.ResetRuntimeLogConfig)
	return r
}

func newRuntimeOpsService(t *testing.T) *ops.OpsService {
	t.Helper()
	if err := logging.Init(logging.InitOptions{
		Level:       "info",
		Format:      "json",
		ServiceName: "tokenrouter",
		Environment: "test",
		Output: logging.OutputOptions{
			ToStdout: false,
			ToFile:   false,
		},
	}); err != nil {
		t.Fatalf("init logger: %v", err)
	}

	settingRepo := newTestSettingRepo()
	cfg := &ops.Options{
		Ops: ops.RuntimeOptions{Enabled: true},
		Log: ops.LogOptions{
			Level:           "info",
			Caller:          true,
			StacktraceLevel: "error",
			Sampling: ops.SamplingOptions{
				Enabled:    false,
				Initial:    100,
				Thereafter: 100,
			},
		},
	}
	return ops.NewOpsService(nil, settingRepo, cfg, nil, nil, nil, nil, opsprovider.LogControl{})
}
