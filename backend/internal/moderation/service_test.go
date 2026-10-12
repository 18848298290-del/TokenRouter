package moderation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	moderationadapter "github.com/TokenFlux/TokenRouter/internal/moderation/provider"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// contentModerationTestProxyRepo counts proxy lookups made by the moderation service.
type contentModerationTestProxyRepo struct {
	proxies    map[int64]*Proxy
	getByIDErr error
	getCalls   atomic.Int64
}

type contentModerationRuntimeSettingRepo struct {
	mu               sync.Mutex
	values           map[string]string
	getValueCalls    int
	getMultipleCalls int
	getMultipleErr   error
	getMultipleStart chan<- struct{}
	getMultipleWait  <-chan struct{}
}

type contentModerationTestSettingRepo struct {
	values map[string]string
}

type contentModerationTestRepo struct {
	mu            sync.Mutex
	logs          []ContentModerationLog
	cyberWarnings []ContentModerationCyberWarning
}

type contentModerationTestHashCache struct {
	mu            sync.Mutex
	hashes        map[string]struct{}
	recorded      []string
	checked       []string
	deleted       []string
	hasResult     bool
	hasResultUsed bool
}

type contentModerationTestUserRepo struct {
	user         *User
	updated      []User
	requestedIDs []int64
}

type contentModerationTestAuthCacheInvalidator struct {
	userIDs []int64
}

type (
	User             = UserSnapshot
	Proxy            = egress.Proxy
	UserUpdateFields struct{ Status bool }
)

func TestSplitContentModerationTextKeepsOverlapAndTail(t *testing.T) {
	chunks := splitContentModerationText("abcdefghij", 6, 2)

	require.Equal(t, []string{"abcdef", "efghij"}, chunks)
}

func TestContentModerationTextBatchingDoesNotCreateOverlapOnlyTail(t *testing.T) {
	var batches [][]string
	forEachContentModerationTextBatch("abcdef", 6, 2, 2, func(_ int, chunks []string) {
		batches = append(batches, append([]string(nil), chunks...))
	})

	require.Equal(t, [][]string{{"abcdef"}}, batches)
	require.Equal(t, 1, countContentModerationTextChunks("abcdef", 6, 2))
	require.Equal(t, 3, countContentModerationTextChunks("abcdefghijk", 6, 2))
}

func TestContentModerationNormalizePreservesFullOriginalText(t *testing.T) {
	input := ContentModerationInput{Text: "  first line\nsecond line  "}

	input.Normalize()

	require.Equal(t, "  first line\nsecond line  ", input.Text)
	require.Equal(t, input.Text, input.Items[0].Text)
}

func TestContentModerationAuditScopeLimitsUpstreamButStoresFullInput(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inputs := decodeModerationTextInputs(t, r)
		received <- inputs[0]
		require.NoError(t, json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{CategoryScores: map[string]float64{"sexual": 0.01}}}}))
	}))
	defer server.Close()

	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	cfg.RetryCount = 0
	cfg.RecordNonHits = true
	cfg.AuditUserTextMaxChars = 5
	cfg.AuditToolOutputMaxChars = 4
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &contentModerationTestRepo{}
	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		&contentModerationTestHashCache{},
		nil, nil, nil, nil,
	)
	svc.Start()
	body := []byte(`{"messages":[{"role":"user","content":"old"},{"role":"assistant","content":"answer"},{"role":"tool","content":"tool-output"},{"role":"user","content":"user-content"}]}`)

	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{Protocol: ContentModerationProtocolOpenAIChat, Body: body})

	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Equal(t, "tool\nuser-", <-received)
	logs := requireContentModerationLogCount(t, repo, 1)
	require.Len(t, logs[0].InputItems, 2)
	require.Equal(t, "tool-output", logs[0].InputItems[0].Text)
	require.Equal(t, "user-content", logs[0].InputItems[1].Text)
}

func TestContentModerationEmptyAuditScopeSkipsObserveQueue(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModeObserve
	cfg.AuditToolOutputs = false
	cfg.AuditImages = false
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &contentModerationTestRepo{}
	svc := &ContentModerationService{
		runtime: testModerationRuntime(),
		settingRepo: &contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo:       repo,
		asyncQueue: make(chan contentModerationTask, 1),
	}
	body := []byte(`{"messages":[{"role":"assistant","content":"answer"},{"role":"tool","content":"tool-output"}]}`)

	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{Protocol: ContentModerationProtocolOpenAIChat, Body: body})

	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Empty(t, svc.asyncQueue)
	require.Empty(t, repo.snapshotLogs())
}

func TestContentModerationWeightedAPIKeySelectionHonorsPriorityAndFreeze(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.APIKeys = []string{"sk-high", "sk-low"}
	cfg.APIKeyMetadata = []ContentModerationAPIKeyMetadata{
		{KeyHash: moderationAPIKeyHash("sk-high"), Priority: 100, Note: "Tier 5"},
		{KeyHash: moderationAPIKeyHash("sk-low"), Priority: 20, Note: "Tier 1"},
	}
	cfg.normalize()
	svc := newTestModeration(nil, nil, nil, nil, nil, nil, nil)
	svc.Start()
	counts := map[string]int{}
	for range 60 {
		key, ok := svc.nextUsableAPIKey(cfg)
		require.True(t, ok)
		counts[key]++
	}
	require.Equal(t, 50, counts["sk-high"])
	require.Equal(t, 10, counts["sk-low"])

	svc.keyHealthMu.Lock()
	svc.keyHealth[moderationAPIKeyHash("sk-high")] = &contentModerationKeyHealth{FrozenUntil: time.Now().Add(time.Minute)}
	svc.keyHealthMu.Unlock()
	for range 10 {
		key, ok := svc.nextUsableAPIKey(cfg)
		require.True(t, ok)
		require.Equal(t, "sk-low", key)
	}
}

func TestContentModerationUpdateConfigPersistsKeyMetadataAndAuditScope(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.APIKeys = []string{"sk-high", "sk-low"}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &contentModerationTestSettingRepo{values: map[string]string{SettingKeyContentModerationConfig: string(rawCfg)}}
	svc := newTestModeration(repo, nil, nil, nil, nil, nil, nil)
	svc.Start()
	updates := []ContentModerationAPIKeyMetadata{
		{KeyHash: moderationAPIKeyHash("sk-high"), Priority: 100, Note: "Tier 5"},
		{KeyHash: moderationAPIKeyHash("sk-low"), Priority: 20, Note: "Tier 1"},
	}
	userLimit := 8000
	toolLimit := 2000
	auditImages := false
	auditTools := false

	view, err := svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{
		APIKeyUpdates:           &updates,
		AuditUserTextMaxChars:   &userLimit,
		AuditImages:             &auditImages,
		AuditToolOutputs:        &auditTools,
		AuditToolOutputMaxChars: &toolLimit,
	})

	require.NoError(t, err)
	require.Equal(t, 8000, view.AuditUserTextMaxChars)
	require.False(t, view.AuditImages)
	require.False(t, view.AuditToolOutputs)
	require.Equal(t, 2000, view.AuditToolOutputMaxChars)
	require.Equal(t, 100, view.APIKeyStatuses[0].Priority)
	require.Equal(t, "Tier 5", view.APIKeyStatuses[0].Note)
	require.Equal(t, 20, view.APIKeyStatuses[1].Priority)

	var saved ContentModerationConfig
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyContentModerationConfig]), &saved))
	require.Equal(t, updates, saved.APIKeyMetadata)
}

func TestContentModerationUpdateConfigAddsKeyWithPriorityAndNote(t *testing.T) {
	cfg := defaultContentModerationConfig()
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &contentModerationTestSettingRepo{values: map[string]string{SettingKeyContentModerationConfig: string(rawCfg)}}
	svc := newTestModeration(repo, nil, nil, nil, nil, nil, nil)
	svc.Start()
	entries := []ContentModerationAPIKeyEntryInput{{APIKey: "sk-tier-five", Priority: 250, Note: "Tier 5 primary"}}

	view, err := svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{APIKeyEntries: &entries})

	require.NoError(t, err)
	require.Equal(t, 1, view.APIKeyCount)
	require.Equal(t, 250, view.APIKeyStatuses[0].Priority)
	require.Equal(t, "Tier 5 primary", view.APIKeyStatuses[0].Note)
	var saved ContentModerationConfig
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyContentModerationConfig]), &saved))
	require.Equal(t, []string{"sk-tier-five"}, saved.APIKeys)
	require.Equal(t, []ContentModerationAPIKeyMetadata{{
		KeyHash: moderationAPIKeyHash("sk-tier-five"), Priority: 250, Note: "Tier 5 primary",
	}}, saved.APIKeyMetadata)
}

func TestContentModerationAsyncQueueDropsDuplicateBodyAndTracksBytes(t *testing.T) {
	svc := &ContentModerationService{runtime: testModerationRuntime(), asyncQueue: make(chan contentModerationTask, 1)}
	content := ContentModerationInput{
		Text:  "完整文本",
		Items: []ContentModerationInputItem{{Index: 0, Source: ContentModerationSourceUser, Type: ContentModerationItemTypeText, Text: "完整文本"}},
	}
	svc.enqueueAsync(ContentModerationCheckInput{Endpoint: "/v1/chat/completions", Body: []byte("raw body")}, &ContentModerationConfig{QueueSize: 1}, content, "hash")

	task := <-svc.asyncQueue
	require.Nil(t, task.input.Body)
	require.Empty(t, task.content.Text)
	require.Equal(t, "完整文本", contentModerationTextFromItems(task.content.Items))
	require.Positive(t, task.bufferedBytes)
	require.Equal(t, task.bufferedBytes, svc.asyncBufferedBytes.Load())
	svc.releaseContentModerationBufferedBytes(task.bufferedBytes)
	require.Zero(t, svc.asyncBufferedBytes.Load())
}

func TestContentModerationAsyncQueueRejectsTaskBeyondByteBudget(t *testing.T) {
	svc := &ContentModerationService{runtime: testModerationRuntime(), asyncQueue: make(chan contentModerationTask, 1)}
	svc.asyncBufferedBytes.Store(maxContentModerationBufferedBytes - 1)
	content := ContentModerationInput{Text: "text", Items: []ContentModerationInputItem{{Type: ContentModerationItemTypeText, Text: "text"}}}

	svc.enqueueAsync(ContentModerationCheckInput{}, &ContentModerationConfig{QueueSize: 1}, content, "hash")

	require.Empty(t, svc.asyncQueue)
	require.Equal(t, maxContentModerationBufferedBytes-1, svc.asyncBufferedBytes.Load())
	require.Equal(t, int64(1), svc.asyncDropped.Load())
}

func TestContentModerationObserveQueueOverflowRecordsIncompleteAudit(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModeObserve
	cfg.QueueSize = 1
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &contentModerationTestRepo{}
	svc := &ContentModerationService{
		runtime: testModerationRuntime(),
		settingRepo: &contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo:       repo,
		asyncQueue: make(chan contentModerationTask, 1),
	}
	svc.asyncQueue <- contentModerationTask{}

	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		Protocol: ContentModerationProtocolOpenAIChat,
		Body:     []byte(`{"messages":[{"role":"user","content":"audit me"}]}`),
	})

	require.NoError(t, err)
	require.True(t, decision.Allowed)
	logs := requireContentModerationLogCount(t, repo, 1)
	require.Equal(t, ContentModerationActionError, logs[0].Action)
	require.False(t, logs[0].AuditComplete)
	require.Equal(t, ContentModerationItemTypeRequest, logs[0].FailedUnits[0].Type)
	require.Equal(t, "audit me", logs[0].InputItems[0].Text)
}

func TestContentModerationCheckAuditsTextBeyondFirstChunkAndStoresFullInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inputs := decodeModerationTextInputs(t, r)
		results := make([]moderationAPIResult, 0, len(inputs))
		for _, input := range inputs {
			score := 0.01
			if strings.Contains(input, "tail-risk") {
				score = 0.9
			}
			results = append(results, moderationAPIResult{CategoryScores: map[string]float64{"sexual": score}})
		}
		require.NoError(t, json.NewEncoder(w).Encode(moderationAPIResponse{Results: results}))
	}))
	defer server.Close()

	text := strings.Repeat("a", maxModerationInputRunes+100) + "tail-risk"
	svc, repo := newExpandedModerationTestService(t, server.URL, false)
	body, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": text}}})
	require.NoError(t, err)

	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{Protocol: ContentModerationProtocolOpenAIChat, Body: body})

	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, 0.9, decision.HighestScore)
	logs := requireContentModerationLogCount(t, repo, 1)
	require.True(t, logs[0].ContentComplete)
	require.True(t, logs[0].AuditComplete)
	require.Equal(t, 2, logs[0].TextUnitCount)
	require.Equal(t, 0.9, logs[0].CategoryScores["sexual"])
	require.Equal(t, text, logs[0].InputItems[0].Text)
}

func TestContentModerationBatchFailureFallsBackToSingleTextUnits(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload struct {
			Input json.RawMessage `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		var batch []string
		if json.Unmarshal(payload.Input, &batch) == nil && len(batch) > 1 {
			http.Error(w, "batch unsupported", http.StatusBadRequest)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{CategoryScores: map[string]float64{"sexual": 0.01}}}}))
	}))
	defer server.Close()

	text := strings.Repeat("a", maxModerationInputRunes+100)
	svc, repo := newExpandedModerationTestService(t, server.URL, true)
	body, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": text}}})
	require.NoError(t, err)

	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{Protocol: ContentModerationProtocolOpenAIChat, Body: body})

	require.NoError(t, err)
	require.False(t, decision.Blocked)
	require.Equal(t, int64(3), calls.Load())
	logs := requireContentModerationLogCount(t, repo, 1)
	require.True(t, logs[0].AuditComplete)
	require.Equal(t, 2, logs[0].TextUnitCount)
}

func TestContentModerationTransientBatchFailureDoesNotFanOut(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	text := strings.Repeat("a", maxModerationInputRunes+100)
	svc, repo := newExpandedModerationTestService(t, server.URL, false)
	body, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": text}}})
	require.NoError(t, err)

	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{Protocol: ContentModerationProtocolOpenAIChat, Body: body})

	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Equal(t, int64(1), calls.Load())
	logs := requireContentModerationLogCount(t, repo, 1)
	require.False(t, logs[0].AuditComplete)
	require.Equal(t, 2, logs[0].FailedUnitCount)
}

func TestContentModerationCheckAuditsAllImagesAndSnapshotsOnlyHit(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw := decodeModerationRawInput(t, r)
		score := 0.01
		if strings.Contains(string(raw), "marker=flag") {
			score = 0.9
		}
		require.NoError(t, json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{CategoryScores: map[string]float64{"sexual": score}}}}))
	}))
	defer server.Close()

	imageData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M/wHwAF/gL+Z8dZAAAAAElFTkSuQmCC"
	first := "data:image/png;marker=pass;base64," + imageData
	second := "data:image/png;marker=flag;base64," + imageData
	body, err := json.Marshal(map[string]any{"messages": []any{map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": first}},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": second}},
		},
	}}})
	require.NoError(t, err)
	svc, repo := newExpandedModerationTestService(t, server.URL, false)

	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{Protocol: ContentModerationProtocolOpenAIChat, Body: body})

	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, int64(2), calls.Load())
	logs := requireContentModerationLogCount(t, repo, 1)
	require.Equal(t, 2, logs[0].ImageUnitCount)
	require.Len(t, logs[0].Media, 1)
	require.Equal(t, second, logs[0].Media[0].OriginalRef)
	require.Equal(t, "ready", logs[0].Media[0].SnapshotStatus)
	require.NotEmpty(t, logs[0].Media[0].Content)
}

func TestContentModerationPartialFailureStillBlocksAndRecordsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := decodeModerationRawInput(t, r)
		if strings.Contains(string(raw), "marker=fail") {
			http.Error(w, "image rejected", http.StatusBadRequest)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{CategoryScores: map[string]float64{"sexual": 0.9}}}}))
	}))
	defer server.Close()

	imageData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M/wHwAF/gL+Z8dZAAAAAElFTkSuQmCC"
	body, err := json.Marshal(map[string]any{"messages": []any{map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;marker=fail;base64," + imageData}},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;marker=flag;base64," + imageData}},
		},
	}}})
	require.NoError(t, err)
	svc, repo := newExpandedModerationTestService(t, server.URL, false)

	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{Protocol: ContentModerationProtocolOpenAIChat, Body: body})

	require.NoError(t, err)
	require.True(t, decision.Blocked)
	logs := requireContentModerationLogCount(t, repo, 1)
	require.True(t, logs[0].Flagged)
	require.False(t, logs[0].AuditComplete)
	require.Equal(t, 1, logs[0].FailedUnitCount)
	require.Equal(t, ContentModerationItemTypeImage, logs[0].FailedUnits[0].Type)
}

func TestContentModerationAllFailuresFailOpenAndAlwaysRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	svc, repo := newExpandedModerationTestService(t, server.URL, false)
	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		Protocol: ContentModerationProtocolOpenAIChat,
		Body:     []byte(`{"messages":[{"role":"user","content":"audit me"}]}`),
	})

	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.False(t, decision.Blocked)
	logs := requireContentModerationLogCount(t, repo, 1)
	require.Equal(t, ContentModerationActionError, logs[0].Action)
	require.False(t, logs[0].AuditComplete)
	require.Equal(t, 1, logs[0].FailedUnitCount)
}

// TestContentModerationCallRoutesThroughProxy verifies use of the configured proxy (#2646).
// The upstream hostname is deliberately unresolvable, so a response requires
// the local forward proxy to handle the request.
func TestContentModerationCallRoutesThroughProxy(t *testing.T) {
	var proxied atomic.Int64
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A forward proxy receives an absolute URI for an HTTP target.
		if !strings.HasPrefix(r.RequestURI, "http://moderation-proxy-test.invalid") {
			t.Errorf("expected absolute-URI proxy request, got %q", r.RequestURI)
		}
		proxied.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{Flagged: false}}})
	}))
	defer proxySrv.Close()

	proxyAddr := strings.TrimPrefix(proxySrv.URL, "http://")
	host, portStr, ok := strings.Cut(proxyAddr, ":")
	if !ok {
		t.Fatalf("unexpected proxy addr: %s", proxyAddr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse proxy port: %v", err)
	}

	proxyRepo := &contentModerationTestProxyRepo{proxies: map[int64]*Proxy{
		7: {ID: 7, Name: "audit-proxy", Protocol: "http", Host: host, Port: port, Status: StatusActive},
	}}
	svc := newTestModeration(nil, nil, nil, nil, nil, nil, nil)
	svc.Start()
	svc.SetProxyRepository(proxyRepo)

	cfg := defaultContentModerationConfig()
	cfg.BaseURL = "http://moderation-proxy-test.invalid"
	cfg.ProxyID = moderationProxyIDPtr(7)
	cfg.normalize()

	httpStatus := 0
	if _, err := svc.callModerationOnceWithInput(context.Background(), cfg, "sk-test", "hello", &httpStatus); err != nil {
		t.Fatalf("expected moderation call via proxy to succeed, got: %v", err)
	}
	if proxied.Load() == 0 {
		t.Fatal("expected request to be routed through the proxy server")
	}
}

// TestContentModerationProxyResolveFailureDoesNotFallBackToDirect checks that failed proxy lookup stops
// the request.
func TestContentModerationProxyResolveFailureDoesNotFallBackToDirect(t *testing.T) {
	var direct atomic.Int64
	directSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		direct.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{Flagged: false}}})
	}))
	defer directSrv.Close()

	proxyRepo := &contentModerationTestProxyRepo{getByIDErr: errors.New("proxy deleted")}
	svc := newTestModeration(nil, nil, nil, nil, nil, nil, nil)
	svc.Start()
	svc.SetProxyRepository(proxyRepo)

	cfg := defaultContentModerationConfig()
	cfg.BaseURL = directSrv.URL
	cfg.ProxyID = moderationProxyIDPtr(9)
	cfg.normalize()

	httpStatus := 0
	_, err := svc.callModerationOnceWithInput(context.Background(), cfg, "sk-test", "hello", &httpStatus)
	if err == nil || !strings.Contains(err.Error(), "resolve moderation proxy") {
		t.Fatalf("expected proxy resolve error, got: %v", err)
	}
	if direct.Load() != 0 {
		t.Fatal("must not fall back to direct connection when proxy resolution fails")
	}
}

// TestContentModerationProxyURLResolutionCached checks reuse of proxy lookups within the TTL.
func TestContentModerationProxyURLResolutionCached(t *testing.T) {
	proxyRepo := &contentModerationTestProxyRepo{proxies: map[int64]*Proxy{
		3: {ID: 3, Name: "p", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: StatusActive},
	}}
	svc := newTestModeration(nil, nil, nil, nil, nil, nil, nil)
	svc.Start()
	svc.SetProxyRepository(proxyRepo)

	for i := range 5 {
		if _, err := svc.resolveModerationProxyURL(context.Background(), 3); err != nil {
			t.Fatalf("resolve attempt %d failed: %v", i, err)
		}
	}
	if got := proxyRepo.getCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 repository lookup thanks to caching, got %d", got)
	}
}

// TestContentModerationUpdateConfigProxyIDSemantics checks proxy selection during configuration updates.
// Positive IDs select a proxy, nil leaves it unchanged, and nonpositive IDs clear it.
func TestContentModerationUpdateConfigProxyIDSemantics(t *testing.T) {
	settingRepo := &contentModerationTestSettingRepo{values: map[string]string{}}
	proxyRepo := &contentModerationTestProxyRepo{proxies: map[int64]*Proxy{
		5: {ID: 5, Name: "p", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: StatusActive},
	}}
	svc := newTestModeration(settingRepo, nil, nil, nil, nil, nil, nil)
	svc.Start()
	svc.SetProxyRepository(proxyRepo)
	ctx := context.Background()

	view, err := svc.UpdateConfig(ctx, UpdateContentModerationConfigInput{ProxyID: moderationProxyIDPtr(5)})
	if err != nil {
		t.Fatalf("set proxy_id=5: %v", err)
	}
	if view.ProxyID == nil || *view.ProxyID != 5 {
		t.Fatalf("expected proxy_id=5 in view, got %v", view.ProxyID)
	}

	// Omitting ProxyID leaves the selected proxy unchanged.
	enabled := true
	view, err = svc.UpdateConfig(ctx, UpdateContentModerationConfigInput{Enabled: &enabled})
	if err != nil {
		t.Fatalf("update unrelated field: %v", err)
	}
	if view.ProxyID == nil || *view.ProxyID != 5 {
		t.Fatalf("expected proxy_id to stay 5 when omitted, got %v", view.ProxyID)
	}

	// Zero clears the proxy and restores direct connections.
	view, err = svc.UpdateConfig(ctx, UpdateContentModerationConfigInput{ProxyID: moderationProxyIDPtr(0)})
	if err != nil {
		t.Fatalf("clear proxy_id: %v", err)
	}
	if view.ProxyID != nil {
		t.Fatalf("expected proxy_id cleared, got %v", *view.ProxyID)
	}

	// A nonexistent proxy ID fails validation.
	if _, err := svc.UpdateConfig(ctx, UpdateContentModerationConfigInput{ProxyID: moderationProxyIDPtr(404)}); err == nil {
		t.Fatal("expected validation error for nonexistent proxy")
	}
}

// TestContentModerationTestAPIKeysProxySemantics checks proxy selection for key tests.
// Nil uses the saved proxy, zero connects directly, and a positive ID selects a proxy.
func TestContentModerationTestAPIKeysProxySemantics(t *testing.T) {
	var proxied atomic.Int64
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{Flagged: false}}})
	}))
	defer proxySrv.Close()
	proxyAddr := strings.TrimPrefix(proxySrv.URL, "http://")
	host, portStr, _ := strings.Cut(proxyAddr, ":")
	port, _ := strconv.Atoi(portStr)

	savedCfg := defaultContentModerationConfig()
	savedCfg.BaseURL = "http://moderation-proxy-test.invalid"
	savedCfg.ProxyID = moderationProxyIDPtr(7)
	savedCfg.APIKeys = []string{"sk-saved"}
	rawCfg, err := json.Marshal(savedCfg)
	if err != nil {
		t.Fatalf("marshal cfg: %v", err)
	}

	settingRepo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyContentModerationConfig: string(rawCfg),
	}}
	proxyRepo := &contentModerationTestProxyRepo{proxies: map[int64]*Proxy{
		7: {ID: 7, Name: "audit-proxy", Protocol: "http", Host: host, Port: port, Status: StatusActive},
	}}
	svc := newTestModeration(settingRepo, nil, nil, nil, nil, nil, nil)
	svc.Start()
	svc.SetProxyRepository(proxyRepo)

	// An omitted ProxyID sends the key test through the saved proxy.
	result, err := svc.TestAPIKeys(context.Background(), TestContentModerationAPIKeysInput{APIKeys: []string{"sk-input"}})
	if err != nil {
		t.Fatalf("test with saved proxy: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Status == "error" {
		t.Fatalf("expected key test via proxy to succeed, got %+v", result.Items)
	}
	if proxied.Load() == 0 {
		t.Fatal("expected test request to route through the saved proxy")
	}

	// Zero selects a direct connection to the local upstream server.
	var direct atomic.Int64
	directSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		direct.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{Flagged: false}}})
	}))
	defer directSrv.Close()

	before := proxied.Load()
	result, err = svc.TestAPIKeys(context.Background(), TestContentModerationAPIKeysInput{
		APIKeys: []string{"sk-input"},
		BaseURL: directSrv.URL,
		ProxyID: moderationProxyIDPtr(0),
	})
	if err != nil {
		t.Fatalf("test with forced direct: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Status == "error" {
		t.Fatalf("expected forced-direct test to succeed, got %+v", result.Items)
	}
	if direct.Load() == 0 {
		t.Fatal("expected forced-direct test to reach the base URL directly")
	}
	if proxied.Load() != before {
		t.Fatal("forced-direct test must not route through the proxy")
	}
}

func TestContentModerationRuntimeSnapshotCachesSettings(t *testing.T) {
	repo := &contentModerationRuntimeSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: runtimeCacheTestConfig(t, "blocked"),
	}}
	svc := runtimeCacheTestService(repo, time.Hour)

	for range 20 {
		decision, err := svc.Check(context.Background(), runtimeCacheTestInput("clean prompt"))
		require.NoError(t, err)
		require.True(t, decision.Allowed)
	}

	getValue, getMultiple := repo.calls()
	require.Zero(t, getValue)
	require.Equal(t, 1, getMultiple)
}

func TestContentModerationRuntimeSnapshotUpdateConfigIsImmediate(t *testing.T) {
	repo := &contentModerationRuntimeSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: runtimeCacheTestConfig(t, "old-keyword"),
	}}
	svc := runtimeCacheTestService(repo, time.Hour)

	decision, err := svc.Check(context.Background(), runtimeCacheTestInput("new-keyword"))
	require.NoError(t, err)
	require.True(t, decision.Allowed)

	keywords := []string{"new-keyword"}
	_, err = svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{
		BlockedKeywords: &keywords,
	})
	require.NoError(t, err)

	decision, err = svc.Check(context.Background(), runtimeCacheTestInput("new-keyword"))
	require.NoError(t, err)
	require.True(t, decision.Blocked)

	_, getMultiple := repo.calls()
	require.Equal(t, 1, getMultiple)
}

func TestContentModerationRuntimeSnapshotUpdateWinsOverInitialLoad(t *testing.T) {
	repo := &contentModerationRuntimeSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: runtimeCacheTestConfig(t, "old-keyword"),
	}}
	svc := runtimeCacheTestService(repo, time.Hour)

	refreshStarted := make(chan struct{}, 1)
	releaseRefresh := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseRefresh)
		}
	}()
	repo.blockNextMultiple(refreshStarted, releaseRefresh)

	initialCheckDone := make(chan error, 1)
	go func() {
		decision, err := svc.Check(context.Background(), runtimeCacheTestInput("clean prompt"))
		if err == nil && (decision == nil || !decision.Allowed) {
			err = errors.New("unexpected initial moderation decision")
		}
		initialCheckDone <- err
	}()
	require.Eventually(t, func() bool {
		select {
		case <-refreshStarted:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)

	updateDone := make(chan error, 1)
	go func() {
		keywords := []string{"new-keyword"}
		_, updateErr := svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{
			BlockedKeywords: &keywords,
		})
		updateDone <- updateErr
	}()
	select {
	case updateErr := <-updateDone:
		require.NoError(t, updateErr)
		t.Fatal("configuration update completed before the initial load released its lock")
	case <-time.After(10 * time.Millisecond):
	}

	close(releaseRefresh)
	released = true
	require.NoError(t, <-initialCheckDone)
	require.NoError(t, <-updateDone)

	decision, err := svc.Check(context.Background(), runtimeCacheTestInput("new-keyword"))
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	decision, err = svc.Check(context.Background(), runtimeCacheTestInput("old-keyword"))
	require.NoError(t, err)
	require.True(t, decision.Allowed)
}

func TestContentModerationRuntimeSnapshotRefreshFailureKeepsStaleConfig(t *testing.T) {
	repo := &contentModerationRuntimeSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: runtimeCacheTestConfig(t, "blocked"),
	}}
	svc := runtimeCacheTestService(repo, time.Nanosecond)
	input := runtimeCacheTestInput("blocked")

	decision, err := svc.Check(context.Background(), input)
	require.NoError(t, err)
	require.True(t, decision.Blocked)

	repo.failMultiple(errors.New("database unavailable"))
	decision, err = svc.Check(context.Background(), input)
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Eventually(t, func() bool {
		_, calls := repo.calls()
		return calls >= 2
	}, time.Second, time.Millisecond)
}

func TestContentModerationRuntimeSnapshotRefreshFailureBacksOff(t *testing.T) {
	repo := &contentModerationRuntimeSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: runtimeCacheTestConfig(t, "blocked"),
	}}
	svc := runtimeCacheTestService(repo, time.Minute)
	input := runtimeCacheTestInput("blocked")

	decision, err := svc.Check(context.Background(), input)
	require.NoError(t, err)
	require.True(t, decision.Blocked)

	current := svc.runtimeSnapshot.Load()
	require.NotNil(t, current)
	expired := *current
	expired.loadedAt = time.Now().Add(-2 * time.Minute)
	svc.runtimeSnapshot.Store(&expired)
	repo.failMultiple(errors.New("database unavailable"))

	decision, err = svc.Check(context.Background(), input)
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Eventually(t, func() bool {
		_, calls := repo.calls()
		return calls == 2
	}, time.Second, time.Millisecond)

	for range 100 {
		decision, err = svc.Check(context.Background(), input)
		require.NoError(t, err)
		require.True(t, decision.Blocked)
	}
	_, calls := repo.calls()
	require.Equal(t, 2, calls)
}

func TestContentModerationRuntimeSnapshotRefreshReusesUnchangedMatcher(t *testing.T) {
	repo := &contentModerationRuntimeSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: runtimeCacheTestConfig(t, "blocked"),
	}}
	svc := runtimeCacheTestService(repo, time.Minute)
	input := runtimeCacheTestInput("blocked")

	decision, err := svc.Check(context.Background(), input)
	require.NoError(t, err)
	require.True(t, decision.Blocked)

	current := svc.runtimeSnapshot.Load()
	require.NotNil(t, current)
	expired := *current
	expired.loadedAt = time.Now().Add(-2 * time.Minute)
	svc.runtimeSnapshot.Store(&expired)

	decision, err = svc.Check(context.Background(), input)
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Eventually(t, func() bool {
		refreshed := svc.runtimeSnapshot.Load()
		return refreshed != nil && refreshed.loadedAt.After(expired.loadedAt)
	}, time.Second, time.Millisecond)

	refreshed := svc.runtimeSnapshot.Load()
	require.Same(t, current.config, refreshed.config)
	require.Same(t, current.keywordMatcher, refreshed.keywordMatcher)
	_, calls := repo.calls()
	require.Equal(t, 2, calls)
}

func TestContentModerationRuntimeSnapshotUpdateWinsOverInFlightRefresh(t *testing.T) {
	repo := &contentModerationRuntimeSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: runtimeCacheTestConfig(t, "old-keyword"),
	}}
	svc := runtimeCacheTestService(repo, time.Minute)

	decision, err := svc.Check(context.Background(), runtimeCacheTestInput("old-keyword"))
	require.NoError(t, err)
	require.True(t, decision.Blocked)

	current := svc.runtimeSnapshot.Load()
	require.NotNil(t, current)
	expired := *current
	expired.loadedAt = time.Now().Add(-2 * time.Minute)
	svc.runtimeSnapshot.Store(&expired)

	refreshStarted := make(chan struct{}, 1)
	releaseRefresh := make(chan struct{})
	repo.blockNextMultiple(refreshStarted, releaseRefresh)
	decision, err = svc.Check(context.Background(), runtimeCacheTestInput("clean prompt"))
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Eventually(t, func() bool {
		select {
		case <-refreshStarted:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)

	updateDone := make(chan error, 1)
	go func() {
		keywords := []string{"new-keyword"}
		_, updateErr := svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{
			BlockedKeywords: &keywords,
		})
		updateDone <- updateErr
	}()
	select {
	case updateErr := <-updateDone:
		require.NoError(t, updateErr)
		t.Fatal("configuration update completed before the in-flight refresh released its lock")
	case <-time.After(10 * time.Millisecond):
	}

	close(releaseRefresh)
	require.NoError(t, <-updateDone)
	decision, err = svc.Check(context.Background(), runtimeCacheTestInput("new-keyword"))
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	decision, err = svc.Check(context.Background(), runtimeCacheTestInput("old-keyword"))
	require.NoError(t, err)
	require.True(t, decision.Allowed)
}

func TestContentModerationRuntimeSnapshotConcurrentReadAndReplace(t *testing.T) {
	repo := &contentModerationRuntimeSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: runtimeCacheTestConfig(t, "blocked-0"),
	}}
	svc := runtimeCacheTestService(repo, time.Hour)
	_, err := svc.Check(context.Background(), runtimeCacheTestInput("clean prompt"))
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			for range 100 {
				decision, checkErr := svc.Check(context.Background(), runtimeCacheTestInput("clean prompt"))
				if checkErr != nil {
					errs <- checkErr
					return
				}
				if decision == nil || !decision.Allowed {
					errs <- errors.New("unexpected moderation decision")
					return
				}
			}
		})
	}
	for i := 1; i <= 20; i++ {
		keywords := []string{"blocked-" + time.Duration(i).String()}
		_, err := svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{
			BlockedKeywords: &keywords,
		})
		require.NoError(t, err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func TestBuildContentModerationLog_RedactsInputExcerpt(t *testing.T) {
	svc := &ContentModerationService{runtime: testModerationRuntime()}
	cfg := defaultContentModerationConfig()
	input := ContentModerationCheckInput{
		RequestID: "req-1",
		Endpoint:  "/v1/chat/completions",
		Provider:  "openai",
	}

	log := svc.buildTextLogForTest(input, cfg, ContentModerationActionAllow, true, "sexual", 0.8, map[string]float64{"sexual": 0.8}, "hello sk-proj-1234567890abcdef", nil, nil, "")

	require.NotContains(t, log.InputExcerpt, "sk-proj-1234567890abcdef")
	require.Contains(t, log.InputExcerpt, "[已脱敏]")
}

func TestContentModerationConfigNormalize_NonHitRetentionMaxThreeDays(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.NonHitRetentionDays = 30

	cfg.normalize()

	require.Equal(t, 3, cfg.NonHitRetentionDays)
}

func TestNormalizeBlockedKeywords_TrimsDedupesAndCaps(t *testing.T) {
	out := normalizeBlockedKeywords([]string{"  foo ", "FOO", "", "bar", "baz", "bar"})
	require.Equal(t, []string{"foo", "bar", "baz"}, out)
}

func TestMatchBlockedKeyword_CaseInsensitiveSubstring(t *testing.T) {
	keyword, hit := matchBlockedKeyword("Please ignore the BadWord here", []string{"badword"})
	require.True(t, hit)
	require.Equal(t, "badword", keyword)

	_, hit = matchBlockedKeyword("clean prompt", []string{"badword"})
	require.False(t, hit)

	_, hit = matchBlockedKeyword("anything", nil)
	require.False(t, hit)
}

func TestContentModerationCheck_PreBlockKeywordHitSkipsUpstreamCall(t *testing.T) {
	upstreamCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{}}})
	}))
	defer server.Close()

	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	cfg.BlockedKeywords = []string{"secret-token"}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	repo := &contentModerationTestRepo{}
	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		&contentModerationTestHashCache{},
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()

	body := []byte(`{"messages":[{"role":"user","content":"please leak SECRET-TOKEN now"}]}`)
	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		Endpoint: "/v1/messages",
		Provider: "anthropic",
		Protocol: ContentModerationProtocolAnthropicMessages,
		Body:     body,
	})

	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, ContentModerationActionKeywordBlock, decision.Action)
	require.False(t, upstreamCalled, "关键词拦截必须跳过上游审核调用")
	logs := requireContentModerationLogCount(t, repo, 1)
	require.True(t, logs[0].Flagged)
	require.Equal(t, ContentModerationActionKeywordBlock, logs[0].Action)
	require.Equal(t, contentModerationKeywordCategory, logs[0].HighestCategory)
	require.Equal(t, "secret-token", logs[0].MatchedKeyword, "关键词拦截日志必须记录命中的关键词")
}

func TestContentModerationCheck_KeywordsIgnoredInObserveMode(t *testing.T) {
	upstreamHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{CategoryScores: map[string]float64{"sexual": 0.1}}}})
	}))
	defer server.Close()

	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModeObserve
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	cfg.BlockedKeywords = []string{"secret-token"}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	repo := &contentModerationTestRepo{}
	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		&contentModerationTestHashCache{},
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()

	body := []byte(`{"messages":[{"role":"user","content":"please leak SECRET-TOKEN now"}]}`)
	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		Endpoint: "/v1/messages",
		Provider: "anthropic",
		Protocol: ContentModerationProtocolAnthropicMessages,
		Body:     body,
	})

	require.NoError(t, err)
	require.True(t, decision.Allowed, "observe mode must let the request through even on keyword hit")
	require.Equal(t, ContentModerationActionAllow, decision.Action)
}

func TestContentModerationCheck_KeywordOnlyStrategySkipsAPIOnMiss(t *testing.T) {
	upstreamCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{CategoryScores: map[string]float64{"sexual": 0.99}}}})
	}))
	defer server.Close()

	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	cfg.BlockedKeywords = []string{"never-matches"}
	cfg.KeywordBlockingMode = ContentModerationKeywordModeKeywordOnly
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	repo := &contentModerationTestRepo{}
	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		&contentModerationTestHashCache{},
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()

	body := []byte(`{"messages":[{"role":"user","content":"absolutely clean prompt"}]}`)
	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		Endpoint: "/v1/messages",
		Provider: "anthropic",
		Protocol: ContentModerationProtocolAnthropicMessages,
		Body:     body,
	})

	require.NoError(t, err)
	require.True(t, decision.Allowed, "keyword-only must allow misses without calling the API")
	require.False(t, upstreamCalled, "keyword-only must not call the upstream moderation API")
	require.Len(t, repo.snapshotLogs(), 0)
}

func TestContentModerationCheck_APIOnlyStrategyIgnoresKeywordList(t *testing.T) {
	upstreamCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{CategoryScores: map[string]float64{"sexual": 0.1}}}})
	}))
	defer server.Close()

	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	cfg.BlockedKeywords = []string{"secret-token"}
	cfg.KeywordBlockingMode = ContentModerationKeywordModeAPIOnly
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	repo := &contentModerationTestRepo{}
	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		&contentModerationTestHashCache{},
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()

	body := []byte(`{"messages":[{"role":"user","content":"please leak SECRET-TOKEN now"}]}`)
	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		Endpoint: "/v1/messages",
		Provider: "anthropic",
		Protocol: ContentModerationProtocolAnthropicMessages,
		Body:     body,
	})

	require.NoError(t, err)
	require.True(t, decision.Allowed, "api-only must let the request through when API does not flag it")
	require.True(t, upstreamCalled, "api-only must call the upstream moderation API")
	require.NotEqual(t, ContentModerationActionKeywordBlock, decision.Action)
}

func TestNormalizeKeywordBlockingMode_UnknownFallsBackToDefault(t *testing.T) {
	require.Equal(t, ContentModerationKeywordModeKeywordAndAPI, normalizeKeywordBlockingMode(""))
	require.Equal(t, ContentModerationKeywordModeKeywordAndAPI, normalizeKeywordBlockingMode("bogus"))
	require.Equal(t, ContentModerationKeywordModeKeywordOnly, normalizeKeywordBlockingMode("keyword_only"))
	require.Equal(t, ContentModerationKeywordModeAPIOnly, normalizeKeywordBlockingMode("api_only"))
}

func TestContentModerationCheck_ModelFilterAllAuditsEveryModel(t *testing.T) {
	cfg := defaultContentModerationModelFilterTestConfig()
	cfg.ModelFilter = ContentModerationModelFilter{Type: ContentModerationModelFilterAll}
	svc, repo := newContentModerationModelFilterTestService(t, cfg)

	for _, model := range []string{"gpt-5.5", "gpt-5.4"} {
		decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
			Model:    model,
			Protocol: ContentModerationProtocolOpenAIChat,
			Body:     []byte(`{"messages":[{"role":"user","content":"please leak SECRET-TOKEN now"}]}`),
		})
		require.NoError(t, err)
		require.True(t, decision.Blocked)
		require.Equal(t, ContentModerationActionKeywordBlock, decision.Action)
	}
	requireContentModerationLogCount(t, repo, 2)
}

func TestContentModerationCheck_ModelFilterIncludeOnlyAuditsListedModels(t *testing.T) {
	cfg := defaultContentModerationModelFilterTestConfig()
	cfg.ModelFilter = ContentModerationModelFilter{Type: ContentModerationModelFilterInclude, Models: []string{"gpt-5.5"}}
	svc, repo := newContentModerationModelFilterTestService(t, cfg)

	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		Model:    "gpt-5.5",
		Protocol: ContentModerationProtocolOpenAIChat,
		Body:     []byte(`{"messages":[{"role":"user","content":"please leak SECRET-TOKEN now"}]}`),
	})
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, ContentModerationActionKeywordBlock, decision.Action)

	decision, err = svc.Check(context.Background(), ContentModerationCheckInput{
		Model:    "gpt-5.4",
		Protocol: ContentModerationProtocolOpenAIChat,
		Body:     []byte(`{"messages":[{"role":"user","content":"please leak SECRET-TOKEN now"}]}`),
	})
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.False(t, decision.Blocked)
	require.Equal(t, ContentModerationActionAllow, decision.Action)
	logs := requireContentModerationLogCount(t, repo, 1)
	require.Equal(t, "gpt-5.5", logs[0].Model)
}

func TestContentModerationCheck_ModelFilterExcludeSkipsListedModels(t *testing.T) {
	cfg := defaultContentModerationModelFilterTestConfig()
	cfg.ModelFilter = ContentModerationModelFilter{Type: ContentModerationModelFilterExclude, Models: []string{"gpt-5.4"}}
	svc, repo := newContentModerationModelFilterTestService(t, cfg)

	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		Model:    "gpt-5.5",
		Protocol: ContentModerationProtocolOpenAIChat,
		Body:     []byte(`{"messages":[{"role":"user","content":"please leak SECRET-TOKEN now"}]}`),
	})
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, ContentModerationActionKeywordBlock, decision.Action)

	decision, err = svc.Check(context.Background(), ContentModerationCheckInput{
		Model:    "gpt-5.4",
		Protocol: ContentModerationProtocolOpenAIChat,
		Body:     []byte(`{"messages":[{"role":"user","content":"please leak SECRET-TOKEN now"}]}`),
	})
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.False(t, decision.Blocked)
	require.Equal(t, ContentModerationActionAllow, decision.Action)
	logs := requireContentModerationLogCount(t, repo, 1)
	require.Equal(t, "gpt-5.5", logs[0].Model)
}

func TestContentModerationLoadConfig_LegacyConfigDefaultsModelFilterToAll(t *testing.T) {
	raw := `{"enabled":true,"mode":"pre_block","base_url":"https://api.openai.com","model":"omni-moderation-latest","blocked_keywords":["secret-token"]}`
	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyContentModerationConfig: raw,
		}},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()

	cfg, err := svc.loadConfig(context.Background())

	require.NoError(t, err)
	require.Equal(t, ContentModerationModelFilterAll, cfg.ModelFilter.Type)
	require.Empty(t, cfg.ModelFilter.Models)
	require.True(t, cfg.includesModel("gpt-5.5"))
	require.True(t, cfg.includesModel("gpt-5.4"))
}

func TestContentModerationCheck_ModelFilterUsesRequestedModelNotBodyModel(t *testing.T) {
	cfg := defaultContentModerationModelFilterTestConfig()
	cfg.ModelFilter = ContentModerationModelFilter{Type: ContentModerationModelFilterInclude, Models: []string{"gpt-5.5"}}
	svc, repo := newContentModerationModelFilterTestService(t, cfg)

	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		Model:    "gpt-5.5",
		Protocol: ContentModerationProtocolOpenAIChat,
		Body:     []byte(`{"model":"mapped-upstream-model","messages":[{"role":"user","content":"please leak SECRET-TOKEN now"}]}`),
	})

	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, ContentModerationActionKeywordBlock, decision.Action)
	logs := requireContentModerationLogCount(t, repo, 1)
	require.Equal(t, "gpt-5.5", logs[0].Model)
}

func TestContentModerationUpdateConfig_AppendsAndDeletesAPIKeys(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.APIKeys = []string{"sk-old-a", "sk-old-b"}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	repo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyContentModerationConfig: string(rawCfg),
	}}
	svc := newTestModeration(repo, nil, nil, nil, nil, nil, nil)
	svc.Start()
	deleteHashes := []string{moderationAPIKeyHash("sk-old-a")}
	addKeys := []string{"sk-new-c", "sk-old-b"}

	view, err := svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{
		APIKeys:            &addKeys,
		DeleteAPIKeyHashes: &deleteHashes,
	})

	require.NoError(t, err)
	require.Equal(t, 2, view.APIKeyCount)
	require.Equal(t, []string{maskSecretTail("sk-old-b"), maskSecretTail("sk-new-c")}, view.APIKeyMasks)

	var saved ContentModerationConfig
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyContentModerationConfig]), &saved))
	require.Equal(t, []string{"sk-old-b", "sk-new-c"}, saved.apiKeys())
}

func TestContentModerationUpdateConfig_ReplacesAPIKeysWhenRequested(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.APIKeys = []string{"sk-old-a", "sk-old-b"}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	repo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyContentModerationConfig: string(rawCfg),
	}}
	svc := newTestModeration(repo, nil, nil, nil, nil, nil, nil)
	svc.Start()
	deleteHashes := []string{moderationAPIKeyHash("sk-old-a")}
	replaceKeys := []string{"sk-new-only"}

	view, err := svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{
		APIKeys:            &replaceKeys,
		APIKeysMode:        contentModerationAPIKeysModeReplace,
		DeleteAPIKeyHashes: &deleteHashes,
	})

	require.NoError(t, err)
	require.Equal(t, 1, view.APIKeyCount)
	require.Equal(t, []string{maskSecretTail("sk-new-only")}, view.APIKeyMasks)

	var saved ContentModerationConfig
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyContentModerationConfig]), &saved))
	require.Equal(t, []string{"sk-new-only"}, saved.apiKeys())
}

func TestContentModerationUpdateConfig_SavesCustomThresholds(t *testing.T) {
	cfg := defaultContentModerationConfig()
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	repo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyContentModerationConfig: string(rawCfg),
	}}
	svc := newTestModeration(repo, nil, nil, nil, nil, nil, nil)
	svc.Start()
	thresholds := map[string]float64{
		"sexual":     0.72,
		"harassment": 1.25,
		"unknown":    0.01,
	}

	view, err := svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{
		Thresholds: &thresholds,
	})

	require.NoError(t, err)
	require.Equal(t, 0.72, view.Thresholds["sexual"])
	require.Equal(t, 1.0, view.Thresholds["harassment"])
	require.NotContains(t, view.Thresholds, "unknown")

	var saved ContentModerationConfig
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyContentModerationConfig]), &saved))
	require.Equal(t, 0.72, saved.Thresholds["sexual"])
	require.Equal(t, 1.0, saved.Thresholds["harassment"])
	require.NotContains(t, saved.Thresholds, "unknown")
}

func TestContentModerationInput_NormalizeKeepsAndBuildsAllImages(t *testing.T) {
	images := []string{
		"data:image/png;base64,Zmlyc3Q=",
		"data:image/png;base64,c2Vjb25k",
	}
	input := ContentModerationInput{
		Text:   "check image",
		Images: append([]string(nil), images...),
	}
	input.Normalize()

	require.Equal(t, images, input.Images)

	parts, ok := input.ModerationInput().([]moderationAPIInputPart)
	require.True(t, ok)
	require.Len(t, parts, 3)
	require.Equal(t, "text", parts[0].Type)
	require.Equal(t, "image_url", parts[1].Type)
	require.NotNil(t, parts[1].ImageURL)
	require.Equal(t, images[0], parts[1].ImageURL.URL)
	require.Equal(t, images[1], parts[2].ImageURL.URL)
}

func TestBuildModerationTestInputAllowsMultipleImages(t *testing.T) {
	input, count, err := buildModerationTestInput("check image", []string{
		"data:image/png;base64,Zmlyc3Q=",
		"data:image/png;base64,c2Vjb25k",
	})

	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NotNil(t, input)
}

func TestContentModerationCheck_OpenAIResponsesRecordsNonHitForCodexPayload(t *testing.T) {
	var moderationRequest moderationAPIRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/moderations", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&moderationRequest))
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{
			Results: []moderationAPIResult{{
				CategoryScores: map[string]float64{"sexual": 0.01},
			}},
		})
	}))
	defer server.Close()

	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	cfg.RecordNonHits = true
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	repo := &contentModerationTestRepo{}
	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		&contentModerationTestHashCache{},
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()

	body := []byte(`{
		"model":"gpt-5.5",
		"input":[
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer instructions should not be audited"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"first user prompt"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"last user prompt"}]}
		]
	}`)
	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		UserID:   1001,
		Endpoint: "/responses",
		Provider: "openai",
		Model:    "gpt-5.5",
		Protocol: ContentModerationProtocolOpenAIResponses,
		Body:     body,
	})

	require.NoError(t, err)
	require.False(t, decision.Blocked)
	logs := requireContentModerationLogCount(t, repo, 1)
	require.False(t, logs[0].Flagged)
	require.Equal(t, ContentModerationActionAllow, logs[0].Action)
	require.Equal(t, "/responses", logs[0].Endpoint)
	require.Equal(t, "first user prompt\nlast user prompt", logs[0].InputExcerpt)
	require.Equal(t, "first user prompt\nlast user prompt", moderationRequest.Input)
}

func TestContentModerationCheck_PreBlockBlocksCodexResponsesLatestUserInput(t *testing.T) {
	var moderationRequest moderationAPIRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/moderations", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&moderationRequest))
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{
			Results: []moderationAPIResult{{
				CategoryScores: map[string]float64{"sexual": 0.9},
			}},
		})
	}))
	defer server.Close()

	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	cfg.BlockStatus = http.StatusUnavailableForLegalReasons
	cfg.BlockMessage = "内容审计测试阻断"
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	repo := &contentModerationTestRepo{}
	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		&contentModerationTestHashCache{},
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()

	body := []byte(`{
		"model":"gpt-5.5",
		"instructions":"instructions.....",
		"input":[
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer instructions should not be audited"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"environment context"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"latest blocked prompt"}]}
		]
	}`)
	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		UserID:   1001,
		Endpoint: "/responses",
		Provider: "openai",
		Model:    "gpt-5.5",
		Protocol: ContentModerationProtocolOpenAIResponses,
		Body:     body,
	})

	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, ContentModerationActionBlock, decision.Action)
	require.Equal(t, http.StatusUnavailableForLegalReasons, decision.StatusCode)
	require.Equal(t, "内容审计测试阻断", decision.Message)
	logs := requireContentModerationLogCount(t, repo, 1)
	require.True(t, logs[0].Flagged)
	require.Equal(t, ContentModerationActionBlock, logs[0].Action)
	require.Equal(t, ContentModerationModePreBlock, logs[0].Mode)
	require.Equal(t, "environment context\nlatest blocked prompt", logs[0].InputExcerpt)
	require.Equal(t, "environment context\nlatest blocked prompt", moderationRequest.Input)
}

func TestContentModerationStatusTracksPreBlockSyncMetrics(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		score := 0.01
		if requestCount == 1 {
			score = 0.9
		}
		time.Sleep(5 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{
			Results: []moderationAPIResult{{
				CategoryScores: map[string]float64{"sexual": score},
			}},
		})
	}))
	defer server.Close()

	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		&contentModerationTestRepo{},
		&contentModerationTestHashCache{},
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()

	for _, prompt := range []string{"blocked prompt", "clean prompt"} {
		_, err := svc.Check(context.Background(), ContentModerationCheckInput{
			UserID:   1001,
			Protocol: ContentModerationProtocolOpenAIChat,
			Body:     []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, prompt)),
		})
		require.NoError(t, err)
	}

	status, err := svc.GetStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(2), status.PreBlockChecked)
	require.Equal(t, int64(1), status.PreBlockAllowed)
	require.Equal(t, int64(1), status.PreBlockBlocked)
	require.Equal(t, int64(0), status.PreBlockErrors)
	require.Equal(t, 0, status.PreBlockActive)
	require.GreaterOrEqual(t, status.PreBlockAvgLatencyMS, int64(1))
}

func TestContentModerationStatusTracksPreBlockAPIKeyLoad(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{
			Results: []moderationAPIResult{{
				CategoryScores: map[string]float64{"sexual": 0.01},
			}},
		})
	}))
	defer server.Close()

	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-one", "sk-two"}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		&contentModerationTestRepo{},
		&contentModerationTestHashCache{},
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()

	for idx := range 4 {
		_, err := svc.Check(context.Background(), ContentModerationCheckInput{
			UserID:   1001,
			Protocol: ContentModerationProtocolOpenAIChat,
			Body:     []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"prompt %d"}]}`, idx)),
		})
		require.NoError(t, err)
	}

	status, err := svc.GetStatus(context.Background())
	require.NoError(t, err)
	require.Len(t, status.PreBlockAPIKeyLoads, 2)
	require.Equal(t, int64(4), status.PreBlockAPIKeyTotalCalls)
	require.Equal(t, int64(2), status.PreBlockAPIKeyAvailableCount)
	require.Equal(t, int64(0), status.PreBlockAPIKeyActive)
	require.Equal(t, int64(0), status.PreBlockAPIKeyLoads[0].Active)
	require.Equal(t, int64(2), status.PreBlockAPIKeyLoads[0].Total)
	require.Equal(t, int64(2), status.PreBlockAPIKeyLoads[0].Success)
	require.Equal(t, int64(0), status.PreBlockAPIKeyLoads[0].Errors)
	require.Equal(t, int64(2), status.PreBlockAPIKeyLoads[1].Total)
	require.Equal(t, int64(2), status.PreBlockAPIKeyLoads[1].Success)
}

func TestContentModerationStatusTracksPreBlockLocalBlocks(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.KeywordBlockingMode = ContentModerationKeywordModeKeywordOnly
	cfg.BlockedKeywords = []string{"blocked"}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		&contentModerationTestRepo{},
		&contentModerationTestHashCache{},
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()

	for _, prompt := range []string{"blocked prompt", "clean prompt"} {
		_, err := svc.Check(context.Background(), ContentModerationCheckInput{
			UserID:   1001,
			Protocol: ContentModerationProtocolOpenAIChat,
			Body:     []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, prompt)),
		})
		require.NoError(t, err)
	}

	status, err := svc.GetStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(2), status.PreBlockChecked)
	require.Equal(t, int64(1), status.PreBlockAllowed)
	require.Equal(t, int64(1), status.PreBlockBlocked)
	require.Equal(t, int64(0), status.PreBlockErrors)
}

func TestBuildContentModerationTestAuditResult_UsesConfiguredThresholdsOnly(t *testing.T) {
	result := buildContentModerationTestAuditResult(&moderationAPIResult{
		Flagged: true,
		CategoryScores: map[string]float64{
			"harassment": 0.65,
		},
	}, nil)

	require.NotNil(t, result)
	require.False(t, result.Flagged)
	require.Equal(t, "harassment", result.HighestCategory)
	require.Equal(t, 0.65, result.HighestScore)
	require.Equal(t, 0.65, result.CompositeScore)
	require.Equal(t, 0.98, result.Thresholds["harassment"])
}

func TestContentModerationCallModeration_400DoesNotFreezeAPIKey(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Number of images (5) exceeds maximum of 1","type":"invalid_request_error","param":"input","code":"too_many_images"}}`))
	}))
	defer server.Close()

	cfg := defaultContentModerationConfig()
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	cfg.RetryCount = 5
	svc := newTestModeration(nil, nil, nil, nil, nil, nil, nil)
	svc.Start()

	_, err := svc.callModeration(context.Background(), cfg, "hello")

	require.Error(t, err)
	require.Equal(t, 1, requestCount)
	status := svc.apiKeyStatusForHash(0, moderationAPIKeyHash("sk-test"), maskSecretTail("sk-test"), true, defaultContentModerationAPIKeyPriority, "")
	require.Equal(t, "error", status.Status)
	require.Equal(t, http.StatusBadRequest, status.LastHTTPStatus)
	require.Zero(t, status.FailureCount)
	require.Nil(t, status.FrozenUntil)
}

func TestContentModerationCallModeration_FreezesByHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		minFreeze  time.Duration
		maxFreeze  time.Duration
	}{
		{name: "401 freezes ten minutes", statusCode: http.StatusUnauthorized, minFreeze: 9*time.Minute + 55*time.Second, maxFreeze: 10*time.Minute + time.Second},
		{name: "403 freezes ten minutes", statusCode: http.StatusForbidden, minFreeze: 9*time.Minute + 55*time.Second, maxFreeze: 10*time.Minute + time.Second},
		{name: "429 freezes one minute", statusCode: http.StatusTooManyRequests, minFreeze: 55 * time.Second, maxFreeze: time.Minute + time.Second},
		{name: "529 freezes one minute", statusCode: 529, minFreeze: 55 * time.Second, maxFreeze: time.Minute + time.Second},
		{name: "500 freezes ten seconds", statusCode: http.StatusInternalServerError, minFreeze: 5 * time.Second, maxFreeze: 11 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(`{"error":{"message":"upstream error"}}`))
			}))
			defer server.Close()

			cfg := defaultContentModerationConfig()
			cfg.BaseURL = server.URL
			cfg.APIKeys = []string{"sk-test"}
			cfg.RetryCount = 0
			svc := newTestModeration(nil, nil, nil, nil, nil, nil, nil)
			svc.Start()

			_, err := svc.callModeration(context.Background(), cfg, "hello")

			require.Error(t, err)
			status := svc.apiKeyStatusForHash(0, moderationAPIKeyHash("sk-test"), maskSecretTail("sk-test"), true, defaultContentModerationAPIKeyPriority, "")
			require.Equal(t, "frozen", status.Status)
			require.Equal(t, tt.statusCode, status.LastHTTPStatus)
			require.Equal(t, 1, status.FailureCount)
			require.NotNil(t, status.FrozenUntil)
			remaining := time.Until(*status.FrozenUntil)
			require.GreaterOrEqual(t, remaining, tt.minFreeze)
			require.LessOrEqual(t, remaining, tt.maxFreeze)
		})
	}
}

func TestContentModerationTestAPIKeys_400DoesNotFreezeAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid moderation request"}}`))
	}))
	defer server.Close()

	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{}},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()
	result, err := svc.TestAPIKeys(context.Background(), TestContentModerationAPIKeysInput{
		APIKeys: []string{"sk-test"},
		BaseURL: server.URL,
		Prompt:  "hello",
	})

	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	require.Equal(t, "error", result.Items[0].Status)
	require.Equal(t, http.StatusBadRequest, result.Items[0].LastHTTPStatus)
	require.Zero(t, result.Items[0].FailureCount)
	require.Nil(t, result.Items[0].FrozenUntil)
}

func TestContentModerationCheck_PreHashUsesRedisHashCache(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.PreHashCheckEnabled = true
	cfg.APIKeys = []string{"sk-test"}
	cfg.BlockStatus = http.StatusConflict
	cfg.BlockMessage = "命中历史风险输入"
	cfg.AutoBanEnabled = true
	cfg.BanThreshold = 1
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	hashCache := &contentModerationTestHashCache{hashes: map[string]struct{}{}}
	content := ContentModerationInput{Text: "blocked prompt"}
	content.Normalize()
	hashCache.hashes[content.Hash()] = struct{}{}

	repo := &contentModerationTestRepo{}
	userRepo := &contentModerationTestUserRepo{user: &User{ID: 1001, Status: StatusActive}}
	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		hashCache,
		nil,
		userRepo,
		nil,
		nil,
	)
	svc.Start()

	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		UserID:   1001,
		Protocol: ContentModerationProtocolOpenAIChat,
		Body:     []byte(`{"messages":[{"role":"user","content":"blocked prompt"}]}`),
	})
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, ContentModerationActionHashBlock, decision.Action)
	require.Equal(t, http.StatusConflict, decision.StatusCode)
	require.Equal(t, content.Hash(), decision.InputHash)
	require.Contains(t, decision.Message, "命中历史风险输入")
	require.Contains(t, decision.Message, content.Hash())
	require.Len(t, hashCache.snapshotChecked(), 1)
	logs := requireContentModerationLogCount(t, repo, 1)
	require.True(t, logs[0].Flagged)
	require.Equal(t, ContentModerationActionHashBlock, logs[0].Action)
	require.Equal(t, 1.0, logs[0].CategoryScores["hash"])
	require.Equal(t, ContentModerationModePreBlock, logs[0].Mode)
	require.Zero(t, logs[0].ViolationCount)
	require.False(t, logs[0].AutoBanned)
	require.Empty(t, userRepo.updated)
}

func TestContentModerationCheck_HashBlockLogsDoNotIncreaseNextViolationCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{
			Results: []moderationAPIResult{{
				CategoryScores: map[string]float64{"sexual": 0.9},
			}},
		})
	}))
	defer server.Close()

	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	cfg.AutoBanEnabled = false
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	userID := int64(1001)
	repo := &contentModerationTestRepo{}
	hashLog := &ContentModerationLog{
		UserID:          &userID,
		Action:          ContentModerationActionHashBlock,
		Flagged:         true,
		HighestCategory: "hash",
		HighestScore:    1,
		CreatedAt:       time.Now(),
	}
	require.NoError(t, repo.CreateLog(context.Background(), hashLog))

	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		&contentModerationTestHashCache{},
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()

	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		UserID:   userID,
		Protocol: ContentModerationProtocolOpenAIChat,
		Body:     []byte(`{"messages":[{"role":"user","content":"new blocked prompt"}]}`),
	})

	require.NoError(t, err)
	require.True(t, decision.Blocked)
	logs := requireContentModerationLogCount(t, repo, 2)
	require.Equal(t, ContentModerationActionHashBlock, logs[0].Action)
	require.Equal(t, ContentModerationActionBlock, logs[1].Action)
	require.Equal(t, 1, logs[1].ViolationCount)
}

func TestContentModerationAutoBanSkipsAdminProvider(t *testing.T) {
	var slogOutput bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&slogOutput, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})

	cfg := defaultContentModerationConfig()
	cfg.BanThreshold = 2
	cfg.ViolationWindowHours = 24

	userID := int64(1001)
	repo := &contentModerationTestRepo{}
	require.NoError(t, repo.CreateLog(context.Background(), newContentModerationFlaggedLog(userID)))
	userRepo := &contentModerationTestUserRepo{user: &User{ID: userID, Role: RoleAdmin, Status: StatusActive}}
	invalidator := &contentModerationTestAuthCacheInvalidator{}
	svc := newTestModeration(nil, repo, nil, nil, userRepo, invalidator, nil)
	svc.Start()

	svc.persistContentModerationLog(context.Background(), cfg, newContentModerationFlaggedLog(userID), "", false, true)

	logs := requireContentModerationLogCount(t, repo, 2)
	require.Equal(t, 2, logs[1].ViolationCount)
	require.False(t, logs[1].AutoBanned)
	require.Equal(t, StatusActive, userRepo.user.Status)
	require.Empty(t, userRepo.updated)
	require.Empty(t, invalidator.userIDs)
	require.Contains(t, slogOutput.String(), "content_moderation.autoban_skipped_admin")
	require.Contains(t, slogOutput.String(), "user_id=1001")
	require.Contains(t, slogOutput.String(), "role=admin")
	require.Contains(t, slogOutput.String(), "count=2")
	require.Contains(t, slogOutput.String(), "threshold=2")
}

func TestContentModerationAutoBanDisablesRegularUserAtThreshold(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.BanThreshold = 2
	cfg.ViolationWindowHours = 24

	userID := int64(1001)
	repo := &contentModerationTestRepo{}
	require.NoError(t, repo.CreateLog(context.Background(), newContentModerationFlaggedLog(userID)))
	userRepo := &contentModerationTestUserRepo{user: &User{ID: userID, Role: RoleUser, Status: StatusActive}}
	invalidator := &contentModerationTestAuthCacheInvalidator{}
	svc := newTestModeration(nil, repo, nil, nil, userRepo, invalidator, nil)
	svc.Start()

	svc.persistContentModerationLog(context.Background(), cfg, newContentModerationFlaggedLog(userID), "", false, true)

	logs := requireContentModerationLogCount(t, repo, 2)
	require.Equal(t, 2, logs[1].ViolationCount)
	require.True(t, logs[1].AutoBanned)
	require.Len(t, userRepo.updated, 1)
	require.Equal(t, StatusDisabled, userRepo.user.Status)
	require.Equal(t, []int64{userID}, invalidator.userIDs)
}

func TestContentModerationTeamAttributionAutoBanDisablesActorOnly(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.BanThreshold = 1
	cfg.ViolationWindowHours = 24

	ownerID := int64(1001)
	memberID := int64(2002)
	teamID := int64(3003)
	repo := &contentModerationTestRepo{}
	userRepo := &contentModerationTestUserRepo{user: &User{ID: memberID, Role: RoleUser, Status: StatusActive}}
	invalidator := &contentModerationTestAuthCacheInvalidator{}
	svc := newTestModeration(nil, repo, nil, nil, userRepo, invalidator, nil)
	svc.Start()
	log := newContentModerationFlaggedLog(memberID)
	log.BillingUserID = &ownerID
	log.TeamID = &teamID

	svc.persistContentModerationLog(context.Background(), cfg, log, "", false, true)

	logs := requireContentModerationLogCount(t, repo, 1)
	require.Equal(t, memberID, *logs[0].UserID)
	require.Equal(t, ownerID, *logs[0].BillingUserID)
	require.Equal(t, teamID, *logs[0].TeamID)
	require.Equal(t, []int64{memberID}, userRepo.requestedIDs)
	require.Len(t, userRepo.updated, 1)
	require.Equal(t, memberID, userRepo.updated[0].ID)
	require.Equal(t, StatusDisabled, userRepo.updated[0].Status)
	require.NotEqual(t, ownerID, userRepo.updated[0].ID)
	require.Equal(t, []int64{memberID}, invalidator.userIDs)
}

func TestContentModerationAdminBelowBanThresholdRecordsViolationOnly(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.BanThreshold = 2
	cfg.ViolationWindowHours = 24

	userID := int64(1001)
	repo := &contentModerationTestRepo{}
	userRepo := &contentModerationTestUserRepo{user: &User{ID: userID, Role: RoleAdmin, Status: StatusActive}}
	invalidator := &contentModerationTestAuthCacheInvalidator{}
	svc := newTestModeration(nil, repo, nil, nil, userRepo, invalidator, nil)
	svc.Start()

	svc.persistContentModerationLog(context.Background(), cfg, newContentModerationFlaggedLog(userID), "", false, true)

	logs := requireContentModerationLogCount(t, repo, 1)
	require.Equal(t, 1, logs[0].ViolationCount)
	require.False(t, logs[0].AutoBanned)
	require.Equal(t, StatusActive, userRepo.user.Status)
	require.Empty(t, userRepo.updated)
	require.Empty(t, invalidator.userIDs)
}

func TestContentModerationCheck_PreBlockFlaggedWritesRedisHashCache(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{
			Results: []moderationAPIResult{{
				CategoryScores: map[string]float64{"sexual": 0.9},
			}},
		})
	}))
	defer server.Close()

	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.PreHashCheckEnabled = true
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	cfg.BlockStatus = http.StatusConflict
	cfg.BlockMessage = "命中风险输入"
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	repo := &contentModerationTestRepo{}
	hashCache := &contentModerationTestHashCache{}
	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		hashCache,
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()

	body := []byte(`{"messages":[{"role":"user","content":"repeat blocked prompt"}]}`)
	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		Protocol: ContentModerationProtocolOpenAIChat,
		Body:     body,
	})
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, ContentModerationActionBlock, decision.Action)
	require.Equal(t, 1, requestCount)
	recorded := requireRecordedHashCount(t, hashCache, 1)
	requireContentModerationLogCount(t, repo, 1)

	decision, err = svc.Check(context.Background(), ContentModerationCheckInput{
		Protocol: ContentModerationProtocolOpenAIChat,
		Body:     body,
	})
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, ContentModerationActionHashBlock, decision.Action)
	require.Equal(t, recorded[0], decision.InputHash)
	require.Equal(t, 1, requestCount)
	logs := requireContentModerationLogCount(t, repo, 2)
	require.Equal(t, ContentModerationActionBlock, logs[0].Action)
	require.Equal(t, ContentModerationActionHashBlock, logs[1].Action)
}

func TestContentModerationDeleteFlaggedInputHash_NormalizesAndDeletes(t *testing.T) {
	existingHash := strings.Repeat("a", 64)
	hashCache := &contentModerationTestHashCache{hashes: map[string]struct{}{
		existingHash: {},
	}}
	svc := &ContentModerationService{runtime: testModerationRuntime(), hashCache: hashCache}

	result, err := svc.DeleteFlaggedInputHash(context.Background(), strings.ToUpper(existingHash))

	require.NoError(t, err)
	require.Equal(t, existingHash, result.InputHash)
	require.True(t, result.Deleted)
	require.False(t, hashCache.hasHash(existingHash))
	require.Equal(t, []string{existingHash}, hashCache.snapshotDeleted())

	result, err = svc.DeleteFlaggedInputHash(context.Background(), existingHash)

	require.NoError(t, err)
	require.Equal(t, existingHash, result.InputHash)
	require.False(t, result.Deleted)
}

func TestContentModerationClearFlaggedInputHashesAndStatusCount(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	hashCache := &contentModerationTestHashCache{hashes: map[string]struct{}{
		strings.Repeat("a", 64): {},
		strings.Repeat("b", 64): {},
	}}
	svc := &ContentModerationService{
		runtime: testModerationRuntime(),
		settingRepo: &contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		hashCache: hashCache,
		keyHealth: make(map[string]*contentModerationKeyHealth),
	}

	status, err := svc.GetStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(2), status.FlaggedHashCount)

	result, err := svc.ClearFlaggedInputHashes(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Deleted)

	status, err = svc.GetStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(0), status.FlaggedHashCount)
}

func TestContentModerationCheck_AsyncFlaggedWritesRedisHashCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{
			Results: []moderationAPIResult{{
				CategoryScores: map[string]float64{"sexual": 0.9},
			}},
		})
	}))
	defer server.Close()

	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModeObserve
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	repo := &contentModerationTestRepo{}
	hashCache := &contentModerationTestHashCache{}
	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		hashCache,
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()

	decision := svc.checkSync(context.Background(), ContentModerationCheckInput{
		Protocol: ContentModerationProtocolOpenAIChat,
		Body:     []byte(`{"messages":[{"role":"user","content":"bad prompt"}]}`),
	}, cfg, ContentModerationInput{Text: "bad prompt"}, strings.Repeat("b", 64), contentModerationIntPtr(25), false)

	require.False(t, decision.Blocked)
	requireRecordedHashCount(t, hashCache, 1)
	requireContentModerationLogCount(t, repo, 1)
}

func TestContentModerationUnbanUser_ActivatesUserAndInvalidatesAuthCache(t *testing.T) {
	userRepo := &contentModerationTestUserRepo{user: &User{ID: 1001, Email: "user@example.com", Status: StatusDisabled}}
	invalidator := &contentModerationTestAuthCacheInvalidator{}
	repo := &contentModerationTestRepo{}
	svc := newTestModeration(nil, repo, nil, nil, userRepo, invalidator, nil)
	svc.Start()

	result, err := svc.UnbanUser(context.Background(), 1001)

	require.NoError(t, err)
	require.Equal(t, int64(1001), result.UserID)
	require.Equal(t, StatusActive, result.Status)
	require.Len(t, userRepo.updated, 1)
	require.Equal(t, StatusActive, userRepo.updated[0].Status)
	require.Equal(t, []int64{1001}, invalidator.userIDs)
}

func TestContentModerationUnbanUser_ActiveUserOnlyInvalidatesAuthCache(t *testing.T) {
	userRepo := &contentModerationTestUserRepo{user: &User{ID: 1001, Email: "user@example.com", Status: StatusActive}}
	invalidator := &contentModerationTestAuthCacheInvalidator{}
	repo := &contentModerationTestRepo{}
	svc := newTestModeration(nil, repo, nil, nil, userRepo, invalidator, nil)
	svc.Start()

	result, err := svc.UnbanUser(context.Background(), 1001)

	require.NoError(t, err)
	require.Equal(t, StatusActive, result.Status)
	require.Empty(t, userRepo.updated)
	require.Equal(t, []int64{1001}, invalidator.userIDs)
}

func TestIsOpenAICyberWarningText(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{name: "cybersecurity risk", text: "This request may pose a cybersecurity risk", want: true},
		{name: "full trusted access warning", text: "This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request. To get authorized for security work, join the Trusted Access for Cyber program:", want: true},
		{name: "cyber and risk separated", text: "Cyber content was rejected because of risk controls", want: true},
		{name: "cyber link", text: "See https://chatgpt.com/cyber for details", want: true},
		{name: "cyber abuse", text: "Request blocked by cyber abuse policy", want: true},
		{name: "usage policy only", text: "Your request violates our usage policy", want: false},
		{name: "flagged only", text: "The request was flagged by safety systems", want: false},
		{name: "normal error", text: "upstream service unavailable", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IsOpenAICyberWarningText(tc.text))
		})
	}
}

func TestContentModerationRecordCyberWarning_ExtractsResponsesFailedError(t *testing.T) {
	rawCfg, _ := json.Marshal(defaultContentModerationConfig())
	settingRepo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: string(rawCfg),
	}}
	repo := &contentModerationTestRepo{}
	svc := newTestModeration(settingRepo, repo, nil, nil, nil, nil, nil)
	svc.Start()

	warning, err := svc.RecordCyberWarning(context.Background(), ContentModerationCyberWarningInput{
		RequestID: "req_failed",
		UserID:    1001,
		ResponseBody: []byte(`{
			"type":"response.failed",
			"response":{
				"error":{"message":"This request may pose a cybersecurity risk."}
			}
		}`),
	})

	require.NoError(t, err)
	require.NotNil(t, warning)
	require.Len(t, repo.cyberWarnings, 1)
	require.Contains(t, repo.cyberWarnings[0].WarningText, "cybersecurity risk")
}

func TestContentModerationRecordCyberWarning_RecordsCyberPolicyCode(t *testing.T) {
	rawCfg, _ := json.Marshal(defaultContentModerationConfig())
	settingRepo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: string(rawCfg),
	}}
	repo := &contentModerationTestRepo{}
	svc := newTestModeration(settingRepo, repo, nil, nil, nil, nil, nil)
	svc.Start()

	warning, err := svc.RecordCyberWarning(context.Background(), ContentModerationCyberWarningInput{
		RequestID: "req_cyber_policy",
		UserID:    1001,
		ResponseBody: []byte(`{
			"response":{
				"error":{
					"code":"cyber_policy",
					"message":"Request blocked by upstream cyber policy"
				}
			}
		}`),
	})

	require.NoError(t, err)
	require.NotNil(t, warning)
	require.Len(t, repo.cyberWarnings, 1)
	require.Equal(t, "Request blocked by upstream cyber policy", repo.cyberWarnings[0].WarningText)
}

func TestContentModerationCyberSessionBlockGroupInScope_RespectsRiskControlGroups(t *testing.T) {
	selectedGroupID := int64(101)
	otherGroupID := int64(202)
	tests := []struct {
		name               string
		riskControlEnabled bool
		allGroups          bool
		groupIDs           []int64
		groupID            *int64
		want               bool
	}{
		{
			name:               "已选分组启用会话屏蔽",
			riskControlEnabled: true,
			groupIDs:           []int64{selectedGroupID},
			groupID:            &selectedGroupID,
			want:               true,
		},
		{
			name:               "未选分组跳过会话屏蔽",
			riskControlEnabled: true,
			groupIDs:           []int64{selectedGroupID},
			groupID:            &otherGroupID,
			want:               false,
		},
		{
			name:               "全部分组启用会话屏蔽",
			riskControlEnabled: true,
			allGroups:          true,
			groupID:            &otherGroupID,
			want:               true,
		},
		{
			name:               "风控中心关闭时跳过会话屏蔽",
			riskControlEnabled: false,
			allGroups:          true,
			groupID:            &selectedGroupID,
			want:               false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultContentModerationConfig()
			cfg.AllGroups = tc.allGroups
			cfg.GroupIDs = tc.groupIDs
			rawCfg, err := json.Marshal(cfg)
			require.NoError(t, err)
			settingRepo := &contentModerationTestSettingRepo{values: map[string]string{
				SettingKeyRiskControlEnabled:      fmt.Sprintf("%t", tc.riskControlEnabled),
				SettingKeyContentModerationConfig: string(rawCfg),
			}}
			svc := newTestModeration(settingRepo, nil, nil, nil, nil, nil, nil)
			svc.Start()

			inScope, err := svc.CyberSessionBlockGroupInScope(context.Background(), tc.groupID)

			require.NoError(t, err)
			require.Equal(t, tc.want, inScope)
		})
	}
}

func TestContentModerationRecordCyberWarning_SkipsGroupOutOfScope(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.AllGroups = false
	cfg.GroupIDs = []int64{101}
	rawCfg, _ := json.Marshal(cfg)
	settingRepo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: string(rawCfg),
	}}
	repo := &contentModerationTestRepo{}
	svc := newTestModeration(settingRepo, repo, nil, nil, nil, nil, nil)
	svc.Start()
	outOfScopeGroupID := int64(202)

	warning, err := svc.RecordCyberWarning(context.Background(), ContentModerationCyberWarningInput{
		RequestID:      "req_cyber_out_of_group_scope",
		UserID:         1001,
		GroupID:        &outOfScopeGroupID,
		GroupName:      "out-of-scope",
		WarningText:    "This request may pose a cybersecurity risk.",
		UpstreamStatus: 400,
	})

	require.NoError(t, err)
	require.Nil(t, warning)
	require.Empty(t, repo.cyberWarnings)
}

func TestContentModerationRecordCyberWarning_SkipsModelOutOfScope(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.ModelFilter = ContentModerationModelFilter{
		Type:   ContentModerationModelFilterInclude,
		Models: []string{"gpt-5"},
	}
	rawCfg, _ := json.Marshal(cfg)
	settingRepo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: string(rawCfg),
	}}
	repo := &contentModerationTestRepo{}
	svc := newTestModeration(settingRepo, repo, nil, nil, nil, nil, nil)
	svc.Start()

	warning, err := svc.RecordCyberWarning(context.Background(), ContentModerationCyberWarningInput{
		RequestID:      "req_cyber_out_of_model_scope",
		UserID:         1001,
		Model:          "gpt-4.1",
		WarningText:    "This request may pose a cybersecurity risk.",
		UpstreamStatus: 400,
	})

	require.NoError(t, err)
	require.Nil(t, warning)
	require.Empty(t, repo.cyberWarnings)
}

func TestContentModerationRecordCyberWarning_DefaultRecordsWithoutBan(t *testing.T) {
	userID := int64(1001)
	rawCfg, _ := json.Marshal(defaultContentModerationConfig())
	settingRepo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: string(rawCfg),
	}}
	repo := &contentModerationTestRepo{}
	userRepo := &contentModerationTestUserRepo{user: &User{ID: userID, Email: "user@example.com", Status: StatusActive}}
	invalidator := &contentModerationTestAuthCacheInvalidator{}
	svc := newTestModeration(settingRepo, repo, nil, nil, userRepo, invalidator, nil)
	svc.Start()

	warning, err := svc.RecordCyberWarning(context.Background(), ContentModerationCyberWarningInput{
		RequestID:      "req_1",
		UserID:         userID,
		UserEmail:      "user@example.com",
		ProviderID:     2001,
		ProviderName:   "openai-1",
		UpstreamStatus: 400,
		ResponseBody:   []byte(`{"error":{"message":"This request may pose a cybersecurity risk. token=abc123456789xyz"}}`),
		PromptExcerpt:  "inspect target sk-proj-1234567890abcdef",
	})

	require.NoError(t, err)
	require.NotNil(t, warning)
	require.Len(t, repo.cyberWarnings, 1)
	require.Contains(t, repo.cyberWarnings[0].WarningText, "token=abc123456789xyz")
	require.Contains(t, repo.cyberWarnings[0].PromptExcerpt, "inspect target")
	require.Contains(t, repo.cyberWarnings[0].PromptExcerpt, "sk-proj-1234567890abcdef")
	require.NotContains(t, repo.cyberWarnings[0].PromptExcerpt, "[已脱敏]")
	require.Equal(t, 1, repo.cyberWarnings[0].ViolationCount)
	require.False(t, repo.cyberWarnings[0].AutoBanned)
	require.Empty(t, userRepo.updated)
	require.Empty(t, invalidator.userIDs)
}

func TestContentModerationRecordCyberWarning_BansWhenCyberThresholdReached(t *testing.T) {
	userID := int64(1001)
	cfg := defaultContentModerationConfig()
	cfg.CyberAutoBanEnabled = true
	cfg.CyberBanThreshold = 1
	rawCfg, _ := json.Marshal(cfg)
	settingRepo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: string(rawCfg),
	}}
	repo := &contentModerationTestRepo{}
	userRepo := &contentModerationTestUserRepo{user: &User{ID: userID, Email: "user@example.com", Status: StatusActive}}
	invalidator := &contentModerationTestAuthCacheInvalidator{}
	svc := newTestModeration(settingRepo, repo, nil, nil, userRepo, invalidator, nil)
	svc.Start()

	warning, err := svc.RecordCyberWarning(context.Background(), ContentModerationCyberWarningInput{
		RequestID:      "req_1",
		UserID:         userID,
		UserEmail:      "user@example.com",
		ProviderID:     2001,
		ProviderName:   "openai-1",
		UpstreamStatus: 400,
		WarningText:    "This content was flagged for possible cybersecurity risk.",
	})

	require.NoError(t, err)
	require.NotNil(t, warning)
	require.True(t, warning.AutoBanned)
	require.Empty(t, userRepo.updated)
	require.Equal(t, []int64{userID}, invalidator.userIDs)
}

func TestContentModerationRecordCyberWarning_CountsExistingWindowWarnings(t *testing.T) {
	userID := int64(1001)
	cfg := defaultContentModerationConfig()
	cfg.CyberAutoBanEnabled = true
	cfg.CyberBanThreshold = 2
	rawCfg, _ := json.Marshal(cfg)
	settingRepo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: string(rawCfg),
	}}
	now := time.Now()
	repo := &contentModerationTestRepo{cyberWarnings: []ContentModerationCyberWarning{{
		UserID:         &userID,
		ViolationCount: 1,
		CreatedAt:      now.Add(-time.Hour),
	}}}
	invalidator := &contentModerationTestAuthCacheInvalidator{}
	svc := newTestModeration(settingRepo, repo, nil, nil, nil, invalidator, nil)
	svc.Start()

	warning, err := svc.RecordCyberWarning(context.Background(), ContentModerationCyberWarningInput{
		RequestID:      "req_2",
		UserID:         userID,
		UserEmail:      "user@example.com",
		ProviderID:     2001,
		ProviderName:   "openai-1",
		UpstreamStatus: 400,
		WarningText:    "This request may pose a cybersecurity risk.",
	})

	require.NoError(t, err)
	require.NotNil(t, warning)
	require.Equal(t, 2, warning.ViolationCount)
	require.True(t, warning.AutoBanned)
	require.Equal(t, []int64{userID}, invalidator.userIDs)
}

// TestContentModerationNoMediaRetention checks logs when NoMediaRetention is true.
// Audit metadata and hashes remain available while image snapshots and text excerpts are omitted.
func TestContentModerationNoMediaRetention(t *testing.T) {
	svc, repo, hashCache, imageDataURL := newCreativeNoMediaRetentionModerationService(t)

	body := []byte(`{"model":"gpt-image-2","prompt":"sexy picture please","images":[{"image_url":"` + imageDataURL + `"}]}`)
	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		RequestID:        "creative_mod_test_noretention",
		UserID:           7,
		BillingUserID:    7,
		Endpoint:         "/v1/creative/runs",
		Provider:         "openai",
		Protocol:         ContentModerationProtocolOpenAIImages,
		Body:             body,
		NoMediaRetention: true,
	})
	require.NoError(t, err)
	require.True(t, decision.Allowed, "observe 模式下命中只记录不阻断")

	logs := requireContentModerationLogCount(t, repo, 1)
	log := logs[0]
	require.True(t, log.Flagged)
	require.Equal(t, "sexual", log.HighestCategory)
	// Content and media fields stay empty, including media snapshot error records.
	require.Empty(t, log.InputExcerpt)
	require.Empty(t, log.InputItems)
	require.Empty(t, log.Media, "无媒体留存模式不得保存媒体快照")
	// Hashes support deduplication while categories, scores, and actions describe the audit result.
	require.NotEmpty(t, hashCache.snapshotRecorded())
	require.NotEmpty(t, log.CategoryScores)
	require.NotEmpty(t, log.Action)
}

// TestContentModerationMediaRetentionControl checks logs when NoMediaRetention is false.
// The log retains an excerpt and a ready image snapshot.
func TestContentModerationMediaRetentionControl(t *testing.T) {
	svc, repo, _, imageDataURL := newCreativeNoMediaRetentionModerationService(t)

	body := []byte(`{"model":"gpt-image-2","prompt":"sexy picture please","images":[{"image_url":"` + imageDataURL + `"}]}`)
	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		RequestID:     "creative_mod_test_control",
		UserID:        7,
		BillingUserID: 7,
		Endpoint:      "/v1/creative/runs",
		Provider:      "openai",
		Protocol:      ContentModerationProtocolOpenAIImages,
		Body:          body,
	})
	require.NoError(t, err)
	require.True(t, decision.Allowed)

	logs := requireContentModerationLogCount(t, repo, 1)
	log := logs[0]
	require.NotEmpty(t, log.InputExcerpt, "对照组必须保留正文摘录")
	require.NotEmpty(t, log.InputItems, "对照组必须保留输入项")
	require.Len(t, log.Media, 1, "对照组必须对命中媒体做快照")
	require.Equal(t, "ready", log.Media[0].SnapshotStatus)
	require.NotEmpty(t, log.Media[0].Content, "对照组快照必须包含图片字节")
}

func newExpandedModerationTestService(t *testing.T, baseURL string, recordNonHits bool) (*ContentModerationService, *contentModerationTestRepo) {
	t.Helper()
	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.BaseURL = baseURL
	cfg.APIKeys = []string{"sk-test"}
	cfg.RetryCount = 0
	cfg.RecordNonHits = recordNonHits
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &contentModerationTestRepo{}
	service := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		&contentModerationTestHashCache{},
		nil,
		nil,
		nil,
		nil,
	)
	service.Start()
	return service, repo
}

func decodeModerationRawInput(t *testing.T, r *http.Request) json.RawMessage {
	t.Helper()
	var payload struct {
		Input json.RawMessage `json:"input"`
	}
	require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
	return payload.Input
}

func decodeModerationTextInputs(t *testing.T, r *http.Request) []string {
	t.Helper()
	raw := decodeModerationRawInput(t, r)
	var batch []string
	if err := json.Unmarshal(raw, &batch); err == nil {
		return batch
	}
	var single string
	require.NoError(t, json.Unmarshal(raw, &single))
	return []string{single}
}

func (r *contentModerationTestProxyRepo) GetByID(ctx context.Context, id int64) (*Proxy, error) {
	r.getCalls.Add(1)
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	if px, ok := r.proxies[id]; ok {
		return px, nil
	}
	return nil, errors.New("proxy not found")
}

func moderationProxyIDPtr(v int64) *int64 { return &v }

func (r *contentModerationRuntimeSettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: value}, nil
}

func (r *contentModerationRuntimeSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getValueCalls++
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *contentModerationRuntimeSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	r.values[key] = value
	return nil
}

func (r *contentModerationRuntimeSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	r.getMultipleCalls++
	if err := r.getMultipleErr; err != nil {
		r.mu.Unlock()
		return nil, err
	}
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	start := r.getMultipleStart
	wait := r.getMultipleWait
	r.getMultipleStart = nil
	r.getMultipleWait = nil
	r.mu.Unlock()
	if start != nil {
		start <- struct{}{}
	}
	if wait != nil {
		<-wait
	}
	return out, nil
}

func (r *contentModerationRuntimeSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	maps.Copy(r.values, values)
	return nil
}

func (r *contentModerationRuntimeSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.values))
	maps.Copy(out, r.values)
	return out, nil
}

func (r *contentModerationRuntimeSettingRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

func (r *contentModerationRuntimeSettingRepo) calls() (getValue, getMultiple int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getValueCalls, r.getMultipleCalls
}

func (r *contentModerationRuntimeSettingRepo) failMultiple(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getMultipleErr = err
}

func (r *contentModerationRuntimeSettingRepo) blockNextMultiple(start chan<- struct{}, wait <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getMultipleStart = start
	r.getMultipleWait = wait
}

func runtimeCacheTestConfig(t *testing.T, keywords ...string) string {
	t.Helper()
	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.KeywordBlockingMode = ContentModerationKeywordModeKeywordOnly
	cfg.BlockedKeywords = keywords
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	return string(raw)
}

func runtimeCacheTestService(repo *contentModerationRuntimeSettingRepo, ttl time.Duration) *ContentModerationService {
	return &ContentModerationService{
		runtime:         testModerationRuntime(),
		settingRepo:     repo,
		repo:            &contentModerationTestRepo{},
		runtimeCacheTTL: ttl,
	}
}

func runtimeCacheTestInput(text string) ContentModerationCheckInput {
	return ContentModerationCheckInput{
		Protocol: ContentModerationProtocolOpenAIChat,
		Model:    "risk-cache-test",
		Body:     []byte(`{"messages":[{"role":"user","content":"` + text + `"}]}`),
	}
}

func (r *contentModerationTestSettingRepo) Get(ctx context.Context, key string) (*Setting, error) {
	if value, ok := r.values[key]; ok {
		return &Setting{Key: key, Value: value}, nil
	}
	return nil, ErrSettingNotFound
}

func (r *contentModerationTestSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", ErrSettingNotFound
}

func (r *contentModerationTestSettingRepo) Set(ctx context.Context, key, value string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

func (r *contentModerationTestSettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (r *contentModerationTestSettingRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	maps.Copy(r.values, settings)
	return nil
}

func (r *contentModerationTestSettingRepo) GetAll(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(r.values))
	maps.Copy(out, r.values)
	return out, nil
}

func (r *contentModerationTestSettingRepo) Delete(ctx context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func (r *contentModerationTestRepo) CreateLog(ctx context.Context, log *ContentModerationLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if log != nil {
		r.logs = append(r.logs, *log)
	}
	return nil
}

func (r *contentModerationTestRepo) ListLogs(ctx context.Context, filter ContentModerationLogFilter) ([]ContentModerationLog, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (r *contentModerationTestRepo) CountFlaggedByUserSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, log := range r.logs {
		if log.UserID == nil || *log.UserID != userID || !log.Flagged || log.Action == ContentModerationActionHashBlock {
			continue
		}
		if log.CreatedAt.IsZero() || log.CreatedAt.Before(since) {
			continue
		}
		count++
	}
	return count, nil
}

func (r *contentModerationTestRepo) CreateCyberWarning(ctx context.Context, warning *ContentModerationCyberWarning) error {
	if warning != nil {
		if warning.CreatedAt.IsZero() {
			warning.CreatedAt = time.Now()
		}
		r.cyberWarnings = append(r.cyberWarnings, *warning)
	}
	return nil
}

func (r *contentModerationTestRepo) CreateCyberWarningAndApplyUserBan(ctx context.Context, warning *ContentModerationCyberWarning, policy ContentModerationCyberWarningPolicy) (bool, error) {
	if warning == nil {
		return false, nil
	}
	count := 1
	if warning.UserID != nil && policy.WindowHours > 0 {
		since := time.Now().Add(-time.Duration(policy.WindowHours) * time.Hour)
		n, _ := r.CountCyberWarningsByUserSince(ctx, *warning.UserID, since)
		count = n + 1
	}
	warning.ViolationCount = count
	if policy.AutoBanEnabled && policy.BanThreshold > 0 && count >= policy.BanThreshold {
		warning.AutoBanned = true
	}
	if warning.CreatedAt.IsZero() {
		warning.CreatedAt = time.Now()
	}
	r.cyberWarnings = append(r.cyberWarnings, *warning)
	return warning.AutoBanned, nil
}

func (r *contentModerationTestRepo) ListCyberWarnings(ctx context.Context, filter ContentModerationCyberWarningFilter) ([]ContentModerationCyberWarning, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (r *contentModerationTestRepo) CountCyberWarningsByUserSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	count := 0
	for _, warning := range r.cyberWarnings {
		if warning.UserID != nil && *warning.UserID == userID && warning.CreatedAt.After(since) {
			count++
		}
	}
	return count, nil
}

func (r *contentModerationTestRepo) GetCyberSummary(ctx context.Context, filter ContentModerationCyberWarningFilter) (*ContentModerationCyberSummary, error) {
	return &ContentModerationCyberSummary{}, nil
}

func (r *contentModerationTestRepo) MarkCyberWarningEmailSent(ctx context.Context, id int64) error {
	return nil
}

func (r *contentModerationTestRepo) CleanupExpiredLogs(ctx context.Context, hitBefore time.Time, nonHitBefore time.Time) (*ContentModerationCleanupResult, error) {
	return &ContentModerationCleanupResult{}, nil
}

func (r *contentModerationTestRepo) snapshotLogs() []ContentModerationLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ContentModerationLog, len(r.logs))
	copy(out, r.logs)
	return out
}

func requireContentModerationLogCount(t *testing.T, repo *contentModerationTestRepo, want int) []ContentModerationLog {
	t.Helper()
	var logs []ContentModerationLog
	require.Eventually(t, func() bool {
		logs = repo.snapshotLogs()
		return len(logs) == want
	}, time.Second, 10*time.Millisecond)
	return logs
}

func requireRecordedHashCount(t *testing.T, cache *contentModerationTestHashCache, want int) []string {
	t.Helper()
	var hashes []string
	require.Eventually(t, func() bool {
		hashes = cache.snapshotRecorded()
		return len(hashes) == want
	}, time.Second, 10*time.Millisecond)
	return hashes
}

func (r *contentModerationTestUserRepo) GetByID(ctx context.Context, id int64) (*User, error) {
	r.requestedIDs = append(r.requestedIDs, id)
	if r.user == nil {
		return nil, ErrUserNotFound
	}
	clone := *r.user
	return &clone, nil
}

func (r *contentModerationTestUserRepo) Update(ctx context.Context, user *User, fields UserUpdateFields) error {
	if user == nil {
		return nil
	}
	clone := *user
	r.updated = append(r.updated, clone)
	r.user = &clone
	return nil
}

func (i *contentModerationTestAuthCacheInvalidator) InvalidateAuthCacheByUserID(ctx context.Context, userID int64) {
	i.userIDs = append(i.userIDs, userID)
}

func (c *contentModerationTestHashCache) RecordFlaggedInputHash(ctx context.Context, inputHash string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hashes == nil {
		c.hashes = map[string]struct{}{}
	}
	c.hashes[inputHash] = struct{}{}
	c.recorded = append(c.recorded, inputHash)
	return nil
}

func (c *contentModerationTestHashCache) HasFlaggedInputHash(ctx context.Context, inputHash string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checked = append(c.checked, inputHash)
	if c.hasResultUsed {
		return c.hasResult, nil
	}
	_, ok := c.hashes[inputHash]
	return ok, nil
}

func (c *contentModerationTestHashCache) DeleteFlaggedInputHash(ctx context.Context, inputHash string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleted = append(c.deleted, inputHash)
	if c.hashes == nil {
		return false, nil
	}
	if _, ok := c.hashes[inputHash]; !ok {
		return false, nil
	}
	delete(c.hashes, inputHash)
	return true, nil
}

func (c *contentModerationTestHashCache) ClearFlaggedInputHashes(ctx context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	deleted := int64(len(c.hashes))
	c.hashes = map[string]struct{}{}
	return deleted, nil
}

func (c *contentModerationTestHashCache) CountFlaggedInputHashes(ctx context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return int64(len(c.hashes)), nil
}

func (c *contentModerationTestHashCache) snapshotRecorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.recorded))
	copy(out, c.recorded)
	return out
}

func (c *contentModerationTestHashCache) snapshotChecked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.checked))
	copy(out, c.checked)
	return out
}

func (c *contentModerationTestHashCache) hasHash(inputHash string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.hashes[inputHash]
	return ok
}

func (c *contentModerationTestHashCache) snapshotDeleted() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.deleted))
	copy(out, c.deleted)
	return out
}

func defaultContentModerationModelFilterTestConfig() *ContentModerationConfig {
	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.BlockedKeywords = []string{"secret-token"}
	return cfg
}

func newContentModerationModelFilterTestService(t *testing.T, cfg *ContentModerationConfig) (*ContentModerationService, *contentModerationTestRepo) {
	t.Helper()
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &contentModerationTestRepo{}
	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		&contentModerationTestHashCache{},
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()
	return svc, repo
}

func newContentModerationFlaggedLog(userID int64) *ContentModerationLog {
	return &ContentModerationLog{
		UserID:          &userID,
		Action:          ContentModerationActionBlock,
		Flagged:         true,
		HighestCategory: "sexual",
		HighestScore:    0.9,
		CreatedAt:       time.Now(),
	}
}

func contentModerationIntPtr(v int) *int {
	return &v
}

// newCreativeNoMediaRetentionModerationService creates a service with an httptest audit upstream.
// The upstream returns safe text scores and flagged image scores.
// Data URLs allow inline image snapshots while keeping SSRF loopback restrictions enabled.
func newCreativeNoMediaRetentionModerationService(t *testing.T) (*ContentModerationService, *contentModerationTestRepo, *contentModerationTestHashCache, string) {
	t.Helper()
	pngBytes := makeTestPNG(t, 2, 2)
	imageDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Text and image units are separate calls; image_url identifies image requests.
		var request struct {
			Input any `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		rawInput, _ := json.Marshal(request.Input)
		if strings.Contains(string(rawInput), "image_url") {
			_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{
				{CategoryScores: map[string]float64{"sexual": 0.99}},
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{
			{CategoryScores: map[string]float64{"sexual": 0.01}},
		}})
	}))
	t.Cleanup(server.Close)

	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModeObserve
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	repo := &contentModerationTestRepo{}
	hashCache := &contentModerationTestHashCache{}
	svc := newTestModeration(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo,
		hashCache,
		nil,
		nil,
		nil,
		nil,
	)
	svc.Start()
	t.Cleanup(func() { require.NoError(t, svc.Stop()) })
	return svc, repo, hashCache, imageDataURL
}

func (r *contentModerationTestUserRepo) SetStatus(ctx context.Context, id int64, status string) error {
	return r.Update(ctx, &User{ID: id, Role: r.user.Role, Status: status}, UserUpdateFields{Status: true})
}

func (r *contentModerationTestProxyRepo) Lookup(ctx context.Context, id int64, now time.Time) (ProxyInfo, error) {
	v, e := r.GetByID(ctx, id)
	if e != nil {
		return ProxyInfo{}, e
	}
	return ProxyInfo{URL: v.URL(), Name: v.Name, Status: v.Status, Address: fmt.Sprintf("%s://%s:%d", v.Protocol, v.Host, v.Port), Active: v.IsActive(), Expired: v.IsExpired(now)}, nil
}

func newTestModeration(settings SettingRepository, repo ContentModerationRepository, hash ContentModerationHashCache, groups GroupRepository, users UserCommands, auth APIKeyAuthCacheInvalidator, email RiskSender) *ContentModerationService {
	return NewContentModerationService(settings, repo, hash, groups, users, auth, email, Runtime{Audit: moderationadapter.NewAuditClient(), SnapshotMedia: moderationadapter.SnapshotMedia, Background: func(_ string, fn func()) { go fn() }, CyberText: openai.IsOpenAICyberWarningText, CyberPolicy: openai.DetectOpenAICyberPolicy, ErrorMessage: upstream.ExtractErrorMessage, MissingRow: func(e error) bool { return errors.Is(e, sql.ErrNoRows) }, MissingUser: func(e error) bool { return errors.Is(e, ErrUserNotFound) }})
}

func IsOpenAICyberWarningText(text string) bool { return openai.IsOpenAICyberWarningText(text) }

func makeTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// splitContentModerationText collects the chunks produced for batch auditing.
func splitContentModerationText(text string, chunkSize int, overlap int) []string {
	chunks := make([]string, 0, countContentModerationTextChunks(text, chunkSize, overlap))
	forEachContentModerationTextBatch(text, chunkSize, overlap, contentModerationTextBatchSize, func(_ int, batch []string) {
		chunks = append(chunks, batch...)
	})
	return chunks
}
