package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	gatewaysession "github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	moderationadapter "github.com/TokenFlux/TokenRouter/internal/moderation/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// liveModerationSettings supplies test settings while the moderation service and HTTP client execute normally.
type liveModerationSettings struct {
	settings.Repository
	values map[string]string
}

// liveModerationLogs supplies empty violation history for tests.
type liveModerationLogs struct {
	moderation.ContentModerationRepository
}

// newLiveModerationRuntime binds moderation to a local protocol client and waits for background work during test
// cleanup.
func newLiveModerationRuntime(t *testing.T, settings moderation.SettingRepository, repo moderation.ContentModerationRepository) *moderation.ContentModerationService {
	t.Helper()
	var background sync.WaitGroup
	t.Cleanup(background.Wait)
	core := moderation.NewContentModerationService(settings, repo, nil, nil, nil, nil, nil, moderation.Runtime{
		Audit:         moderationadapter.NewAuditClient(),
		SnapshotMedia: moderationadapter.SnapshotMedia,
		Background:    func(_ string, fn func()) { background.Go(fn) },
		CyberText:     openai.IsOpenAICyberWarningText,
		CyberPolicy:   openai.DetectOpenAICyberPolicy,
		ErrorMessage:  upstream.ExtractErrorMessage,
		MissingRow:    func(err error) bool { return errors.Is(err, sql.ErrNoRows) },
		MissingUser:   func(err error) bool { return errors.Is(err, identity.ErrUserNotFound) },
	})
	t.Cleanup(func() {
		if err := core.Stop(); err != nil {
			t.Errorf("停止审核运行时: %v", err)
		}
	})
	return core
}

func (s *liveModerationSettings) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", settings.ErrSettingNotFound
}

func (s *liveModerationSettings) Get(ctx context.Context, key string) (*settings.Setting, error) {
	v, e := s.GetValue(ctx, key)
	if e != nil {
		return nil, e
	}
	return &settings.Setting{Key: key, Value: v}, nil
}

func (s *liveModerationSettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := s.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (*liveModerationLogs) CreateLog(context.Context, *moderation.ContentModerationLog) error {
	return nil
}

func (*liveModerationLogs) CountFlaggedByUserSince(context.Context, int64, time.Time) (int, error) {
	return 0, nil
}

func TestParseLiveCallRequestMultipartPreservesSession(t *testing.T) {
	session := `{"model":"gpt-live-test","delegation":{"type":"client"},"instructions":"你好"}`
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("sdp", "v=0\r\n"))
	require.NoError(t, writer.WriteField("session", session))
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, "/v1/live", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	parsed, err := ParseLiveCallRequest(context)
	require.NoError(t, err)
	require.Equal(t, "v=0\r\n", parsed.SDP)
	require.JSONEq(t, session, string(parsed.Session))
	require.Equal(t, "client", jsonPathString(t, parsed.Session, "delegation", "type"))
}

func TestParseLiveCallRequestJSONPreservesSessionWithoutDelegation(t *testing.T) {
	body := `{"sdp":"v=0\\r\\n","session":{"model":"gpt-live-test","instructions":"standalone"}}`
	request := httptest.NewRequest(http.MethodPost, "/backend-api/codex/realtime/calls", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request

	parsed, err := ParseLiveCallRequest(context)
	require.NoError(t, err)
	require.NotContains(t, string(parsed.Session), "delegation")
	require.Equal(t, "standalone", jsonPathString(t, parsed.Session, "instructions"))
}

func TestParseLiveCallRequestRejectsInvalidJSONShape(t *testing.T) {
	testCases := []string{
		`{"session":{"type":"quicksilver"}}`,
		`{"sdp":"v=0\\r\\n","session":[]}`,
		`{"sdp":"v=0\\r\\n","session":null}`,
		`{"sdp":"v=0\\r\\n","session":{"type":"quicksilver"}} {}`,
	}
	for _, body := range testCases {
		request := httptest.NewRequest(http.MethodPost, "/backend-api/codex/realtime/calls", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = request
		_, err := ParseLiveCallRequest(context)
		require.Error(t, err)
	}
}

func TestLiveSidebandLocationMatchesCreateRoute(t *testing.T) {
	require.Equal(t, "/v1/live/call_123", LiveSidebandLocation("/v1/live", "call_123"))
	require.Equal(
		t,
		"/backend-api/codex/call_123",
		LiveSidebandLocation("/backend-api/codex/realtime/calls", "call_123"),
	)
}

func TestLiveEnabledForAPIKey(t *testing.T) {
	require.False(t, liveEnabledForAPIKey(nil))
	require.False(t, liveEnabledForAPIKey(&LiveAPIKey{}))
	require.False(t, liveEnabledForAPIKey(&LiveAPIKey{
		Group: &LiveGroup{},
	}))
	require.True(t, liveEnabledForAPIKey(&LiveAPIKey{
		Group: &LiveGroup{AllowLive: true},
	}))
	require.True(t, liveEnabledForAPIKey(&LiveAPIKey{
		Group: &LiveGroup{AllowLive: true},
	}))
}

func TestLiveContentModerationBlocksBeforeBilling(t *testing.T) {
	moderationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/moderations", r.URL.Path)
		_, _ = w.Write([]byte(`{"results":[{"category_scores":{"sexual":0.9}}]}`))
	}))
	defer moderationServer.Close()

	cfg := &moderation.ContentModerationConfig{
		Enabled:      true,
		Mode:         moderation.ContentModerationModePreBlock,
		BaseURL:      moderationServer.URL,
		Model:        "omni-moderation-latest",
		APIKeys:      []string{"sk-test"},
		SampleRate:   100,
		AllGroups:    true,
		BlockMessage: "Live 内容审核测试阻断",
	}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	moderationSvc := newLiveModerationRuntime(t, &liveModerationSettings{values: map[string]string{
		moderation.SettingKeyRiskControlEnabled:      "true",
		moderation.SettingKeyContentModerationConfig: string(rawCfg),
	}},
		&liveModerationLogs{},
	)
	moderationSvc.Start()

	groupID := int64(2)
	group := &routing.Group{ID: groupID, Name: "live", AllowLive: true}
	apiKey := &apikey.APIKey{
		ID:      101,
		Name:    "live-test-key",
		GroupID: &groupID,
		Group:   group,
		UserID:  1,
		User:    &identity.User{ID: 1},
	}
	body := bytes.NewBufferString(`{"sdp":"v=0\\r\\n","session":{"model":"gpt-live-test","input":"bad prompt"}}`)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/live", body)
	context.Request.Header.Set("Content-Type", "application/json")
	context.Set(string(keyhttp.ContextKeyAPIKey), apiKey)
	context.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1, Concurrency: 1})

	// Omit funding services so the forbidden response proves moderation blocks before billing checks.
	NewLiveHandler(LivePorts{Moderation: moderationSvc}).Live(context)

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "content_policy_violation")
	require.Contains(t, recorder.Body.String(), "Live 内容审核测试阻断")
	requestType, exists := context.Get("ops_request_type")
	require.True(t, exists)
	require.Equal(t, int16(usage.RequestTypeLive), requestType)
}

func TestLiveAttestationErrorIsExplicit(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)

	NewLiveHandler(LivePorts{}).WriteLiveCreateError(context, &gatewaysession.LiveAttestationUnavailableError{
		Reason: "Live attestation is only supported when TokenRouter runs on macOS",
	})

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), "TokenRouter runs on macOS")
}

func jsonPathString(t *testing.T, raw json.RawMessage, keys ...string) string {
	t.Helper()
	var value any
	require.NoError(t, json.Unmarshal(raw, &value))
	current := value
	for _, key := range keys {
		object, ok := current.(map[string]any)
		require.True(t, ok)
		current = object[key]
	}
	result, ok := current.(string)
	require.True(t, ok)
	return result
}
