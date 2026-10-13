package errorpolicy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockErrorPassthroughRepo retains serial rule pointers and supplies preset
// storage failures.
type mockErrorPassthroughRepo struct {
	// rules retains the supplied rule pointers, including objects shared with
	// outer slice copies. Tests serialize mutations, while production publication
	// performs its own nested cloning.
	rules     []*ErrorPassthroughRule // retained fixture rule pointer list
	listErr   error                   // injected authoritative list read error
	getErr    error                   // injected individual rule read error
	createErr error                   // injected rule creation failure
	updateErr error                   // injected rule replacement failure
	deleteErr error                   // injected rule deletion failure
}

// mockErrorPassthroughCache counts serial cache calls and copies outer fixture
// slices.
type mockErrorPassthroughCache struct {
	// rules retains the supplied rule pointers, including objects shared with
	// outer slice copies. Tests serialize mutations, while production publication
	// performs its own nested cloning.
	rules            []*ErrorPassthroughRule // retained fixture rule pointer list
	hasData          bool                    // fixture cached value is available
	getCalled        int                     // fixture cache read invocation count
	setCalled        int                     // fixture cache publication invocation count
	invalidateCalled int                     // fixture cache invalidation invocation count
	notifyCalled     int                     // fixture update notification invocation count
}

// controlledRules snapshots a protected rule and can suspend one read for
// update-order tests.
type controlledRules struct {
	ErrorPassthroughRepository // unused inherited fixture operations
	// mu protects current and block while a List captures its initial snapshot.
	// entered and resume coordinate that one delayed read outside the lock so
	// another fixture Update can replace current while the captured snapshot waits.
	mu      sync.Mutex            // controlled read/write state access lock
	current *ErrorPassthroughRule // current authoritative fixture rule pointer
	entered chan struct{}         // blocked read entry notification channel
	resume  chan struct{}         // test-controlled blocked read continuation channel
	block   bool                  // next fixture read is suspended
}

// callbackRuleCache retains a refresh callback and waits for its
// test-controlled completion.
type callbackRuleCache struct {
	ErrorPassthroughCache // unused inherited fixture operations
	// callback is installed during startup before the test launches it. done is
	// installed before that launch and closed by the test after callback return,
	// allowing StopSubscription to wait for the same active refresh task.
	callback func()        // stored synchronous refresh callback function
	done     chan struct{} // test-controlled callback completion notification channel
}

// newMockErrorPassthroughCache copies the outer rule slice and sets the
// supplied availability flag. Rule pointers remain shared with the fixture
// input; production service compilation performs its own nested ownership
// copies.
func newMockErrorPassthroughCache(rules []*ErrorPassthroughRule, hasData bool) *mockErrorPassthroughCache {
	return &mockErrorPassthroughCache{
		rules:   cloneFixtureRules(rules),
		hasData: hasData,
	}
}

// Get counts the cache read and returns an outer slice copy when hasData is
// true. Missing data returns nil and false; nested rule pointers remain shared
// for these serial fixture assertions.
func (m *mockErrorPassthroughCache) Get(ctx context.Context) ([]*ErrorPassthroughRule, bool) {
	m.getCalled++
	if !m.hasData {
		return nil, false
	}
	return cloneFixtureRules(m.rules), true
}

// Set counts publication, copies the outer rule slice, and marks fixture data
// available. Nested rule objects remain retained rather than cloned; tests
// serialize cache calls and the production service owns compilation copies.
func (m *mockErrorPassthroughCache) Set(ctx context.Context, rules []*ErrorPassthroughRule) error {
	m.setCalled++
	m.rules = cloneFixtureRules(rules)
	m.hasData = true
	return nil
}

// Invalidate counts the call and removes this fixture's cached slice and
// availability flag.
func (m *mockErrorPassthroughCache) Invalidate(ctx context.Context) error {
	m.invalidateCalled++
	m.rules = nil
	m.hasData = false
	return nil
}

// NotifyUpdate counts the notification request and accepts it for fixture
// assertions.
func (m *mockErrorPassthroughCache) NotifyUpdate(ctx context.Context) error {
	m.notifyCalled++
	return nil
}

// SubscribeUpdates accepts registration without storing or invoking its
// handler in this serial cache fixture. Tests needing active callback shutdown
// use callbackRuleCache to control that lifecycle explicitly.
func (m *mockErrorPassthroughCache) SubscribeUpdates(ctx context.Context, handler func()) {
}

// cloneFixtureRules copies outer slice storage while retaining each fixture
// rule pointer. Nil lists remain nil; callers avoid treating this helper as
// the production service's nested rule ownership clone.
func cloneFixtureRules(rules []*ErrorPassthroughRule) []*ErrorPassthroughRule {
	if rules == nil {
		return nil
	}
	out := make([]*ErrorPassthroughRule, len(rules))
	copy(out, rules)
	return out
}

// List returns the retained fixture list when listErr is absent, otherwise nil
// with that error. The context is ignored and source pointers remain borrowed
// until production service cloning handles them.
func (m *mockErrorPassthroughRepo) List(ctx context.Context) ([]*ErrorPassthroughRule, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.rules, nil
}

// GetByID returns getErr first or the first retained rule matching ID, with
// nil for a missing ID. Context cancellation is ignored and returned pointers
// remain shared with serial fixture storage.
func (m *mockErrorPassthroughRepo) GetByID(ctx context.Context, id int64) (*ErrorPassthroughRule, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	for _, r := range m.rules {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, nil
}

// Create returns createErr before modifying storage, otherwise assigns
// len(rules)+1 and appends the supplied pointer. The fixture mutates input ID
// and provides no persistent uniqueness or context-cancellation behavior.
func (m *mockErrorPassthroughRepo) Create(ctx context.Context, rule *ErrorPassthroughRule) (*ErrorPassthroughRule, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	rule.ID = int64(len(m.rules) + 1)
	m.rules = append(m.rules, rule)
	return rule, nil
}

// Update returns updateErr first or replaces the first stored pointer with
// matching ID. An absent ID still returns the supplied rule successfully
// without appending it; context is ignored by this fixture.
func (m *mockErrorPassthroughRepo) Update(ctx context.Context, rule *ErrorPassthroughRule) (*ErrorPassthroughRule, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	for i, r := range m.rules {
		if r.ID == rule.ID {
			m.rules[i] = rule
			return rule, nil
		}
	}
	return rule, nil
}

// Delete returns deleteErr first or removes the first matching rule pointer
// from the retained slice. Missing IDs succeed; tests serialize list mutation
// and the fixture ignores its context.
func (m *mockErrorPassthroughRepo) Delete(ctx context.Context, id int64) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	for i, r := range m.rules {
		if r.ID == id {
			m.rules = append(m.rules[:i], m.rules[i+1:]...)
			return nil
		}
	}
	return nil
}

// newTestService constructs a matcher with authoritative fixture data and an
// already compiled local snapshot. It uses no shared cache; even nil input
// becomes a loaded empty snapshot that avoids lazy reads.
func newTestService(rules []*ErrorPassthroughRule) *ErrorPassthroughService {
	repo := &mockErrorPassthroughRepo{rules: rules}
	svc := &ErrorPassthroughService{
		repo:  repo,
		cache: nil, // No shared cache.
	}

	svc.setLocalCache(rules)
	return svc
}

// newCachedRuleForTest precomputes lowercase lists and status membership for a
// usable fixture rule. It retains the original rule pointer, allowing matcher
// tests to exercise compiled conditions without invoking production
// publication cloning.
func newCachedRuleForTest(rule *ErrorPassthroughRule) *cachedPassthroughRule {
	cr := &cachedPassthroughRule{ErrorPassthroughRule: rule}
	if len(rule.Keywords) > 0 {
		cr.lowerKeywords = make([]string, len(rule.Keywords))
		for j, kw := range rule.Keywords {
			cr.lowerKeywords[j] = strings.ToLower(kw)
		}
	}
	if len(rule.Platforms) > 0 {
		cr.lowerPlatforms = make([]string, len(rule.Platforms))
		for j, p := range rule.Platforms {
			cr.lowerPlatforms[j] = strings.ToLower(p)
		}
	}
	if len(rule.ErrorCodes) > 0 {
		cr.errorCodeSet = make(map[int]struct{}, len(rule.ErrorCodes))
		for _, code := range rule.ErrorCodes {
			cr.errorCodeSet[code] = struct{}{}
		}
	}
	return cr
}

// TestRuleMatches_NoConditions compiles a rule with both condition lists empty
// and checks the low-level matcher. The rule must refuse a match despite
// any-mode and arbitrary supplied body text.
func TestRuleMatches_NoConditions(t *testing.T) {
	svc := newTestService(nil)
	rule := newCachedRuleForTest(&ErrorPassthroughRule{
		Enabled:    true,
		ErrorCodes: []int{},
		Keywords:   []string{},
		MatchMode:  MatchModeAny,
	})

	var bodyLower string
	var bodyLowerDone bool
	assert.False(t, svc.ruleMatchesOptimized(rule, 422, []byte("some error message"), &bodyLower, &bodyLowerDone),
		"没有配置条件时不应该匹配")
}

// TestRuleMatches_OnlyErrorCodes_AnyMode supplies matching and unrelated
// statuses with an empty keyword list. All four cases use the same body, and
// matching must follow status membership alone.
func TestRuleMatches_OnlyErrorCodes_AnyMode(t *testing.T) {
	svc := newTestService(nil)
	rule := newCachedRuleForTest(&ErrorPassthroughRule{
		Enabled:    true,
		ErrorCodes: []int{422, 400},
		Keywords:   []string{},
		MatchMode:  MatchModeAny,
	})

	tests := []struct {
		name       string // descriptive rule scenario label
		statusCode int    // supplied upstream status code
		body       string // supplied upstream error body text
		expected   bool   // expected rule matching result
	}{
		{"状态码匹配 422", 422, "any message", true},
		{"状态码匹配 400", 400, "any message", true},
		{"状态码不匹配 500", 500, "any message", false},
		{"状态码不匹配 429", 429, "any message", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodyLower string
			var bodyLowerDone bool
			result := svc.ruleMatchesOptimized(rule, tt.statusCode, []byte(tt.body), &bodyLower, &bodyLowerDone)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestRuleMatches_OnlyKeywords_AnyMode supplies two configured substrings with
// matching, missing, and mixed-case body text. The matcher must use keyword
// presence independently of the supplied status.
func TestRuleMatches_OnlyKeywords_AnyMode(t *testing.T) {
	svc := newTestService(nil)
	rule := newCachedRuleForTest(&ErrorPassthroughRule{
		Enabled:    true,
		ErrorCodes: []int{},
		Keywords:   []string{"context limit", "model not supported"},
		MatchMode:  MatchModeAny,
	})

	tests := []struct {
		name       string // descriptive rule scenario label
		statusCode int    // supplied upstream status code
		body       string // supplied upstream error body text
		expected   bool   // expected rule matching result
	}{
		{"关键词匹配 context limit", 500, "error: context limit reached", true},
		{"关键词匹配 model not supported", 400, "the model not supported here", true},
		{"关键词不匹配", 422, "some other error", false},
		{"关键词大小写 - 自动转换", 500, "Context Limit exceeded", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodyLower string
			var bodyLowerDone bool
			result := svc.ruleMatchesOptimized(rule, tt.statusCode, []byte(tt.body), &bodyLower, &bodyLowerDone)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestRuleMatches_BothConditions_AnyMode exercises both, either, and neither
// configured category matching. Any-mode must accept status or keyword success
// while refusing the case where both configured conditions fail.
func TestRuleMatches_BothConditions_AnyMode(t *testing.T) {
	svc := newTestService(nil)
	rule := newCachedRuleForTest(&ErrorPassthroughRule{
		Enabled:    true,
		ErrorCodes: []int{422, 400},
		Keywords:   []string{"context limit"},
		MatchMode:  MatchModeAny,
	})

	tests := []struct {
		name       string // descriptive rule scenario label
		statusCode int    // supplied upstream status code
		body       string // supplied upstream error body text
		expected   bool   // expected rule matching result
		reason     string // scenario-specific expected result explanation
	}{
		{
			name:       "状态码和关键词都匹配",
			statusCode: 422,
			body:       "context limit reached",
			expected:   true,
			reason:     "both match",
		},
		{
			name:       "只有状态码匹配",
			statusCode: 422,
			body:       "some other error",
			expected:   true,
			reason:     "code matches, keyword doesn't - OR mode should match",
		},
		{
			name:       "只有关键词匹配",
			statusCode: 500,
			body:       "context limit exceeded",
			expected:   true,
			reason:     "keyword matches, code doesn't - OR mode should match",
		},
		{
			name:       "都不匹配",
			statusCode: 500,
			body:       "some other error",
			expected:   false,
			reason:     "neither matches",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodyLower string
			var bodyLowerDone bool
			result := svc.ruleMatchesOptimized(rule, tt.statusCode, []byte(tt.body), &bodyLower, &bodyLowerDone)
			assert.Equal(t, tt.expected, result, tt.reason)
		})
	}
}

// TestRuleMatches_BothConditions_AllMode exercises status and keyword success
// in every combination. The all-mode rule must match only when both configured
// categories succeed together.
func TestRuleMatches_BothConditions_AllMode(t *testing.T) {
	svc := newTestService(nil)
	rule := newCachedRuleForTest(&ErrorPassthroughRule{
		Enabled:    true,
		ErrorCodes: []int{422, 400},
		Keywords:   []string{"context limit"},
		MatchMode:  MatchModeAll,
	})

	tests := []struct {
		name       string // descriptive rule scenario label
		statusCode int    // supplied upstream status code
		body       string // supplied upstream error body text
		expected   bool   // expected rule matching result
		reason     string // scenario-specific expected result explanation
	}{
		{
			name:       "状态码和关键词都匹配",
			statusCode: 422,
			body:       "context limit reached",
			expected:   true,
			reason:     "both match - AND mode should match",
		},
		{
			name:       "只有状态码匹配",
			statusCode: 422,
			body:       "some other error",
			expected:   false,
			reason:     "code matches but keyword doesn't - AND mode should NOT match",
		},
		{
			name:       "只有关键词匹配",
			statusCode: 500,
			body:       "context limit exceeded",
			expected:   false,
			reason:     "keyword matches but code doesn't - AND mode should NOT match",
		},
		{
			name:       "都不匹配",
			statusCode: 500,
			body:       "some other error",
			expected:   false,
			reason:     "neither matches",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodyLower string
			var bodyLowerDone bool
			result := svc.ruleMatchesOptimized(rule, tt.statusCode, []byte(tt.body), &bodyLower, &bodyLowerDone)
			assert.Equal(t, tt.expected, result, tt.reason)
		})
	}
}

// TestPlatformMatches covers absent filters, exact membership, mixed-case
// configuration, and an excluded platform. Compilation and the caller's case
// folding must produce the expected platform qualification result.
func TestPlatformMatches(t *testing.T) {
	svc := newTestService(nil)

	tests := []struct {
		name            string   // descriptive rule scenario label
		rulePlatforms   []string // configured rule platform match labels
		requestPlatform string   // supplied upstream platform match label
		expected        bool     // expected rule matching result
	}{
		{
			name:            "空平台列表匹配所有",
			rulePlatforms:   []string{},
			requestPlatform: "anthropic",
			expected:        true,
		},
		{
			name:            "nil平台列表匹配所有",
			rulePlatforms:   nil,
			requestPlatform: "openai",
			expected:        true,
		},
		{
			name:            "精确匹配 anthropic",
			rulePlatforms:   []string{"anthropic", "openai"},
			requestPlatform: "anthropic",
			expected:        true,
		},
		{
			name:            "精确匹配 openai",
			rulePlatforms:   []string{"anthropic", "openai"},
			requestPlatform: "openai",
			expected:        true,
		},
		{
			name:            "不匹配 gemini",
			rulePlatforms:   []string{"anthropic", "openai"},
			requestPlatform: "gemini",
			expected:        false,
		},
		{
			name:            "大小写不敏感",
			rulePlatforms:   []string{"Anthropic", "OpenAI"},
			requestPlatform: "anthropic",
			expected:        true,
		},
		{
			name:            "匹配 antigravity",
			rulePlatforms:   []string{"antigravity"},
			requestPlatform: "antigravity",
			expected:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := newCachedRuleForTest(&ErrorPassthroughRule{
				Platforms: tt.rulePlatforms,
			})
			result := svc.platformMatchesCached(rule, strings.ToLower(tt.requestPlatform))
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestMatchRule_Priority supplies two enabled rules that both match the same
// status with reversed priority values. The returned rule must have the lower
// numeric priority after publication sorting.
func TestMatchRule_Priority(t *testing.T) {
	rules := []*ErrorPassthroughRule{
		{
			ID:         1,
			Name:       "Low Priority",
			Enabled:    true,
			Priority:   10,
			ErrorCodes: []int{422},
			MatchMode:  MatchModeAny,
		},
		{
			ID:         2,
			Name:       "High Priority",
			Enabled:    true,
			Priority:   1,
			ErrorCodes: []int{422},
			MatchMode:  MatchModeAny,
		},
	}

	svc := newTestService(rules)
	matched := svc.MatchRule("anthropic", 422, []byte("error"))

	require.NotNil(t, matched)
	assert.Equal(t, int64(2), matched.ID, "应该匹配优先级更高(数值更小)的规则")
	assert.Equal(t, "High Priority", matched.Name)
}

// TestMatchRule_DisabledRule places a disabled rule before a matching enabled
// rule in priority order. Matching must skip the disabled entry and return the
// later enabled rule.
func TestMatchRule_DisabledRule(t *testing.T) {
	rules := []*ErrorPassthroughRule{
		{
			ID:         1,
			Name:       "Disabled Rule",
			Enabled:    false,
			Priority:   1,
			ErrorCodes: []int{422},
			MatchMode:  MatchModeAny,
		},
		{
			ID:         2,
			Name:       "Enabled Rule",
			Enabled:    true,
			Priority:   10,
			ErrorCodes: []int{422},
			MatchMode:  MatchModeAny,
		},
	}

	svc := newTestService(rules)
	matched := svc.MatchRule("anthropic", 422, []byte("error"))

	require.NotNil(t, matched)
	assert.Equal(t, int64(2), matched.ID, "应该跳过禁用的规则")
}

// TestMatchRule_PlatformFilter combines platform-specific status rules with a
// global rule. Requests must select their eligible platform entry or use the
// global match when neither platform-specific filter qualifies.
func TestMatchRule_PlatformFilter(t *testing.T) {
	rules := []*ErrorPassthroughRule{
		{
			ID:         1,
			Name:       "Anthropic Only",
			Enabled:    true,
			Priority:   1,
			ErrorCodes: []int{422},
			Platforms:  []string{"anthropic"},
			MatchMode:  MatchModeAny,
		},
		{
			ID:         2,
			Name:       "OpenAI Only",
			Enabled:    true,
			Priority:   2,
			ErrorCodes: []int{422},
			Platforms:  []string{"openai"},
			MatchMode:  MatchModeAny,
		},
		{
			ID:         3,
			Name:       "All Platforms",
			Enabled:    true,
			Priority:   3,
			ErrorCodes: []int{422},
			Platforms:  []string{},
			MatchMode:  MatchModeAny,
		},
	}

	svc := newTestService(rules)

	t.Run("Anthropic 请求匹配 Anthropic 规则", func(t *testing.T) {
		matched := svc.MatchRule("anthropic", 422, []byte("error"))
		require.NotNil(t, matched)
		assert.Equal(t, int64(1), matched.ID)
	})

	t.Run("OpenAI 请求匹配 OpenAI 规则", func(t *testing.T) {
		matched := svc.MatchRule("openai", 422, []byte("error"))
		require.NotNil(t, matched)
		assert.Equal(t, int64(2), matched.ID)
	})

	t.Run("Gemini 请求匹配全平台规则", func(t *testing.T) {
		matched := svc.MatchRule("gemini", 422, []byte("error"))
		require.NotNil(t, matched)
		assert.Equal(t, int64(3), matched.ID)
	})

	t.Run("Antigravity 请求匹配全平台规则", func(t *testing.T) {
		matched := svc.MatchRule("antigravity", 422, []byte("error"))
		require.NotNil(t, matched)
		assert.Equal(t, int64(3), matched.ID)
	})
}

// TestMatchRule_NoMatch supplies an enabled status-only rule and a different
// requested status. The public matcher must return nil when no configured
// condition qualifies for that response.
func TestMatchRule_NoMatch(t *testing.T) {
	rules := []*ErrorPassthroughRule{
		{
			ID:         1,
			Name:       "Rule for 422",
			Enabled:    true,
			Priority:   1,
			ErrorCodes: []int{422},
			MatchMode:  MatchModeAny,
		},
	}

	svc := newTestService(rules)
	matched := svc.MatchRule("anthropic", 500, []byte("error"))

	assert.Nil(t, matched, "不匹配任何规则时应返回 nil")
}

// TestMatchRule_EmptyRules publishes an allocated empty local snapshot before
// matching a status and body. Matching must return nil for this loaded
// configuration without inventing a default rule.
func TestMatchRule_EmptyRules(t *testing.T) {
	svc := newTestService([]*ErrorPassthroughRule{})
	matched := svc.MatchRule("anthropic", 422, []byte("error"))

	assert.Nil(t, matched, "没有规则时应返回 nil")
}

// TestMatchRule_CaseInsensitiveKeyword varies the body's case around a
// configured context-limit substring. All case variants must match while a
// body lacking that substring returns no rule.
func TestMatchRule_CaseInsensitiveKeyword(t *testing.T) {
	rules := []*ErrorPassthroughRule{
		{
			ID:        1,
			Name:      "Context Limit",
			Enabled:   true,
			Priority:  1,
			Keywords:  []string{"Context Limit"},
			MatchMode: MatchModeAny,
		},
	}

	svc := newTestService(rules)

	tests := []struct {
		name     string // descriptive rule scenario label
		body     string // supplied upstream error body text
		expected bool   // expected rule matching result
	}{
		{"完全匹配", "Context Limit reached", true},
		{"小写匹配", "context limit reached", true},
		{"大写匹配", "CONTEXT LIMIT REACHED", true},
		{"混合大小写", "ConTeXt LiMiT error", true},
		{"不匹配", "some other error", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched := svc.MatchRule("anthropic", 500, []byte(tt.body))
			if tt.expected {
				assert.NotNil(t, matched)
			} else {
				assert.Nil(t, matched)
			}
		})
	}
}

// TestMatchRule_RealWorldScenario_ContextLimitPassthrough requires status 422
// and context-limit text on two allowed platforms. Matches retain passthrough
// flags, while excluded platform, wrong status, or missing keyword rejects the
// rule.
func TestMatchRule_RealWorldScenario_ContextLimitPassthrough(t *testing.T) {
	rules := []*ErrorPassthroughRule{
		{
			ID:              1,
			Name:            "Context Limit Passthrough",
			Enabled:         true,
			Priority:        1,
			ErrorCodes:      []int{422},
			Keywords:        []string{"context limit"},
			MatchMode:       MatchModeAll, // Both categories required.
			Platforms:       []string{"anthropic", "antigravity"},
			PassthroughCode: true,
			PassthroughBody: true,
		},
	}

	svc := newTestService(rules)

	t.Run("Anthropic 422 with context limit", func(t *testing.T) {
		body := []byte(`{"type":"error","error":{"type":"invalid_request","message":"The context limit has been reached"}}`)
		matched := svc.MatchRule("anthropic", 422, body)
		require.NotNil(t, matched)
		assert.True(t, matched.PassthroughCode)
		assert.True(t, matched.PassthroughBody)
	})

	t.Run("Antigravity 422 with context limit", func(t *testing.T) {
		body := []byte(`{"error":"context limit exceeded"}`)
		matched := svc.MatchRule("antigravity", 422, body)
		require.NotNil(t, matched)
	})

	t.Run("OpenAI should not match", func(t *testing.T) {
		body := []byte(`{"error":"context limit exceeded"}`)
		matched := svc.MatchRule("openai", 422, body)
		assert.Nil(t, matched, "OpenAI 不在规则的平台列表中")
	})

	t.Run("Wrong status code", func(t *testing.T) {
		body := []byte(`{"error":"context limit exceeded"}`)
		matched := svc.MatchRule("anthropic", 400, body)
		assert.Nil(t, matched, "状态码不匹配")
	})

	t.Run("Wrong keyword", func(t *testing.T) {
		body := []byte(`{"error":"rate limit exceeded"}`)
		matched := svc.MatchRule("anthropic", 422, body)
		assert.Nil(t, matched, "关键词不匹配")
	})
}

// TestMatchRule_RealWorldScenario_CustomErrorMessage selects a configured rule
// for an upstream 500. Returned fields must carry the custom 503 status and
// message for later protocol presentation.
func TestMatchRule_RealWorldScenario_CustomErrorMessage(t *testing.T) {
	customMsg := "Service temporarily unavailable, please try again later"
	responseCode := 503
	rules := []*ErrorPassthroughRule{
		{
			ID:              1,
			Name:            "Hide Internal Errors",
			Enabled:         true,
			Priority:        1,
			ErrorCodes:      []int{500, 502, 503},
			MatchMode:       MatchModeAny,
			PassthroughCode: false,
			ResponseCode:    &responseCode,
			PassthroughBody: false,
			CustomMessage:   &customMsg,
		},
	}

	svc := newTestService(rules)

	matched := svc.MatchRule("anthropic", 500, []byte("internal server error"))
	require.NotNil(t, matched)
	assert.False(t, matched.PassthroughCode)
	assert.Equal(t, 503, *matched.ResponseCode)
	assert.False(t, matched.PassthroughBody)
	assert.Equal(t, customMsg, *matched.CustomMessage)
}

// TestErrorPassthroughRule_Validate supplies accepted passthrough/custom rules
// and each missing required value. Validation must return the expected
// field-specific error for invalid inputs and accept the complete
// configurations.
func TestErrorPassthroughRule_Validate(t *testing.T) {
	tests := []struct {
		name        string                // descriptive rule scenario label
		rule        *ErrorPassthroughRule // supplied validation rule fixture
		expectError bool                  // validation error is expected
		// errorField names the expected ValidationError field when expectError is
		// true. Valid cases leave this label unused and assert successful validation
		// instead.
		errorField string // expected rejected validation field name
	}{
		{
			name: "有效规则 - 透传模式（含错误码）",
			rule: &ErrorPassthroughRule{
				Name:            "Valid Rule",
				MatchMode:       MatchModeAny,
				ErrorCodes:      []int{422},
				PassthroughCode: true,
				PassthroughBody: true,
			},
			expectError: false,
		},
		{
			name: "有效规则 - 透传模式（含关键词）",
			rule: &ErrorPassthroughRule{
				Name:            "Valid Rule",
				MatchMode:       MatchModeAny,
				Keywords:        []string{"context limit"},
				PassthroughCode: true,
				PassthroughBody: true,
			},
			expectError: false,
		},
		{
			name: "有效规则 - 自定义响应",
			rule: &ErrorPassthroughRule{
				Name:            "Valid Rule",
				MatchMode:       MatchModeAll,
				ErrorCodes:      []int{500},
				Keywords:        []string{"internal error"},
				PassthroughCode: false,
				ResponseCode:    testIntPtr(503),
				PassthroughBody: false,
				CustomMessage:   testStrPtr("Custom error"),
			},
			expectError: false,
		},
		{
			name: "缺少名称",
			rule: &ErrorPassthroughRule{
				Name:            "",
				MatchMode:       MatchModeAny,
				ErrorCodes:      []int{422},
				PassthroughCode: true,
				PassthroughBody: true,
			},
			expectError: true,
			errorField:  "name",
		},
		{
			name: "无效的匹配模式",
			rule: &ErrorPassthroughRule{
				Name:            "Invalid Mode",
				MatchMode:       "invalid",
				ErrorCodes:      []int{422},
				PassthroughCode: true,
				PassthroughBody: true,
			},
			expectError: true,
			errorField:  "match_mode",
		},
		{
			name: "缺少匹配条件（错误码和关键词都为空）",
			rule: &ErrorPassthroughRule{
				Name:            "No Conditions",
				MatchMode:       MatchModeAny,
				ErrorCodes:      []int{},
				Keywords:        []string{},
				PassthroughCode: true,
				PassthroughBody: true,
			},
			expectError: true,
			errorField:  "conditions",
		},
		{
			name: "缺少匹配条件（nil切片）",
			rule: &ErrorPassthroughRule{
				Name:            "Nil Conditions",
				MatchMode:       MatchModeAny,
				ErrorCodes:      nil,
				Keywords:        nil,
				PassthroughCode: true,
				PassthroughBody: true,
			},
			expectError: true,
			errorField:  "conditions",
		},
		{
			name: "自定义状态码但未提供值",
			rule: &ErrorPassthroughRule{
				Name:            "Missing Code",
				MatchMode:       MatchModeAny,
				ErrorCodes:      []int{422},
				PassthroughCode: false,
				ResponseCode:    nil,
				PassthroughBody: true,
			},
			expectError: true,
			errorField:  "response_code",
		},
		{
			name: "自定义消息但未提供值",
			rule: &ErrorPassthroughRule{
				Name:            "Missing Message",
				MatchMode:       MatchModeAny,
				ErrorCodes:      []int{422},
				PassthroughCode: true,
				PassthroughBody: false,
				CustomMessage:   nil,
			},
			expectError: true,
			errorField:  "custom_message",
		},
		{
			name: "自定义消息为空字符串",
			rule: &ErrorPassthroughRule{
				Name:            "Empty Message",
				MatchMode:       MatchModeAny,
				ErrorCodes:      []int{422},
				PassthroughCode: true,
				PassthroughBody: false,
				CustomMessage:   testStrPtr(""),
			},
			expectError: true,
			errorField:  "custom_message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rule.Validate()
			if tt.expectError {
				require.Error(t, err)
				validationErr, ok := err.(*ValidationError)
				require.True(t, ok, "应该返回 ValidationError")
				assert.Equal(t, tt.errorField, validationErr.Field)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestCreate_ForceRefreshCacheAfterWrite starts from stale local and shared
// data then persists another rule. Matching must use the new authoritative
// rule, with invalidation, set, and notification counts confirming refresh
// bypasses cache Get.
func TestCreate_ForceRefreshCacheAfterWrite(t *testing.T) {
	ctx := context.Background()

	staleRule := newPassthroughRuleForWritePathTest(99, "service temporarily unavailable after multiple", "旧缓存消息")
	repo := &mockErrorPassthroughRepo{rules: []*ErrorPassthroughRule{}}
	cache := newMockErrorPassthroughCache([]*ErrorPassthroughRule{staleRule}, true)

	svc := &ErrorPassthroughService{repo: repo, cache: cache}
	svc.setLocalCache([]*ErrorPassthroughRule{staleRule})

	newRule := newPassthroughRuleForWritePathTest(0, "service temporarily unavailable after multiple", "上游请求失败")
	created, err := svc.Create(ctx, newRule)
	require.NoError(t, err)
	require.NotNil(t, created)

	body := []byte(`{"message":"Service temporarily unavailable after multiple retries, please try again later"}`)
	matched := svc.MatchRule("anthropic", 503, body)
	require.NotNil(t, matched)
	assert.Equal(t, created.ID, matched.ID)
	if assert.NotNil(t, matched.CustomMessage) {
		assert.Equal(t, "上游请求失败", *matched.CustomMessage)
	}

	assert.Equal(t, 0, cache.getCalled, "写路径刷新不应依赖 cache.Get")
	assert.Equal(t, 1, cache.invalidateCalled)
	assert.Equal(t, 1, cache.setCalled)
	assert.Equal(t, 1, cache.notifyCalled)
}

// TestUpdate_ForceRefreshCacheAfterWrite replaces a stored rule's keyword and
// response message while both caches contain its earlier value. The old
// keyword must stop matching and the new value must become visible after
// refresh.
func TestUpdate_ForceRefreshCacheAfterWrite(t *testing.T) {
	ctx := context.Background()

	originalRule := newPassthroughRuleForWritePathTest(1, "old keyword", "旧消息")
	repo := &mockErrorPassthroughRepo{rules: []*ErrorPassthroughRule{originalRule}}
	cache := newMockErrorPassthroughCache([]*ErrorPassthroughRule{originalRule}, true)

	svc := &ErrorPassthroughService{repo: repo, cache: cache}
	svc.setLocalCache([]*ErrorPassthroughRule{originalRule})

	updatedRule := newPassthroughRuleForWritePathTest(1, "new keyword", "新消息")
	_, err := svc.Update(ctx, updatedRule)
	require.NoError(t, err)

	oldBody := []byte(`{"message":"old keyword"}`)
	oldMatched := svc.MatchRule("anthropic", 503, oldBody)
	assert.Nil(t, oldMatched, "更新后旧关键词不应继续命中")

	newBody := []byte(`{"message":"new keyword"}`)
	newMatched := svc.MatchRule("anthropic", 503, newBody)
	require.NotNil(t, newMatched)
	if assert.NotNil(t, newMatched.CustomMessage) {
		assert.Equal(t, "新消息", *newMatched.CustomMessage)
	}

	assert.Equal(t, 0, cache.getCalled, "写路径刷新不应依赖 cache.Get")
	assert.Equal(t, 1, cache.invalidateCalled)
	assert.Equal(t, 1, cache.setCalled)
	assert.Equal(t, 1, cache.notifyCalled)
}

// TestDelete_ForceRefreshCacheAfterWrite removes a rule present in local and
// shared fixtures. Matching must return nil afterward, and cache counters must
// show invalidation, authoritative set, and update notification.
func TestDelete_ForceRefreshCacheAfterWrite(t *testing.T) {
	ctx := context.Background()

	rule := newPassthroughRuleForWritePathTest(1, "to be deleted", "删除前消息")
	repo := &mockErrorPassthroughRepo{rules: []*ErrorPassthroughRule{rule}}
	cache := newMockErrorPassthroughCache([]*ErrorPassthroughRule{rule}, true)

	svc := &ErrorPassthroughService{repo: repo, cache: cache}
	svc.setLocalCache([]*ErrorPassthroughRule{rule})

	err := svc.Delete(ctx, 1)
	require.NoError(t, err)

	body := []byte(`{"message":"to be deleted"}`)
	matched := svc.MatchRule("anthropic", 503, body)
	assert.Nil(t, matched, "删除后规则不应再命中")

	assert.Equal(t, 0, cache.getCalled, "写路径刷新不应依赖 cache.Get")
	assert.Equal(t, 1, cache.invalidateCalled)
	assert.Equal(t, 1, cache.setCalled)
	assert.Equal(t, 1, cache.notifyCalled)
}

// TestNewService_StartupReloadFromDBToHealStaleCache gives startup a newer
// repository rule and an older shared cache. Matching must use repository data
// and overwrite shared storage without consulting cached Get.
func TestNewService_StartupReloadFromDBToHealStaleCache(t *testing.T) {
	staleRule := newPassthroughRuleForWritePathTest(99, "stale keyword", "旧缓存消息")
	latestRule := newPassthroughRuleForWritePathTest(1, "fresh keyword", "最新消息")

	repo := &mockErrorPassthroughRepo{rules: []*ErrorPassthroughRule{latestRule}}
	cache := newMockErrorPassthroughCache([]*ErrorPassthroughRule{staleRule}, true)

	svc := NewErrorPassthroughService(repo, cache)
	require.NoError(t, svc.StartContext(context.Background()))
	t.Cleanup(svc.Stop)

	matchedFresh := svc.MatchRule("anthropic", 503, []byte(`{"message":"fresh keyword"}`))
	require.NotNil(t, matchedFresh)
	assert.Equal(t, int64(1), matchedFresh.ID)

	matchedStale := svc.MatchRule("anthropic", 503, []byte(`{"message":"stale keyword"}`))
	assert.Nil(t, matchedStale, "启动后应以 DB 最新规则覆盖旧缓存")

	assert.Equal(t, 0, cache.getCalled, "启动强制 DB 刷新不应依赖 cache.Get")
	assert.Equal(t, 1, cache.setCalled, "启动后应回写缓存，覆盖陈旧缓存")
}

// TestUpdate_RefreshFailureShouldNotKeepStaleEnabledRule disables a rule while
// later authoritative reads fail. The successful update must clear local
// matching state and stop matching the former enabled value after fixture
// invalidation.
func TestUpdate_RefreshFailureShouldNotKeepStaleEnabledRule(t *testing.T) {
	ctx := context.Background()

	staleRule := newPassthroughRuleForWritePathTest(1, "service temporarily unavailable after multiple", "旧缓存消息")
	repo := &mockErrorPassthroughRepo{
		rules:   []*ErrorPassthroughRule{staleRule},
		listErr: errors.New("db list failed"),
	}
	cache := newMockErrorPassthroughCache([]*ErrorPassthroughRule{staleRule}, true)

	svc := &ErrorPassthroughService{repo: repo, cache: cache}
	svc.setLocalCache([]*ErrorPassthroughRule{staleRule})

	disabledRule := *staleRule
	disabledRule.Enabled = false
	_, err := svc.Update(ctx, &disabledRule)
	require.NoError(t, err)

	body := []byte(`{"message":"Service temporarily unavailable after multiple retries, please try again later"}`)
	matched := svc.MatchRule("anthropic", 503, body)
	assert.Nil(t, matched, "刷新失败时不应继续命中旧的启用规则")

	svc.localCacheMu.RLock()
	assert.Nil(t, svc.localCache, "刷新失败后应清空本地缓存，避免误命中")
	svc.localCacheMu.RUnlock()
}

// newPassthroughRuleForWritePathTest creates an enabled 503 all-mode rule
// using the supplied keyword and custom message. Its response status is also
// 503, and mutable pointer fields receive per-fixture storage.
func newPassthroughRuleForWritePathTest(id int64, keyword, customMsg string) *ErrorPassthroughRule {
	responseCode := 503
	rule := &ErrorPassthroughRule{
		ID:              id,
		Name:            "write-path-cache-refresh",
		Enabled:         true,
		Priority:        1,
		ErrorCodes:      []int{503},
		Keywords:        []string{keyword},
		MatchMode:       MatchModeAll,
		PassthroughCode: false,
		ResponseCode:    &responseCode,
		PassthroughBody: false,
		CustomMessage:   &customMsg,
	}
	return rule
}

// testIntPtr returns a pointer to the supplied integer fixture value.
func testIntPtr(i int) *int { return &i }

// testStrPtr returns a pointer to the supplied string fixture value.
func testStrPtr(s string) *string { return &s }

// List captures the current rule and consumes its one-shot blocking flag under
// the fixture mutex. Callers supply a usable context and let the test
// coordinate the delayed read through entered and resume.
//
// The snapshot is copied before blocking and the mutex is released so Update
// can replace current while this read waits. A resumed read returns its
// captured earlier value, while cancellation winning the wait returns the
// context error. Only the consumed blocking read closes entered, preventing
// another read from closing that same signal twice.
func (r *controlledRules) List(ctx context.Context) ([]*ErrorPassthroughRule, error) {
	r.mu.Lock()
	snapshot := cloneRule(r.current)
	block := r.block
	r.block = false
	r.mu.Unlock()
	if block {
		// Signal that the earlier snapshot has been captured before waiting outside
		// the mutex; the test can now schedule a write while this fixture read
		// remains suspended.
		close(r.entered)
		select {
		case <-r.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []*ErrorPassthroughRule{snapshot}, nil
}

// Update replaces current under the fixture mutex using a production rule
// clone and returns another clone. The context is ignored; callers can safely
// compare captured earlier reads with this replacement value.
func (r *controlledRules) Update(_ context.Context, rule *ErrorPassthroughRule) (*ErrorPassthroughRule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = cloneRule(rule)
	return cloneRule(rule), nil
}

// fixtureRule returns an enabled OpenAI 503 fixture with a custom 502 response
// and original message. Keywords is allocated empty, allowing ownership tests
// to check that nil and empty collections stay distinct.
func fixtureRule() *ErrorPassthroughRule {
	message := "original"
	code := 502
	return &ErrorPassthroughRule{ID: 1, Name: "fixture", Enabled: true, MatchMode: MatchModeAny, ErrorCodes: []int{503}, ResponseCode: &code, CustomMessage: &message, Keywords: []string{}, Platforms: []string{"openai"}}
}

// blockedRules creates the canonical rule with fresh entry and resume channels
// for one delayed List. Tests wait for entered before scheduling an update,
// then close resume or cancel the read to finish it.
func blockedRules() *controlledRules {
	return &controlledRules{current: fixtureRule(), entered: make(chan struct{}), resume: make(chan struct{}), block: true}
}

// TestRuleUpdateCannotBeOverwrittenByOlderLoad suspends a captured earlier
// read while scheduling a disabling write. After both complete, matching and
// another authoritative reload must retain the disabled state.
func TestRuleUpdateCannotBeOverwrittenByOlderLoad(t *testing.T) {
	repo := blockedRules()
	svc := NewErrorPassthroughService(repo, nil)
	defer svc.Stop()
	loaded := make(chan error, 1)
	// Suspend an earlier authoritative snapshot while it holds the service update
	// token, then coordinate the competing write or cancellation before letting
	// that captured load publish.
	go func() { loaded <- svc.reloadRulesFromDB(context.Background()) }()
	<-repo.entered
	changed := fixtureRule()
	changed.Enabled = false
	updated := make(chan error, 1)
	go func() { _, err := svc.Update(context.Background(), changed); updated <- err }()
	close(repo.resume)
	require.NoError(t, <-loaded)
	require.NoError(t, <-updated)
	require.Nil(t, svc.MatchRule("openai", 503, nil))
	require.NoError(t, svc.reloadRulesFromDB(context.Background()))
	require.Nil(t, svc.MatchRule("openai", 503, nil))
}

// TestRuleUpdateCoordinatorWaitHonorsCancellation holds the update token
// through a suspended load and times out a disabling write. The rejected
// writer must report DeadlineExceeded while the initially enabled rule remains
// matchable after the suspended load completes.
func TestRuleUpdateCoordinatorWaitHonorsCancellation(t *testing.T) {
	repo := blockedRules()
	svc := NewErrorPassthroughService(repo, nil)
	defer svc.Stop()
	loaded := make(chan error, 1)
	// Suspend an earlier authoritative snapshot while it holds the service update
	// token, then coordinate the competing write or cancellation before letting
	// that captured load publish.
	go func() { loaded <- svc.reloadRulesFromDB(context.Background()) }()
	<-repo.entered
	// Bound the writer's wait while the earlier load retains update ownership.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	changed := fixtureRule()
	changed.Enabled = false
	_, err := svc.Update(ctx, changed)
	close(repo.resume)
	require.NoError(t, <-loaded)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotNil(t, svc.MatchRule("openai", 503, nil), "未成功写入不能发布禁用状态")
}

// TestRuleSnapshotOwnership mutates input pointers and slices after
// publication, then changes fields in a returned match. Later matches must
// retain their published values and preserve an allocated empty keyword list.
func TestRuleSnapshotOwnership(t *testing.T) {
	source := fixtureRule()
	svc := NewErrorPassthroughService(nil, nil)
	defer svc.Stop()
	svc.setLocalCache([]*ErrorPassthroughRule{source})
	*source.CustomMessage = "input mutation"
	source.Platforms[0] = "other"
	source.ErrorCodes[0] = 400
	first := svc.MatchRule("openai", 503, nil)
	require.NotNil(t, first)
	require.Equal(t, "original", *first.CustomMessage)
	first.Enabled = false
	*first.CustomMessage = "output mutation"
	*first.ResponseCode = 418
	first.Keywords = append(first.Keywords, "changed")
	second := svc.MatchRule("openai", 503, nil)
	require.NotNil(t, second)
	require.Equal(t, "original", *second.CustomMessage)
	require.Equal(t, 502, *second.ResponseCode)
	require.NotNil(t, second.Keywords)
	require.Empty(t, second.Keywords)
}

// TestStopCancelsStartupRuleLoad suspends startup in its repository call and
// stops the service concurrently. Shutdown must cancel that load, complete
// cleanup, and make repeated start or stop calls harmless.
func TestStopCancelsStartupRuleLoad(t *testing.T) {
	repo := blockedRules()
	svc := NewErrorPassthroughService(repo, nil)
	started := make(chan error, 1)
	// Run startup separately so StopContext can cancel its already entered
	// repository load; the fixture's entry channel establishes that shutdown is
	// interrupting active startup work.
	go func() { started <- svc.StartContext(context.Background()) }()
	<-repo.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, svc.StopContext(ctx))
	require.ErrorIs(t, <-started, context.Canceled)
	require.NoError(t, svc.StopContext(ctx))
	require.NoError(t, svc.StartContext(ctx))
}

// Get reports no cached fixture data so refresh must read its repository.
func (c *callbackRuleCache) Get(context.Context) ([]*ErrorPassthroughRule, bool) { return nil, false }

// Set accepts publication while leaving storage observations to the controlled
// repository.
func (c *callbackRuleCache) Set(context.Context, []*ErrorPassthroughRule) error { return nil }

// SubscribeUpdates retains the supplied callback for later test-controlled
// invocation. It creates no task itself and ignores the context; the test owns
// the callback goroutine and its completion channel.
func (c *callbackRuleCache) SubscribeUpdates(_ context.Context, callback func()) {
	c.callback = callback
}

// StopSubscription waits for done when the test has installed an active
// callback completion channel. Nil done returns immediately; the test closes
// that channel after its launched callback returns.
func (c *callbackRuleCache) StopSubscription() {
	if c.done != nil {
		// Wait for the test-owned callback to return before subscriber cleanup
		// completes, so service shutdown includes its active repository refresh
		// rather than only cancellation notification.
		<-c.done
	}
}

// TestStopCancelsSubscriptionRuleLoad launches a notification callback that
// blocks in repository loading. StopContext must cancel that active refresh
// and wait for its subscriber fixture completion within the supplied budget.
func TestStopCancelsSubscriptionRuleLoad(t *testing.T) {
	repo := blockedRules()
	repo.block = false
	cache := &callbackRuleCache{}
	svc := NewErrorPassthroughService(repo, cache)
	require.NoError(t, svc.StartContext(context.Background()))
	repo.mu.Lock()
	repo.block = true
	repo.mu.Unlock()
	cache.done = make(chan struct{})
	// Close the fixture's subscriber completion only after its callback returns,
	// letting service stopping prove that cancellation has also finished the
	// coordinated repository refresh.
	go func() { cache.callback(); close(cache.done) }()
	<-repo.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, svc.StopContext(ctx))
}
