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

// Moderation service cases combine local HTTP audit replies with fixture state
// to inspect admission, retained observations, key scheduling, and runtime
// publication. Hash fixtures are mutex-backed memory sets, while Cyber fixtures
// report threshold eligibility without identity database writes. Their
// observations have a narrower scope than the PostgreSQL and Redis adapters.
//
// Locked settings fixtures capture values before a testcase gate and return
// them after concurrent updates, reproducing stale-read publication ordering.
// Ordinary log snapshots copy outer records while keeping nested references
// shared. Helpers identify caller-owned worker cleanup, context-ignoring
// fixture waits, and publication polling that can finish before complete task
// draining, so assertions describe the actual observation point.

// Fixture role labels select regular-account and administrator paths in
// ordinary moderation ban and diagnostic assertions.
const (
	RoleAdmin = "admin" // ordinary admin ban exemption
	RoleUser  = "user"  // ordinary regular actor policy
)

// contentModerationTestProxyRepo resolves fixture proxies and atomically counts
// lookups while its configured map stays stable.
type contentModerationTestProxyRepo struct {
	// proxies and getByIDErr are configured before lookup begins. Atomic getCalls
	// supports observation, while callers keep the map and pointed proxy values
	// stable during concurrent reads.
	proxies    map[int64]*Proxy // stable fixture proxy pointer map
	getByIDErr error            // configured lookup failure result
	getCalls   atomic.Int64     // atomic lookup invocation count
}

// contentModerationRuntimeSettingRepo provides locked policy storage, read
// counters, and one captured-read gate for publication races.
type contentModerationRuntimeSettingRepo struct {
	// mu protects values, getValueCalls, getMultipleCalls, getMultipleErr,
	// getMultipleStart, and getMultipleWait. GetMultiple copies values and
	// consumes gate references under this lock, then sends or waits after
	// unlocking.
	mu               sync.Mutex        // fixture storage and gate lock
	values           map[string]string // stored fixture setting strings
	getValueCalls    int               // individual read invocation count
	getMultipleCalls int               // batch read invocation count
	getMultipleErr   error             // injected batch read failure
	// getMultipleStart and getMultipleWait gate the next successful batch read. A
	// failed read leaves the gate armed; callers drain the notification and
	// release the wait because these fixture waits ignore context cancellation.
	getMultipleStart chan<- struct{} // captured read notification channel
	getMultipleWait  <-chan struct{} // captured read release channel
}

// contentModerationTestSettingRepo is simple map storage for cases with stable
// worker-visible settings or serial writes.
type contentModerationTestSettingRepo struct {
	// values has no mutex. Cases keep it stable while started workers or untracked
	// refresh callbacks read it, and serialize any fixture settings edits
	// separately.
	values map[string]string // fixture setting string map
}

// contentModerationTestRepo retains shallow ordinary logs and serial Cyber
// policy observations for service assertions.
type contentModerationTestRepo struct {
	mu sync.Mutex // ordinary log collection lock
	// mu protects logs append and snapshot/count reads. Stored value copies still
	// share pointer fields, maps, item slices, and media storage, so cases keep
	// those nested values stable after publication.
	logs []ContentModerationLog // shallow retained ordinary observations
	// cyberWarnings is accessed by serial Cyber cases and has no mutex. Its policy
	// helper reports threshold eligibility without identity row locking, database
	// IDs, or a user status write.
	// serial retained Cyber observations
	cyberWarnings []ContentModerationCyberWarning
}

// contentModerationTestHashCache is mutex-backed membership and command history
// used in place of Redis by service cases.
type contentModerationTestHashCache struct {
	// mu protects hashes, recorded, checked, deleted, hasResult, and hasResultUsed
	// during commands and snapshots. Initial fixture values are bound before
	// concurrent use; command contexts are ignored by this in-memory adapter.
	mu            sync.Mutex          // membership and history lock
	hashes        map[string]struct{} // fixture flagged-member set
	recorded      []string            // record invocation member history
	checked       []string            // membership invocation member history
	deleted       []string            // remove invocation member history
	hasResult     bool                // configured membership override value
	hasResultUsed bool                // membership override selection flag
}

// contentModerationTestUserRepo exposes one fixture account and records
// requested IDs and whole-user replacement observations.
type contentModerationTestUserRepo struct {
	// user, updated, and requestedIDs are used serially. GetByID records the
	// requested ID but returns the one stored account without matching it; tests
	// inspect requestedIDs to verify actor selection.
	user         *User   // one stored fixture account
	updated      []User  // copied whole-account update history
	requestedIDs []int64 // requested account lookup history
}

// contentModerationTestAuthCacheInvalidator records account identities passed
// to authentication invalidation in serial cases.
type contentModerationTestAuthCacheInvalidator struct {
	// userIDs is mutated and read serially by these cases after synchronous side
	// effects. This fixture has no mutex or physical authentication cache to
	// invalidate.
	userIDs []int64 // serial invalidation identity history
}

type (
	// User is the fixture name for moderation account snapshot values used in
	// ordinary status and actor-attribution assertions.
	User = UserSnapshot
	// Proxy is the fixture name for stored egress proxy values resolved by the
	// test adapter into moderation connection observations.
	Proxy = egress.Proxy
	// UserUpdateFields carries the selector passed by fixture SetStatus, while
	// fixture Update replaces the complete supplied account value.
	UserUpdateFields struct {
		// Status is passed as a status-write marker, but Update ignores this selector
		// and stores the complete provided account copy regardless of its value.
		Status bool // requested status field marker
	}
)

// TestSplitContentModerationTextKeepsOverlapAndTail supplies text longer than
// one small chunk with a two-rune overlap. It checks the exact two chunks,
// including repeated overlap and the final original text tail.
func TestSplitContentModerationTextKeepsOverlapAndTail(t *testing.T) {
	chunks := splitContentModerationText("abcdefghij", 6, 2)

	require.Equal(t, []string{"abcdef", "efghij"}, chunks)
}

// TestContentModerationTextBatchingDoesNotCreateOverlapOnlyTail supplies an
// exact full chunk and a longer count-only example. It checks that the exact
// tail creates one batch and that the count helper matches selected overlapping
// chunk totals.
func TestContentModerationTextBatchingDoesNotCreateOverlapOnlyTail(t *testing.T) {
	var batches [][]string
	forEachContentModerationTextBatch("abcdef", 6, 2, 2, func(_ int, chunks []string) {
		batches = append(batches, append([]string(nil), chunks...))
	})

	require.Equal(t, [][]string{{"abcdef"}}, batches)
	require.Equal(t, 1, countContentModerationTextChunks("abcdef", 6, 2))
	require.Equal(t, 3, countContentModerationTextChunks("abcdefghijk", 6, 2))
}

// TestContentModerationNormalizePreservesFullOriginalText normalizes text with
// surrounding spaces and an embedded newline. It checks that aggregate text and
// the synthesized review item both retain the full supplied spelling.
func TestContentModerationNormalizePreservesFullOriginalText(t *testing.T) {
	input := ContentModerationInput{Text: "  first line\nsecond line  "}

	input.Normalize()

	require.Equal(t, "  first line\nsecond line  ", input.Text)
	require.Equal(t, input.Text, input.Items[0].Text)
}

// TestContentModerationAuditScopeLimitsUpstreamButStoresFullInput sends mixed
// tool/user text through a local audit server with separate source limits. It
// checks the shorter outgoing aggregate and full retained review items after
// record publication.
func TestContentModerationAuditScopeLimitsUpstreamButStoresFullInput(t *testing.T) {
	// Buffer the observation so the HTTP handler can finish its reply before
	// Check returns and the testcase reads the captured audit input.
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

// TestContentModerationEmptyAuditScopeSkipsObserveQueue disables tool and image
// auditing for a tool-only current turn. It checks an allowed decision, an
// empty observe queue, and no stored fixture log.
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

// TestContentModerationWeightedAPIKeySelectionHonorsPriorityAndFreeze uses two
// keys with a five-to-one scheduling ratio and then freezes the larger weight.
// It checks sixty selections split fifty/ten and subsequent selections
// restricted to the remaining unfrozen key.
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

	// Publish the fixture freeze under the same lock used by weighted selection,
	// so subsequent calls see that exclusion alongside their accumulated eligible key weights.
	svc.keyHealthMu.Lock()
	svc.keyHealth[moderationAPIKeyHash("sk-high")] = &contentModerationKeyHealth{FrozenUntil: time.Now().Add(time.Minute)}
	svc.keyHealthMu.Unlock()
	for range 10 {
		key, ok := svc.nextUsableAPIKey(cfg)
		require.True(t, ok)
		require.Equal(t, "sk-low", key)
	}
}

// TestContentModerationUpdateConfigPersistsKeyMetadataAndAuditScope updates two
// saved metadata entries and audit source controls through a settings fixture.
// It checks returned limits/toggles and exact stored key metadata after JSON
// decoding.
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

// TestContentModerationUpdateConfigAddsKeyWithPriorityAndNote adds a credential
// entry with administrator scheduling attributes to an empty saved pool. It
// checks returned count, priority/note, and its literal key with hash-addressed
// stored metadata.
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

// TestContentModerationAsyncQueueDropsDuplicateBodyAndTracksBytes admits one
// observe task without starting a worker and receives it directly. It checks
// dropped raw body and aggregate duplication, reconstructable item text, a
// positive reservation, and counter release.
func TestContentModerationAsyncQueueDropsDuplicateBodyAndTracksBytes(t *testing.T) {
	svc := &ContentModerationService{runtime: testModerationRuntime(), asyncQueue: make(chan contentModerationTask, 1)}
	content := ContentModerationInput{
		Text:  "完整文本",
		Items: []ContentModerationInputItem{{Index: 0, Source: ContentModerationSourceUser, Type: ContentModerationItemTypeText, Text: "完整文本"}},
	}
	svc.enqueueAsync(ContentModerationCheckInput{Endpoint: "/v1/chat/completions", Body: []byte("raw body")}, &ContentModerationConfig{QueueSize: 1}, content, "hash")

	// Receive directly because this fixture starts no worker; the testcase now
	// owns the accepted task's reserved byte cost and releases it after inspecting
	// its queued representation.
	task := <-svc.asyncQueue
	require.Nil(t, task.input.Body)
	require.Empty(t, task.content.Text)
	require.Equal(t, "完整文本", contentModerationTextFromItems(task.content.Items))
	require.Positive(t, task.bufferedBytes)
	require.Equal(t, task.bufferedBytes, svc.asyncBufferedBytes.Load())
	svc.releaseContentModerationBufferedBytes(task.bufferedBytes)
	require.Zero(t, svc.asyncBufferedBytes.Load())
}

// TestContentModerationAsyncQueueRejectsTaskBeyondByteBudget preloads the byte
// counter just below its cap and attempts another task. It checks queue
// rejection, unchanged reserved bytes, and one additional dropped observation.
func TestContentModerationAsyncQueueRejectsTaskBeyondByteBudget(t *testing.T) {
	svc := &ContentModerationService{runtime: testModerationRuntime(), asyncQueue: make(chan contentModerationTask, 1)}
	svc.asyncBufferedBytes.Store(maxContentModerationBufferedBytes - 1)
	content := ContentModerationInput{Text: "text", Items: []ContentModerationInputItem{{Type: ContentModerationItemTypeText, Text: "text"}}}

	svc.enqueueAsync(ContentModerationCheckInput{}, &ContentModerationConfig{QueueSize: 1}, content, "hash")

	require.Empty(t, svc.asyncQueue)
	require.Equal(t, maxContentModerationBufferedBytes-1, svc.asyncBufferedBytes.Load())
	require.Equal(t, int64(1), svc.asyncDropped.Load())
}

// TestContentModerationObserveQueueOverflowRecordsIncompleteAudit fills a
// one-task observe queue before submitting current-turn text. It checks allowed
// admission plus an inline error log with incomplete audit status,
// request-level failure, and retained input text.
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
	// Preload a placeholder while no worker is running, so a consumer cannot free
	// this slot before Check samples queue length and chooses inline error
	// recording.
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

// TestContentModerationCheckAuditsTextBeyondFirstChunkAndStoresFullInput places
// a risk marker beyond the first text chunk at a local audit endpoint. It
// checks blocking from the later score and retained complete input with two
// successful audit units.
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

// TestContentModerationBatchFailureFallsBackToSingleTextUnits makes the local
// endpoint reject a multi-text batch as unsupported. It checks one batch call
// plus two individual calls, an allowed decision, and complete recorded audit
// units.
func TestContentModerationBatchFailureFallsBackToSingleTextUnits(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload struct {
			Input json.RawMessage `json:"input"` // JSON audit input field value
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		var batch []string
		// A failed array decode selects the ordinary reply path, allowing scalar
		// retries to succeed while this fixture rejects decoded multi-text batches.
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

// TestContentModerationTransientBatchFailureDoesNotFanOut makes the local
// endpoint fail a multi-chunk request with HTTP 503 and retry disabled. It
// checks one call, allowed admission, and both failed units in an incomplete
// record.
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

// TestContentModerationCheckAuditsAllImagesAndSnapshotsOnlyHit returns
// different scores for two inline image references at a local audit endpoint.
// It checks two audit calls, blocking, and one ready retained snapshot
// corresponding to the flagged image.
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

// TestContentModerationPartialFailureStillBlocksAndRecordsFailure fails one
// image audit and flags another image in the same request. It checks blocking
// from the successful hit and an incomplete record containing one image
// failure.
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

// TestContentModerationAllFailuresFailOpenAndAlwaysRecord makes the local audit
// endpoint return HTTP 503 for the sole input unit. It checks allowed
// admission, no block, and an error record even when successful non-hit logging
// is disabled.
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

// TestContentModerationCallRoutesThroughProxy uses an unresolvable upstream
// name and a local forward proxy answering the audit request. It checks proxy
// request URI form and a positive proxied count after the successful call.
func TestContentModerationCallRoutesThroughProxy(t *testing.T) {
	var proxied atomic.Int64
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A forward proxy receives an absolute URI for an HTTP target.
		if !strings.HasPrefix(r.RequestURI, "http://moderation-proxy-test.invalid") {
			t.Errorf("expected absolute-URI proxy request, got %q", r.RequestURI)
		}
		proxied.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Reply from the forward proxy with an empty-score result; request URI and
		// proxied count are observed by this case, while response-write errors are
		// ignored.
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

// TestContentModerationProxyResolveFailureDoesNotFallBackToDirect fails stored
// proxy lookup while a directly reachable local upstream is available. It
// checks the resolution error and zero direct requests after the attempted
// audit.
func TestContentModerationProxyResolveFailureDoesNotFallBackToDirect(t *testing.T) {
	var direct atomic.Int64
	directSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		direct.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// A reply-capable direct endpoint exposes accidental routing through its
		// counter. The rejection case ignores callback write errors because a correct
		// resolution failure never reaches this endpoint.
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

// TestContentModerationProxyURLResolutionCached resolves the same stored proxy
// five times inside the ordinary cache lifetime. It checks a single fixture
// repository lookup, without dialing that proxy endpoint.
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

// TestContentModerationUpdateConfigProxyIDSemantics updates a selected proxy,
// omits its field, clears it with zero, and supplies an absent ID. It checks
// retained/cleared returned selection and failed validation for the nonexistent
// proxy.
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

// TestContentModerationTestAPIKeysProxySemantics tests an explicit credential
// first through the saved proxy and then with a zero direct override. It checks
// the corresponding local servers receive requests and that direct selection
// leaves the prior proxy count unchanged.
func TestContentModerationTestAPIKeysProxySemantics(t *testing.T) {
	var proxied atomic.Int64
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Reply from the selected proxy/direct fixture and observe its request
		// counter. This callback ignores response-write errors; successful probe
		// status and routing counts are the assertions for these inputs.
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{Flagged: false}}})
	}))
	defer proxySrv.Close()
	proxyAddr := strings.TrimPrefix(proxySrv.URL, "http://")
	// This fixture parser assumes an IPv4 host:port server URL and ignores its
	// delimiter and numeric errors; later proxy-call assertions consume the constructed address.
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
		// Reply from the selected proxy/direct fixture and observe its request
		// counter. This callback ignores response-write errors; successful probe
		// status and routing counts are the assertions for these inputs.
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

// TestContentModerationRuntimeSnapshotCachesSettings checks twenty clean
// requests through a keyword-only runtime snapshot with a long TTL. It checks
// one batch settings read and no dedicated value reads from those hot-path
// checks.
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

// TestContentModerationRuntimeSnapshotUpdateConfigIsImmediate loads old keyword
// policy, persists a new keyword, and checks it before TTL expiry. It checks
// the new block and reuse of the initial batch settings read count.
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

// TestContentModerationRuntimeSnapshotUpdateWinsOverInitialLoad holds a fixture
// settings read after old values are captured and starts a configuration update
// concurrently. It checks the update waits for initial publication, then that
// new keywords block while old keywords allow.
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
			// On an early assertion exit, release the context-ignoring captured read so
			// it and any waiting update can leave the refresh lock.
			close(releaseRefresh)
		}
	}()
	repo.blockNextMultiple(refreshStarted, releaseRefresh)

	initialCheckDone := make(chan error, 1)
	// Start the initial read before submitting the update, and send its eventual
	// result through a buffered channel so releasing the gate can publish both
	// completions.
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
	// Submit new keyword policy only after the initial read has captured old
	// values; the buffered result channel reports when the refresh lock permits
	// publication.
	go func() {
		keywords := []string{"new-keyword"}
		_, updateErr := svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{
			BlockedKeywords: &keywords,
		})
		updateDone <- updateErr
	}()
	// The short interval checks premature completion while the read gate remains
	// held; later channel joins establish completion before checking final keyword decisions.
	select {
	case updateErr := <-updateDone:
		require.NoError(t, updateErr)
		t.Fatal("configuration update completed before the initial load released its lock")
	case <-time.After(10 * time.Millisecond):
	}

	// Release the old captured read before joining update completion, so its held
	// refresh lock no longer prevents the saved new policy from being published.
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

// TestContentModerationRuntimeSnapshotRefreshFailureKeepsStaleConfig loads
// keyword policy, expires it immediately, and then fails batch settings reads.
// It checks stale blocking continues and an additional refresh read was
// attempted.
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

// TestContentModerationRuntimeSnapshotRefreshFailureBacksOff publishes an
// expired copied snapshot and fails its reload. It checks one hundred later
// requests still block on stale policy and the observed batch-read count
// remains two; it does not join the reload callback before that loop.
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
	// Copy the snapshot header before changing its load time; immutable policy
	// and matcher storage stay shared while this test publishes an expired cache entry.
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

// TestContentModerationRuntimeSnapshotRefreshReusesUnchangedMatcher expires a
// snapshot without changing stored JSON and waits for renewed load time. It
// checks shared config/matcher pointers and exactly one additional settings
// read.
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
	// Copy the snapshot header before changing its load time; immutable policy
	// and matcher storage stay shared while this test publishes an expired cache entry.
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

// TestContentModerationRuntimeSnapshotUpdateWinsOverInFlightRefresh holds a
// reload after capturing old values while a dedicated update waits for the
// refresh lock. It checks the update cannot finish early and that new keyword
// policy wins after the gate is released.
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
	// Copy the snapshot header before changing its load time; immutable policy
	// and matcher storage stay shared while this test publishes an expired cache entry.
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
	// Request the saved replacement after the stale-snapshot check launches and
	// gates a reload; the completion channel observes whether that update waits
	// for the reload lock.
	go func() {
		keywords := []string{"new-keyword"}
		_, updateErr := svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{
			BlockedKeywords: &keywords,
		})
		updateDone <- updateErr
	}()
	// The short interval checks premature completion while the read gate remains
	// held; later channel joins establish completion before checking final keyword decisions.
	select {
	case updateErr := <-updateDone:
		require.NoError(t, updateErr)
		t.Fatal("configuration update completed before the in-flight refresh released its lock")
	case <-time.After(10 * time.Millisecond):
	}

	// Release the old captured read before joining update completion, so its held
	// refresh lock no longer prevents the saved new policy from being published.
	close(releaseRefresh)
	require.NoError(t, <-updateDone)
	decision, err = svc.Check(context.Background(), runtimeCacheTestInput("new-keyword"))
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	decision, err = svc.Check(context.Background(), runtimeCacheTestInput("old-keyword"))
	require.NoError(t, err)
	require.True(t, decision.Allowed)
}

// TestContentModerationRuntimeSnapshotConcurrentReadAndReplace runs eight
// clean-request readers while twenty keyword replacements are persisted
// serially. It joins every reader and checks no request error or unexpected
// blocking decision was returned.
func TestContentModerationRuntimeSnapshotConcurrentReadAndReplace(t *testing.T) {
	repo := &contentModerationRuntimeSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: runtimeCacheTestConfig(t, "blocked-0"),
	}}
	svc := runtimeCacheTestService(repo, time.Hour)
	_, err := svc.Check(context.Background(), runtimeCacheTestInput("clean prompt"))
	require.NoError(t, err)

	// Each of eight readers reports at most one error into an eight-slot channel;
	// this capacity permits joining readers before the caller drains their observations.
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
		// Duration formatting supplies distinct fixture keyword suffixes for each
		// replacement.
		keywords := []string{"blocked-" + time.Duration(i).String()}
		_, err := svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{
			BlockedKeywords: &keywords,
		})
		require.NoError(t, err)
	}
	// Join every possible sender before closing error collection; the final loop
	// can then drain observations until channel closure without racing another send.
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

// TestBuildContentModerationLog_RedactsInputExcerpt builds an ordinary summary
// from text containing a recognizable credential. It checks the credential
// disappears from InputExcerpt and a redaction marker is present.
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
	require.Contains(t, log.InputExcerpt, "[REDACTED]")
}

// TestContentModerationConfigNormalize_NonHitRetentionMaxThreeDays supplies a
// non-hit retention period above the supported cap. It checks normalization
// selects three days while the rest of the default policy remains fixture
// input.
func TestContentModerationConfigNormalize_NonHitRetentionMaxThreeDays(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.NonHitRetentionDays = 30

	cfg.normalize()

	require.Equal(t, 3, cfg.NonHitRetentionDays)
}

// TestNormalizeBlockedKeywords_TrimsDedupesAndCaps supplies spaced,
// case-duplicate, blank, and repeated keyword values. It checks retained first
// spelling and order after normalization; this fixture does not reach the
// configured entry or rune caps.
func TestNormalizeBlockedKeywords_TrimsDedupesAndCaps(t *testing.T) {
	out := normalizeBlockedKeywords([]string{"  foo ", "FOO", "", "bar", "baz", "bar"})
	require.Equal(t, []string{"foo", "bar", "baz"}, out)
}

// TestMatchBlockedKeyword_CaseInsensitiveSubstring supplies a mixed-case
// matching substring, clean text, and an absent keyword list. It checks
// returned configured spelling for the hit and false results for both misses.
func TestMatchBlockedKeyword_CaseInsensitiveSubstring(t *testing.T) {
	keyword, hit := matchBlockedKeyword("Please ignore the BadWord here", []string{"badword"})
	require.True(t, hit)
	require.Equal(t, "badword", keyword)

	_, hit = matchBlockedKeyword("clean prompt", []string{"badword"})
	require.False(t, hit)

	_, hit = matchBlockedKeyword("anything", nil)
	require.False(t, hit)
}

// TestContentModerationCheck_PreBlockKeywordHitSkipsUpstreamCall submits a
// configured mixed-case keyword hit under pre-block mode with a local endpoint
// present. It checks keyword rejection, no API request, and stored
// category/keyword observations after record publication.
func TestContentModerationCheck_PreBlockKeywordHitSkipsUpstreamCall(t *testing.T) {
	upstreamCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		// Supply a reply for any unintended API call so the keyword-bypass case can
		// inspect its invocation flag. Writer errors remain outside this callback
		// assertion.
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

// TestContentModerationCheck_KeywordsIgnoredInObserveMode submits a configured
// keyword while observe mode is enabled. It checks the immediate allowed
// action; the local endpoint counter is not asserted or joined by this case.
func TestContentModerationCheck_KeywordsIgnoredInObserveMode(t *testing.T) {
	upstreamHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		// The observe case asserts immediate admission and leaves queued execution
		// unjoined. Its callback supplies safe scores and ignores response-write
		// errors after that admission observation.
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

// TestContentModerationCheck_KeywordOnlyStrategySkipsAPIOnMiss submits clean
// text under pre-block keyword-only policy with a potentially flagging
// endpoint. It checks allowed admission, no API request, and no fixture log for
// that keyword miss.
func TestContentModerationCheck_KeywordOnlyStrategySkipsAPIOnMiss(t *testing.T) {
	upstreamCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		// Provide a potentially flagging reply for any unintended audit call. This
		// keyword-only miss case checks the invocation flag and ignores
		// response-write errors from that fixture.
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

// TestContentModerationCheck_APIOnlyStrategyIgnoresKeywordList submits a
// configured keyword while the local endpoint returns a safe score under
// API-only policy. It checks the API was called and the decision is allowed
// with no keyword action.
func TestContentModerationCheck_APIOnlyStrategyIgnoresKeywordList(t *testing.T) {
	upstreamCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		// Provide a safe fixed score for the API-only admission decision while
		// recording invocation. The fixture ignores writer errors, and the case
		// inspects the resulting service decision.
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

// TestNormalizeKeywordBlockingMode_UnknownFallsBackToDefault checks empty,
// unknown, and accepted keyword strategy strings. It protects combined-mode
// defaulting alongside exact accepted keyword-only and API-only values, without
// testing every normalized spelling.
func TestNormalizeKeywordBlockingMode_UnknownFallsBackToDefault(t *testing.T) {
	require.Equal(t, ContentModerationKeywordModeKeywordAndAPI, normalizeKeywordBlockingMode(""))
	require.Equal(t, ContentModerationKeywordModeKeywordAndAPI, normalizeKeywordBlockingMode("bogus"))
	require.Equal(t, ContentModerationKeywordModeKeywordOnly, normalizeKeywordBlockingMode("keyword_only"))
	require.Equal(t, ContentModerationKeywordModeAPIOnly, normalizeKeywordBlockingMode("api_only"))
}

// TestContentModerationCheck_ModelFilterAllAuditsEveryModel uses keyword
// blocking with all-model scope for two requested names. It checks both
// decisions block and two corresponding records are published by the fixture.
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

// TestContentModerationCheck_ModelFilterIncludeOnlyAuditsListedModels uses an
// include list with one requested model and matching keyword content. It checks
// the listed model blocks and is recorded while the other model receives
// allowed admission.
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

// TestContentModerationCheck_ModelFilterExcludeSkipsListedModels uses an
// exclude list with one requested model and matching keyword content. It checks
// the remaining model blocks and is recorded while the excluded model receives
// allowed admission.
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

// TestContentModerationLoadConfig_LegacyConfigDefaultsModelFilterToAll loads
// saved JSON with no model-filter field. It checks default all-model scope, an
// empty names list, and admission for the two requested model names.
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

// TestContentModerationCheck_ModelFilterUsesRequestedModelNotBodyModel supplies
// a selected requested model and a different body model under include scope. It
// checks keyword blocking and the requested-model value retained in the fixture
// log.
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

// TestContentModerationUpdateConfig_AppendsAndDeletesAPIKeys deletes one saved
// credential by hash and appends a new key alongside a duplicate. It checks
// remaining order, returned masks/count, and literal saved credentials after
// decoding.
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

// TestContentModerationUpdateConfig_ReplacesAPIKeysWhenRequested supplies
// replacement credentials and a deletion list together. It checks replacement
// leaves one supplied key in the view and saved policy, independent of prior
// key order.
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

// TestContentModerationUpdateConfig_SavesCustomThresholds supplies a valid
// category override, excessive category value, and unknown category. It checks
// the accepted score, upper clamp, and omitted unknown key in both returned and
// saved thresholds.
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

// TestContentModerationInput_NormalizeKeepsAndBuildsAllImages normalizes
// aggregate text with two inline image references and no review items. It
// checks retained image order and the text-plus-two-image parts produced for
// API input.
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

// TestBuildModerationTestInputAllowsMultipleImages supplies a prompt and two
// valid base64 data references to probe construction. It checks successful
// construction, an accepted image count of two, and a nonnil input value.
func TestBuildModerationTestInputAllowsMultipleImages(t *testing.T) {
	input, count, err := buildModerationTestInput("check image", []string{
		"data:image/png;base64,Zmlyc3Q=",
		"data:image/png;base64,c2Vjb25k",
	})

	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NotNil(t, input)
}

// TestContentModerationCheck_OpenAIResponsesRecordsNonHitForCodexPayload sends
// developer content plus consecutive user messages through a local Responses
// audit fixture. It checks the combined user text in outgoing input and an
// allowed non-hit record.
func TestContentModerationCheck_OpenAIResponsesRecordsNonHitForCodexPayload(t *testing.T) {
	var moderationRequest moderationAPIRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/moderations", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&moderationRequest))
		// Return the safe score after captured request decoding; the case observes
		// outgoing input and the retained allowed log, while this fixture ignores
		// response-write errors.
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

// TestContentModerationCheck_PreBlockBlocksCodexResponsesLatestUserInput sends
// consecutive user entries and developer text while the local audit returns a
// threshold hit. It checks custom block status/message and joined selected user
// text in both outgoing input and retained summary.
func TestContentModerationCheck_PreBlockBlocksCodexResponsesLatestUserInput(t *testing.T) {
	var moderationRequest moderationAPIRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/moderations", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&moderationRequest))
		// Return the threshold hit for the captured Responses input; decision and
		// retained-text assertions consume that result, while this fixture ignores
		// response-write errors.
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

// TestContentModerationStatusTracksPreBlockSyncMetrics sends one flagged and
// one safe text input through a deliberately delayed local endpoint. It checks
// two completed decisions, their blocked/allowed counts, zero active/error
// counts, and a positive sampled average latency.
func TestContentModerationStatusTracksPreBlockSyncMetrics(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		score := 0.01
		if requestCount == 1 {
			score = 0.9
		}
		// Introduce measurable fixture latency for integer-millisecond status averaging.
		time.Sleep(5 * time.Millisecond)
		// Return each selected score after injected latency so completed-decision
		// counters can be inspected. Writer errors are ignored by this fixture, whose
		// service outcomes are asserted below.
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

// TestContentModerationStatusTracksPreBlockAPIKeyLoad sends four safe
// synchronous audits through two equally weighted credentials. It checks total
// calls, available and active counts, and two successful completed attempts for
// each displayed key.
func TestContentModerationStatusTracksPreBlockAPIKeyLoad(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return successful scores for per-key attempt counting. This fixture ignores
		// response-write errors; the case checks completed success, error, and
		// activity observations after the calls.
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

// TestContentModerationStatusTracksPreBlockLocalBlocks sends one keyword hit
// and one miss under keyword-only pre-block policy. It checks two counted
// decisions split between one blocked and one allowed, with no error outcome.
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

// TestBuildContentModerationTestAuditResult_UsesConfiguredThresholdsOnly
// supplies provider Flagged true with a score below the local default
// threshold. It checks local false flagging, maximum and composite display
// scores, and the effective threshold.
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

// TestContentModerationCallModeration_400DoesNotFreezeAPIKey returns a
// request-shape HTTP 400 while retry count is high for a fresh key. It checks
// one request, error status, no failure increment, and no freeze on that
// initially unfrozen state.
func TestContentModerationCallModeration_400DoesNotFreezeAPIKey(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusBadRequest)
		// Send a diagnostic body after the chosen HTTP status; this fixture ignores
		// body-write errors because the selected case observes received status and
		// key-health classification.
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

// TestContentModerationCallModeration_FreezesByHTTPStatus returns selected
// authentication, throttle, and server statuses from local endpoints with
// retries disabled. It checks frozen display state, one failure, received
// status, and a remaining duration inside each fixture tolerance.
func TestContentModerationCallModeration_FreezesByHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string // selected freeze scenario label
		statusCode int    // fixture response HTTP status
		// minFreeze and maxFreeze bound the remaining duration observed after the
		// request and status read. Their tolerance admits elapsed fixture work while
		// still distinguishing the selected status freeze classes.
		minFreeze time.Duration // minimum accepted remaining freeze
		maxFreeze time.Duration // maximum accepted remaining freeze
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
				// Send a diagnostic body after the chosen HTTP status; this fixture ignores
				// body-write errors because the selected case observes received status and
				// key-health classification.
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

// TestContentModerationTestAPIKeys_400DoesNotFreezeAPIKey probes a fresh
// explicit credential against a local HTTP-400 endpoint. It checks the overall
// probe succeeds with an error item, received status, zero freeze-count
// increment, and no active freeze.
func TestContentModerationTestAPIKeys_400DoesNotFreezeAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		// Send a diagnostic body after the chosen HTTP status; this fixture ignores
		// body-write errors because the selected case observes received status and
		// key-health classification.
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

// TestContentModerationCheck_PreHashUsesRedisHashCache prepopulates the
// mutex-backed hash fixture and submits the same text identity. It checks
// cached-hash blocking and message/hash fields, one membership read, and a
// stored hash-block log with no ordinary ban effect.
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

// TestContentModerationCheck_HashBlockLogsDoNotIncreaseNextViolationCount
// stores a prior hash-block fixture log and then obtains a new external score
// hit. It checks the subsequent ordinary block has violation count one and the
// two action labels remain distinct.
func TestContentModerationCheck_HashBlockLogsDoNotIncreaseNextViolationCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a fresh ordinary score hit after the preloaded hash-block history.
		// This fixture ignores write errors; the case inspects the later counted log
		// and action.
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

// TestContentModerationAutoBanSkipsAdminProvider captures the default logger
// while an admin actor reaches the ordinary hit threshold. It checks retained
// violation count, no disablement/invalidation, and diagnostic
// actor/role/count/threshold attributes before logger restoration.
func TestContentModerationAutoBanSkipsAdminProvider(t *testing.T) {
	var slogOutput bytes.Buffer
	// This serial case replaces the package-wide logger for diagnostic assertions;
	// registered cleanup restores the earlier logger after the captured metadata is inspected.
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

// TestContentModerationAutoBanDisablesRegularUserAtThreshold adds a prior
// timestamped hit and supplies a regular actor at the next threshold. It checks
// counted violations, a newly disabled fixture user, and authentication
// invalidation for that actor.
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

// TestContentModerationTeamAttributionAutoBanDisablesActorOnly supplies
// separate team member, paying owner, and team identifiers for one ordinary
// hit. It checks stored attribution and that lookup, status update, and
// invalidation target the member.
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

// TestContentModerationAdminBelowBanThresholdRecordsViolationOnly supplies an
// admin actor with the first hit below threshold. It checks one recorded
// violation and active fixture status with no user update or invalidation.
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

// TestContentModerationCheck_PreBlockFlaggedWritesRedisHashCache submits a
// threshold-hitting input and repeats it after hash publication in the
// mutex-backed fixture. It checks the first API block becomes a later hash
// block, with one endpoint call and distinct stored actions.
func TestContentModerationCheck_PreBlockFlaggedWritesRedisHashCache(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		// Return a hit for initial hash recording before the repeated cache check.
		// Writer errors are ignored here, while one API call and distinct recorded
		// actions are asserted.
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

// TestContentModerationDeleteFlaggedInputHash_NormalizesAndDeletes deletes an
// uppercase version of a stored hexadecimal member in the hash fixture. It
// checks normalized identity, successful first removal, absent membership,
// recorded delete arguments, and false repeated removal.
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

// TestContentModerationClearFlaggedInputHashesAndStatusCount starts with two
// mutex-backed fixture members and reads status around a clear call. It checks
// count two before clearing, a returned count two, and zero afterward; it does
// not exercise Redis command interleaving.
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

// TestContentModerationCheck_AsyncFlaggedWritesRedisHashCache calls checkSync
// directly with an observe policy and a queue-delay value. It checks no block
// plus a recorded fixture hash and log, without scheduling the ordinary observe
// queue path.
func TestContentModerationCheck_AsyncFlaggedWritesRedisHashCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Supply the hit consumed by direct checkSync with queue-delay metadata. This
		// fixture ignores write errors, and the case checks hash/log publication from
		// that call.
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

// TestContentModerationUnbanUser_ActivatesUserAndInvalidatesAuthCache supplies
// a disabled fixture user to administrator reactivation. It checks active
// result/status, one user update, and authentication invalidation for the
// selected actor ID.
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

// TestContentModerationUnbanUser_ActiveUserOnlyInvalidatesAuthCache supplies an
// already-active fixture user to administrator reactivation. It checks active
// result and authentication invalidation with an empty status-update
// observation list.
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

// TestIsOpenAICyberWarningText supplies known Cyber warning wording, a Cyber
// link, and ordinary error text through the wrapper. It checks each selected
// recognition result, without querying an external provider policy.
func TestIsOpenAICyberWarningText(t *testing.T) {
	cases := []struct {
		name string // selected classifier scenario label
		text string // supplied warning classifier text
		want bool   // expected returned recognition result
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

// TestContentModerationRecordCyberWarning_ExtractsResponsesFailedError supplies
// nested response.failed message evidence under enabled risk control. It checks
// warning creation and the extracted Cyber phrase retained in the fixture
// observation.
func TestContentModerationRecordCyberWarning_ExtractsResponsesFailedError(t *testing.T) {
	// The fixture policy uses controlled finite thresholds and JSON-supported
	// values. Ignore encoding error here while this case exercises Cyber
	// recognition and policy outcomes from those supplied settings.
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

// TestContentModerationRecordCyberWarning_RecordsCyberPolicyCode supplies a
// nested cyber_policy code and message to warning recording. It checks one
// fixture warning and exact selected text returned from that coded evidence.
func TestContentModerationRecordCyberWarning_RecordsCyberPolicyCode(t *testing.T) {
	// The fixture policy uses controlled finite thresholds and JSON-supported
	// values. Ignore encoding error here while this case exercises Cyber
	// recognition and policy outcomes from those supplied settings.
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

// TestContentModerationCyberSessionBlockGroupInScope_RespectsRiskControlGroups
// checks selected, unselected, all-group, and master-disabled combinations in
// fixture settings. It protects the returned session-scope boolean for each
// group policy case.
func TestContentModerationCyberSessionBlockGroupInScope_RespectsRiskControlGroups(t *testing.T) {
	selectedGroupID := int64(101)
	otherGroupID := int64(202)
	tests := []struct {
		name string // selected session-scope scenario label
		// riskControlEnabled, allGroups, groupIDs, and groupID select the fixture
		// master gate and group membership, with want recording their combined
		// expected result. Ordinary audit Enabled remains its default in these
		// Cyber-scope cases.
		riskControlEnabled bool    // fixture risk master switch
		allGroups          bool    // fixture all-group admission switch
		groupIDs           []int64 // fixture selected group identifiers
		groupID            *int64  // queried optional group identifier
		want               bool    // expected session-scope admission result
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

// TestContentModerationRecordCyberWarning_SkipsGroupOutOfScope supplies
// recognized Cyber text from a group outside the selected list. It checks a nil
// returned warning and no fixture Cyber insertion.
func TestContentModerationRecordCyberWarning_SkipsGroupOutOfScope(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.AllGroups = false
	cfg.GroupIDs = []int64{101}
	// The fixture policy uses controlled finite thresholds and JSON-supported
	// values. Ignore encoding error here while this case exercises Cyber
	// recognition and policy outcomes from those supplied settings.
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

// TestContentModerationRecordCyberWarning_SkipsModelOutOfScope supplies
// recognized Cyber text for a requested model outside an include list. It
// checks a nil returned warning and no fixture Cyber insertion.
func TestContentModerationRecordCyberWarning_SkipsModelOutOfScope(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.ModelFilter = ContentModerationModelFilter{
		Type:   ContentModerationModelFilterInclude,
		Models: []string{"gpt-5"},
	}
	// The fixture policy uses controlled finite thresholds and JSON-supported
	// values. Ignore encoding error here while this case exercises Cyber
	// recognition and policy outcomes from those supplied settings.
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

// TestContentModerationRecordCyberWarning_DefaultRecordsWithoutBan supplies
// warning and prompt text containing recognizable credentials under default
// Cyber policy. It checks raw retained review text, count one, false ban state,
// and no ordinary user updates or invalidation.
func TestContentModerationRecordCyberWarning_DefaultRecordsWithoutBan(t *testing.T) {
	userID := int64(1001)
	// The fixture policy uses controlled finite thresholds and JSON-supported
	// values. Ignore encoding error here while this case exercises Cyber
	// recognition and policy outcomes from those supplied settings.
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
	require.NotContains(t, repo.cyberWarnings[0].PromptExcerpt, "[REDACTED]")
	require.Equal(t, 1, repo.cyberWarnings[0].ViolationCount)
	require.False(t, repo.cyberWarnings[0].AutoBanned)
	require.Empty(t, userRepo.updated)
	require.Empty(t, invalidator.userIDs)
}

// TestContentModerationRecordCyberWarning_BansWhenCyberThresholdReached
// configures a first-warning Cyber threshold in the repository fixture. It
// checks reported AutoBanned and authentication invalidation, while the fixture
// reports policy outcome without writing the separate user-command fixture.
func TestContentModerationRecordCyberWarning_BansWhenCyberThresholdReached(t *testing.T) {
	userID := int64(1001)
	cfg := defaultContentModerationConfig()
	cfg.CyberAutoBanEnabled = true
	cfg.CyberBanThreshold = 1
	// The fixture policy uses controlled finite thresholds and JSON-supported
	// values. Ignore encoding error here while this case exercises Cyber
	// recognition and policy outcomes from those supplied settings.
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

// TestContentModerationRecordCyberWarning_CountsExistingWindowWarnings preloads
// one recent Cyber fixture observation before submitting another under
// threshold two. It checks count two, a reported ban, and authentication
// invalidation for the actor.
func TestContentModerationRecordCyberWarning_CountsExistingWindowWarnings(t *testing.T) {
	userID := int64(1001)
	cfg := defaultContentModerationConfig()
	cfg.CyberAutoBanEnabled = true
	cfg.CyberBanThreshold = 2
	// The fixture policy uses controlled finite thresholds and JSON-supported
	// values. Ignore encoding error here while this case exercises Cyber
	// recognition and policy outcomes from those supplied settings.
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

// TestContentModerationNoMediaRetention submits flagged image content under
// observe policy with sensitive retention enabled. It checks empty
// excerpt/items/media and retained score/action observations plus hash-cache
// recording after asynchronous publication.
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
	// The hash fixture observes recorded risk identity after review content is
	// stripped; score and action fields remain available for administrator review.
	require.NotEmpty(t, hashCache.snapshotRecorded())
	require.NotEmpty(t, log.CategoryScores)
	require.NotEmpty(t, log.Action)
}

// TestContentModerationMediaRetentionControl submits the same kind of flagged
// observe content with ordinary retention enabled. It checks retained
// summary/items and exactly one ready snapshot containing nonempty image bytes.
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

// newExpandedModerationTestService constructs and starts a pre-block service
// with one key and no audit retry. It uses immutable serialized fixture
// settings and returns the log observer; Stop is not registered here, so
// callers own the started runtime lifetime.
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

// decodeModerationRawInput decodes the raw input field from an audit fixture
// request and fails on JSON errors. Callers supply valid expected moderation
// request shapes; the returned RawMessage retains the decoded field bytes for
// further fixture inspection.
func decodeModerationRawInput(t *testing.T, r *http.Request) json.RawMessage {
	t.Helper()
	var payload struct {
		Input json.RawMessage `json:"input"` // JSON audit input field value
	}
	require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
	return payload.Input
}

// decodeModerationTextInputs accepts a decoded string batch or one scalar
// string from the request fixture. Batch decoding is tried first; scalar
// decoding errors fail the test, and callers using element zero supply nonempty
// text input.
func decodeModerationTextInputs(t *testing.T, r *http.Request) []string {
	t.Helper()
	raw := decodeModerationRawInput(t, r)
	var batch []string
	// Batch input and scalar input have separate JSON shapes; retry scalar decode
	// only after the batch shape fails, matching the API input selected by the caller.
	if err := json.Unmarshal(raw, &batch); err == nil {
		return batch
	}
	var single string
	require.NoError(t, json.Unmarshal(raw, &single))
	return []string{single}
}

// GetByID records one lookup and returns a configured proxy pointer, injected
// failure, or missing-proxy error. It ignores context, and returned proxy
// storage remains shared with the stable fixture map.
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

// moderationProxyIDPtr allocates an independent integer pointer for explicit
// fixture proxy-selection arguments.
func moderationProxyIDPtr(v int64) *int64 { return &v }

// Get returns a new Setting for a stored fixture value under its mutex. Missing
// keys return ErrSettingNotFound, context is ignored, and this method does not
// contribute to the GetValue read counter.
func (r *contentModerationRuntimeSettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: value}, nil
}

// GetValue records and returns an individual fixture setting read under its
// mutex. Missing keys return ErrSettingNotFound after incrementing the counter;
// context cancellation is ignored by this memory operation.
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

// Set writes one fixture setting and initializes missing map storage under its
// mutex. It ignores context and returns nil, allowing dedicated updates to
// change stored policy while a previously captured batch read is gated.
func (r *contentModerationRuntimeSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	r.values[key] = value
	return nil
}

// GetMultiple captures present selected values and one pending gate while
// holding the fixture mutex. It increments the read count first; an injected
// error returns before consuming the gate. The copied map is independent of
// later setting updates.
//
// The notification and wait run after unlocking, so a concurrent dedicated
// update can save new policy while this read still returns its old captured
// values. Context is ignored for both gate operations; the testcase drains
// start and releases wait to complete the read. Clearing gate fields before
// unlocking restricts the gate to that captured successful read.
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
	// After unlocking, notify that older values have been captured and await the
	// testcase release. Clearing both gate fields earlier lets later reads proceed
	// independently while this context-ignoring read remains held.
	if start != nil {
		start <- struct{}{}
	}
	if wait != nil {
		<-wait
	}
	return out, nil
}

// SetMultiple merges supplied setting strings into fixture storage under its
// mutex. It initializes missing storage, ignores context, and returns nil;
// omitted keys retain their prior fixture values.
func (r *contentModerationRuntimeSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	maps.Copy(r.values, values)
	return nil
}

// GetAll returns an independent map copy of fixture settings while holding its
// mutex. It ignores context and performs no read-count increment, keeping this
// interface method separate from snapshot-load observations.
func (r *contentModerationRuntimeSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.values))
	maps.Copy(out, r.values)
	return out, nil
}

// Delete removes one fixture key under its mutex and returns nil. Missing
// entries are accepted, context is ignored, and subsequent individual reads
// report ErrSettingNotFound for that key.
func (r *contentModerationRuntimeSettingRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

// calls returns individual and batch fixture read counters from one locked
// observation. Callers can compare them while an untracked background refresh
// proceeds, instead of directly reading mutable counter fields.
func (r *contentModerationRuntimeSettingRepo) calls() (getValue, getMultiple int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getValueCalls, r.getMultipleCalls
}

// failMultiple installs the batch-read error under the fixture mutex. Passing
// nil restores successful reads; an already captured successful read keeps its
// earlier copied values and gate behavior.
func (r *contentModerationRuntimeSettingRepo) failMultiple(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getMultipleErr = err
}

// blockNextMultiple arms notification and release channels for the next
// successful batch read. Callers provide a receivable start channel and release
// wait themselves; failed reads leave this gate armed and cancellation does not
// release it.
func (r *contentModerationRuntimeSettingRepo) blockNextMultiple(start chan<- struct{}, wait <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getMultipleStart = start
	r.getMultipleWait = wait
}

// runtimeCacheTestConfig serializes enabled pre-block keyword-only settings for
// the supplied ordered keywords. JSON failures stop the test, and the resulting
// fixture avoids API transport work while exercising runtime snapshot
// publication.
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

// runtimeCacheTestService binds locked setting storage and a log fixture with
// the supplied snapshot TTL. It starts no workers and has no queue; keyword
// records therefore use inline persistence, while reloads use the test runtime
// background launcher.
func runtimeCacheTestService(repo *contentModerationRuntimeSettingRepo, ttl time.Duration) *ContentModerationService {
	return &ContentModerationService{
		runtime:         testModerationRuntime(),
		settingRepo:     repo,
		repo:            &contentModerationTestRepo{},
		runtimeCacheTTL: ttl,
	}
}

// runtimeCacheTestInput embeds text directly inside a Chat JSON fixture and
// fixes the requested model label. Callers supply JSON-safe text without quote,
// escape, or control sequences because this helper does not encode the text
// separately.
func runtimeCacheTestInput(text string) ContentModerationCheckInput {
	return ContentModerationCheckInput{
		Protocol: ContentModerationProtocolOpenAIChat,
		Model:    "risk-cache-test",
		Body:     []byte(`{"messages":[{"role":"user","content":"` + text + `"}]}`),
	}
}

// Get returns a new Setting value from the simple fixture map or
// ErrSettingNotFound. It ignores context and has no synchronization; cases
// serialize mutation or keep the map stable while readers run.
func (r *contentModerationTestSettingRepo) Get(ctx context.Context, key string) (*Setting, error) {
	if value, ok := r.values[key]; ok {
		return &Setting{Key: key, Value: value}, nil
	}
	return nil, ErrSettingNotFound
}

// GetValue reads one simple fixture string or returns ErrSettingNotFound.
// Context is ignored and no mutex is used, so callers keep values stable during
// worker or untracked snapshot-refresh reads.
func (r *contentModerationTestSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", ErrSettingNotFound
}

// Set initializes and writes the simple fixture map, ignoring context and
// returning nil. Callers serialize edits because this adapter has no mutex and
// can also be used by service readers.
func (r *contentModerationTestSettingRepo) Set(ctx context.Context, key, value string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

// GetMultiple copies present selected keys into a new fixture result map and
// omits missing keys. It ignores context and requires stable source storage
// while reading because this simple adapter supplies no mutex.
func (r *contentModerationTestSettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

// SetMultiple merges supplied strings into the simple fixture map and
// initializes it when absent. It ignores context and returns nil; callers
// serialize this mutation against all fixture reads.
func (r *contentModerationTestSettingRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	maps.Copy(r.values, settings)
	return nil
}

// GetAll returns a new map containing the current simple fixture strings. It
// ignores context and requires stable source storage during copying; callers
// can change the returned map independently afterward.
func (r *contentModerationTestSettingRepo) GetAll(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(r.values))
	maps.Copy(out, r.values)
	return out, nil
}

// Delete removes one simple fixture key and returns nil even if it was absent.
// It ignores context; callers serialize mutations because storage is shared
// with the same unsynchronized read methods.
func (r *contentModerationTestSettingRepo) Delete(ctx context.Context, key string) error {
	delete(r.values, key)
	return nil
}

// CreateLog appends a nonnil log value under the ordinary fixture mutex and
// ignores context. It does not assign database ID or creation time; nested
// pointers, maps, and slices remain shared, so cases leave them stable after
// publication.
func (r *contentModerationTestRepo) CreateLog(ctx context.Context, log *ContentModerationLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if log != nil {
		r.logs = append(r.logs, *log)
	}
	return nil
}

// ListLogs is an inert interface fixture returning nil rows, nil pagination,
// and nil error. It ignores context and filters; current cases inspect
// snapshotLogs instead, and this result cannot support a handler expecting page
// metadata.
func (r *contentModerationTestRepo) ListLogs(ctx context.Context, filter ContentModerationLogFilter) ([]ContentModerationLog, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

// CountFlaggedByUserSince counts mutex-protected actor matches that are
// flagged, timestamped at or after since, and not hash blocks. It ignores
// context and does not implement PostgreSQL latest-auto-ban resetting, so cases
// supply the history needed for their selected assertion.
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

// CreateCyberWarning appends a nonnil warning in serial fixture use and fills
// absent creation time. It ignores context and keeps nested references shared;
// database identity, media insertion, and transaction behavior are outside this
// fixture.
func (r *contentModerationTestRepo) CreateCyberWarning(ctx context.Context, warning *ContentModerationCyberWarning) error {
	if warning != nil {
		if warning.CreatedAt.IsZero() {
			warning.CreatedAt = time.Now()
		}
		r.cyberWarnings = append(r.cyberWarnings, *warning)
	}
	return nil
}

// CreateCyberWarningAndApplyUserBan computes a window count, appends one serial
// fixture warning, and returns its AutoBanned value. Missing warnings return
// false, nil. The policy can set that value true; an incoming true value
// remains true even when this policy does not select a ban.
//
// This fixture performs no identity row locking or status command and can
// report a ban with no actor. It assigns missing creation time and keeps nested
// warning references shared, but assigns no database ID. Service cases supply
// fresh warning values and inspect post-return invalidation and metadata; this
// helper does not establish a durable user status change.
func (r *contentModerationTestRepo) CreateCyberWarningAndApplyUserBan(ctx context.Context, warning *ContentModerationCyberWarning, policy ContentModerationCyberWarningPolicy) (bool, error) {
	if warning == nil {
		return false, nil
	}
	count := 1
	if warning.UserID != nil && policy.WindowHours > 0 {
		// Convert policy hours to a cutoff before reading existing fixture
		// observations.
		since := time.Now().Add(-time.Duration(policy.WindowHours) * time.Hour)
		// This in-memory counter reports computed history with nil error; the fake
		// policy adds this warning without simulating database count failures or identity locking.
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

// ListCyberWarnings is an inert fixture returning nil rows, nil pagination, and
// nil error. It ignores filters and context; serial warning cases inspect the
// retained cyberWarnings collection instead of this list capability.
func (r *contentModerationTestRepo) ListCyberWarnings(ctx context.Context, filter ContentModerationCyberWarningFilter) ([]ContentModerationCyberWarning, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

// CountCyberWarningsByUserSince counts serial fixture rows for a matching
// pointed actor strictly after since. It ignores context and has no
// latest-auto-ban reset or mutex; callers avoid concurrent warning writes.
func (r *contentModerationTestRepo) CountCyberWarningsByUserSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	count := 0
	for _, warning := range r.cyberWarnings {
		if warning.UserID != nil && *warning.UserID == userID && warning.CreatedAt.After(since) {
			count++
		}
	}
	return count, nil
}

// GetCyberSummary returns an empty summary fixture and ignores the supplied
// filter and context. It performs no grouping or counts, so current recording
// cases inspect stored warning observations directly.
func (r *contentModerationTestRepo) GetCyberSummary(ctx context.Context, filter ContentModerationCyberWarningFilter) (*ContentModerationCyberSummary, error) {
	return &ContentModerationCyberSummary{}, nil
}

// MarkCyberWarningEmailSent is a successful no-op fixture method for every ID
// and context. It does not mutate retained warning rows, so this adapter cannot
// establish persistence of an email marker.
func (r *contentModerationTestRepo) MarkCyberWarningEmailSent(ctx context.Context, id int64) error {
	return nil
}

// CleanupExpiredLogs returns a zero-delete fixture result with zero FinishedAt
// and ignores context and cutoffs. It does not alter retained logs or warnings,
// so retention deletion is outside these service fixtures.
func (r *contentModerationTestRepo) CleanupExpiredLogs(ctx context.Context, hitBefore time.Time, nonHitBefore time.Time) (*ContentModerationCleanupResult, error) {
	return &ContentModerationCleanupResult{}, nil
}

// snapshotLogs copies the outer ordinary log slice under its mutex for polling
// assertions. Nested pointers, maps, items, and media remain shared with
// retained logs; callers keep those values stable during asynchronous
// observation.
func (r *contentModerationTestRepo) snapshotLogs() []ContentModerationLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ContentModerationLog, len(r.logs))
	copy(out, r.logs)
	return out
}

// requireContentModerationLogCount polls shallow fixture snapshots for the
// exact expected publication count. It fails after the one-second fixture
// budget and returns the observed rows; matching the count is not a join of all
// service workers.
func requireContentModerationLogCount(t *testing.T, repo *contentModerationTestRepo, want int) []ContentModerationLog {
	t.Helper()
	var logs []ContentModerationLog
	// Bound fixture publication waiting independently of the service drain; the
	// observed row count can become ready before a worker finishes all completion bookkeeping.
	require.Eventually(t, func() bool {
		logs = repo.snapshotLogs()
		return len(logs) == want
	}, time.Second, 10*time.Millisecond)
	return logs
}

// requireRecordedHashCount polls copied fixture record history for the exact
// expected count. Its one-second budget bounds the assertion wait, while hash
// publication can precede log insertion and does not establish full task
// completion.
func requireRecordedHashCount(t *testing.T, cache *contentModerationTestHashCache, want int) []string {
	t.Helper()
	var hashes []string
	// Hash recording occurs before log insertion, so its publication assertion
	// owns a separate wait and does not imply that the complete task has finished.
	require.Eventually(t, func() bool {
		hashes = cache.snapshotRecorded()
		return len(hashes) == want
	}, time.Second, 10*time.Millisecond)
	return hashes
}

// GetByID records the requested account ID and returns a copy of the one stored
// fixture user. Missing users return ErrUserNotFound; it ignores context and
// does not match the requested ID against that stored user.
func (r *contentModerationTestUserRepo) GetByID(ctx context.Context, id int64) (*User, error) {
	r.requestedIDs = append(r.requestedIDs, id)
	if r.user == nil {
		return nil, ErrUserNotFound
	}
	clone := *r.user
	return &clone, nil
}

// Update replaces the serial fixture user with a copied supplied value and
// records that copy. Nil users are accepted, context and fields selector are
// ignored, and omitted values in the supplied user remain omitted in the
// replacement.
func (r *contentModerationTestUserRepo) Update(ctx context.Context, user *User, fields UserUpdateFields) error {
	if user == nil {
		return nil
	}
	clone := *user
	r.updated = append(r.updated, clone)
	r.user = &clone
	return nil
}

// InvalidateAuthCacheByUserID appends the supplied identity to a serial fixture
// observation list. It ignores context and performs no cache operation; cases
// inspect the list after the synchronous side effect returns.
func (i *contentModerationTestAuthCacheInvalidator) InvalidateAuthCacheByUserID(ctx context.Context, userID int64) {
	i.userIDs = append(i.userIDs, userID)
}

// RecordFlaggedInputHash initializes membership, adds the literal member, and
// records every invocation under its mutex. It ignores context and performs no
// trim or hash validation, unlike normalization applied by production adapter
// or service callers.
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

// HasFlaggedInputHash records the queried literal member under its mutex and
// returns configured override or current membership. Context is ignored;
// hasResultUsed selects the override even when that override is false.
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

// DeleteFlaggedInputHash records the literal delete request and removes an
// existing fixture member under its mutex. Missing sets or members return
// false; context and real Redis retries are outside this in-memory operation.
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

// ClearFlaggedInputHashes samples and replaces fixture membership under one
// mutex, returning its prior count. It ignores context; this single locked
// fixture operation does not model the production SCARD-before-DEL
// interleaving.
func (c *contentModerationTestHashCache) ClearFlaggedInputHashes(ctx context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	deleted := int64(len(c.hashes))
	c.hashes = map[string]struct{}{}
	return deleted, nil
}

// CountFlaggedInputHashes returns fixture cardinality under its mutex and
// ignores context. Nil membership counts as zero, and this adapter introduces
// no Redis availability or command retry errors.
func (c *contentModerationTestHashCache) CountFlaggedInputHashes(ctx context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return int64(len(c.hashes)), nil
}

// snapshotRecorded returns a new string slice containing record invocation
// history under the fixture mutex. Repeated member recordings remain separate
// observations, and callers can modify the returned slice independently.
func (c *contentModerationTestHashCache) snapshotRecorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.recorded))
	copy(out, c.recorded)
	return out
}

// snapshotChecked returns a new string slice containing membership invocation
// history under the fixture mutex. It counts reads rather than unique members,
// allowing cases to observe exactly which query arguments were used.
func (c *contentModerationTestHashCache) snapshotChecked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.checked))
	copy(out, c.checked)
	return out
}

// hasHash reads literal fixture membership under its mutex for administrator
// removal assertions. Missing sets return false, and this helper does not add a
// membership invocation to checked history.
func (c *contentModerationTestHashCache) hasHash(inputHash string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.hashes[inputHash]
	return ok
}

// snapshotDeleted returns a new slice of delete invocation arguments under its
// mutex. Failed or repeated removals still appear in that history, letting
// cases separate invocation observation from successful removal.
func (c *contentModerationTestHashCache) snapshotDeleted() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.deleted))
	copy(out, c.deleted)
	return out
}

// defaultContentModerationModelFilterTestConfig returns enabled pre-block
// policy with a fixed blocked keyword and otherwise default scope. Cases
// replace ModelFilter before serializing this policy to isolate requested-model
// admission.
func defaultContentModerationModelFilterTestConfig() *ContentModerationConfig {
	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.BlockedKeywords = []string{"secret-token"}
	return cfg
}

// newContentModerationModelFilterTestService serializes supplied policy and
// starts a service with ordinary log and hash fixtures. It registers no stop
// cleanup; callers own the returned runtime and observe queued records through
// the log fixture.
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

// newContentModerationFlaggedLog constructs a timestamped ordinary flagged
// fixture record for the supplied actor. Its block action counts toward
// ordinary policy, while hash-block history is supplied by other cases and
// additional attribution can be set before persistence.
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

// contentModerationIntPtr allocates an independent integer pointer used for
// fixture timing arguments.
func contentModerationIntPtr(v int) *int {
	return &v
}

// newCreativeNoMediaRetentionModerationService starts an observe fixture that
// flags serialized input containing image_url and assigns a safe score to other
// input. The selected cases submit separate text and image units, and a valid
// inline PNG permits production snapshot checks without remote URL fetching.
//
// LIFO cleanup runs service Stop before the HTTP server closes, allowing
// accepted queue work to finish against that endpoint. Fixture decode, marshal,
// and response writes ignore their errors; malformed input or marker text can
// affect this simple classifier, so callers supply the intended unit shapes.
func newCreativeNoMediaRetentionModerationService(t *testing.T) (*ContentModerationService, *contentModerationTestRepo, *contentModerationTestHashCache, string) {
	t.Helper()
	pngBytes := makeTestPNG(t, 2, 2)
	imageDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input any `json:"input"` // JSON audit input field value
		}
		// Decode and reserialize the controlled request for literal marker matching;
		// ignored errors can produce a non-image classification instead of rejecting fixture input.
		_ = json.NewDecoder(r.Body).Decode(&request)
		rawInput, _ := json.Marshal(request.Input)
		if strings.Contains(string(rawInput), "image_url") {
			// Emit the image-marker score used by retained-snapshot assertions; write
			// errors are ignored here and can affect the service outcome observed by the case.
			_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{
				{CategoryScores: map[string]float64{"sexual": 0.99}},
			}})
			return
		}
		// Emit the ordinary text score for input lacking the literal marker; this
		// fixture ignores writer errors and leaves result observation to the service assertions.
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
	// Register stop after server cleanup so LIFO teardown joins accepted service
	// work while its HTTP fixture remains available, then closes the server resource.
	svc.Start()
	t.Cleanup(func() { require.NoError(t, svc.Stop()) })
	return svc, repo, hashCache, imageDataURL
}

// SetStatus replaces the serial fixture user with the requested ID/status and
// existing role. A stored user is required for role lookup; the replacement
// omits email, and Update ignores the supplied Status selector.
func (r *contentModerationTestUserRepo) SetStatus(ctx context.Context, id int64, status string) error {
	return r.Update(ctx, &User{ID: id, Role: r.user.Role, Status: status}, UserUpdateFields{Status: true})
}

// Lookup converts the shared fixture proxy to moderation values using the
// supplied expiry instant. It delegates lookup counting and errors, keeps URL
// credentials for dialing, and constructs a separate address label for
// diagnostics.
func (r *contentModerationTestProxyRepo) Lookup(ctx context.Context, id int64, now time.Time) (ProxyInfo, error) {
	v, e := r.GetByID(ctx, id)
	if e != nil {
		return ProxyInfo{}, e
	}
	return ProxyInfo{URL: v.URL(), Name: v.Name, Status: v.Status, Address: fmt.Sprintf("%s://%s:%d", v.Protocol, v.Host, v.Port), Active: v.IsActive(), Expired: v.IsExpired(now)}, nil
}

// newTestModeration constructs the service with production transport, snapshot,
// and upstream classifiers plus an untracked background launcher. Construction
// starts no workers; callers explicitly Start and Stop any runtime they choose
// to launch.
func newTestModeration(settings SettingRepository, repo ContentModerationRepository, hash ContentModerationHashCache, groups GroupRepository, users UserCommands, auth APIKeyAuthCacheInvalidator, email RiskSender) *ContentModerationService {
	return NewContentModerationService(settings, repo, hash, groups, users, auth, email, Runtime{Audit: moderationadapter.NewAuditClient(), SnapshotMedia: moderationadapter.SnapshotMedia, Background: func(_ string, fn func()) {
		// Launch the reload separately so a Check or worker with stale usable policy
		// can continue before storage finishes; this fixture has no application task
		// owner to join that callback.
		go fn()
	}, CyberText: openai.IsOpenAICyberWarningText, CyberPolicy: openai.DetectOpenAICyberPolicy, ErrorMessage: upstream.ExtractErrorMessage, MissingRow: func(e error) bool { return errors.Is(e, sql.ErrNoRows) }, MissingUser: func(e error) bool { return errors.Is(e, ErrUserNotFound) }})
}

// IsOpenAICyberWarningText delegates fixture recognition to the shared upstream
// OpenAI warning-text classifier.
func IsOpenAICyberWarningText(text string) bool { return openai.IsOpenAICyberWarningText(text) }

// makeTestPNG returns encoded bytes for a transparent RGBA image with
// caller-supplied positive dimensions. Encoding errors fail the testcase; the
// local buffer remains reachable through the returned byte slice for inline
// snapshot fixtures.
func makeTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// splitContentModerationText collects the chunk iterator output into a fixture
// slice in original chunk order. Blank text yields empty output, while chunk
// size and overlap follow the iterator normalization used by production
// batching.
func splitContentModerationText(text string, chunkSize int, overlap int) []string {
	chunks := make([]string, 0, countContentModerationTextChunks(text, chunkSize, overlap))
	forEachContentModerationTextBatch(text, chunkSize, overlap, contentModerationTextBatchSize, func(_ int, batch []string) {
		chunks = append(chunks, batch...)
	})
	return chunks
}
