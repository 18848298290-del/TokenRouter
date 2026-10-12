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

// mockErrorPassthroughRepo supplies configurable rule-storage results.
type mockErrorPassthroughRepo struct {
	rules     []*ErrorPassthroughRule
	listErr   error
	getErr    error
	createErr error
	updateErr error
	deleteErr error
}

type mockErrorPassthroughCache struct {
	rules            []*ErrorPassthroughRule
	hasData          bool
	getCalled        int
	setCalled        int
	invalidateCalled int
	notifyCalled     int
}

// controlledRules uses channels to coordinate reads and updates.
type controlledRules struct {
	ErrorPassthroughRepository
	mu      sync.Mutex
	current *ErrorPassthroughRule
	entered chan struct{}
	resume  chan struct{}
	block   bool
}

type callbackRuleCache struct {
	ErrorPassthroughCache
	callback func()
	done     chan struct{}
}

func newMockErrorPassthroughCache(rules []*ErrorPassthroughRule, hasData bool) *mockErrorPassthroughCache {
	return &mockErrorPassthroughCache{
		rules:   cloneFixtureRules(rules),
		hasData: hasData,
	}
}

func (m *mockErrorPassthroughCache) Get(ctx context.Context) ([]*ErrorPassthroughRule, bool) {
	m.getCalled++
	if !m.hasData {
		return nil, false
	}
	return cloneFixtureRules(m.rules), true
}

func (m *mockErrorPassthroughCache) Set(ctx context.Context, rules []*ErrorPassthroughRule) error {
	m.setCalled++
	m.rules = cloneFixtureRules(rules)
	m.hasData = true
	return nil
}

func (m *mockErrorPassthroughCache) Invalidate(ctx context.Context) error {
	m.invalidateCalled++
	m.rules = nil
	m.hasData = false
	return nil
}

func (m *mockErrorPassthroughCache) NotifyUpdate(ctx context.Context) error {
	m.notifyCalled++
	return nil
}

func (m *mockErrorPassthroughCache) SubscribeUpdates(ctx context.Context, handler func()) {
	// This fixture does not deliver subscription notifications.
}

func cloneFixtureRules(rules []*ErrorPassthroughRule) []*ErrorPassthroughRule {
	if rules == nil {
		return nil
	}
	out := make([]*ErrorPassthroughRule, len(rules))
	copy(out, rules)
	return out
}

func (m *mockErrorPassthroughRepo) List(ctx context.Context) ([]*ErrorPassthroughRule, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.rules, nil
}

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

func (m *mockErrorPassthroughRepo) Create(ctx context.Context, rule *ErrorPassthroughRule) (*ErrorPassthroughRule, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	rule.ID = int64(len(m.rules) + 1)
	m.rules = append(m.rules, rule)
	return rule, nil
}

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

// newTestService constructs a service with a preloaded local snapshot.
func newTestService(rules []*ErrorPassthroughRule) *ErrorPassthroughService {
	repo := &mockErrorPassthroughRepo{rules: rules}
	svc := &ErrorPassthroughService{
		repo:  repo,
		cache: nil, // No shared cache.
	}

	svc.setLocalCache(rules)
	return svc
}

// newCachedRuleForTest compiles a rule for matcher tests.
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

func TestRuleMatches_NoConditions(t *testing.T) {
	// An empty condition set must not match.
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

func TestRuleMatches_OnlyErrorCodes_AnyMode(t *testing.T) {
	svc := newTestService(nil)
	rule := newCachedRuleForTest(&ErrorPassthroughRule{
		Enabled:    true,
		ErrorCodes: []int{422, 400},
		Keywords:   []string{},
		MatchMode:  MatchModeAny,
	})

	tests := []struct {
		name       string
		statusCode int
		body       string
		expected   bool
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

func TestRuleMatches_OnlyKeywords_AnyMode(t *testing.T) {
	svc := newTestService(nil)
	rule := newCachedRuleForTest(&ErrorPassthroughRule{
		Enabled:    true,
		ErrorCodes: []int{},
		Keywords:   []string{"context limit", "model not supported"},
		MatchMode:  MatchModeAny,
	})

	tests := []struct {
		name       string
		statusCode int
		body       string
		expected   bool
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

func TestRuleMatches_BothConditions_AnyMode(t *testing.T) {
	// Any mode accepts either a status-code or keyword match.
	svc := newTestService(nil)
	rule := newCachedRuleForTest(&ErrorPassthroughRule{
		Enabled:    true,
		ErrorCodes: []int{422, 400},
		Keywords:   []string{"context limit"},
		MatchMode:  MatchModeAny,
	})

	tests := []struct {
		name       string
		statusCode int
		body       string
		expected   bool
		reason     string
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

func TestRuleMatches_BothConditions_AllMode(t *testing.T) {
	// All mode requires both configured categories.
	svc := newTestService(nil)
	rule := newCachedRuleForTest(&ErrorPassthroughRule{
		Enabled:    true,
		ErrorCodes: []int{422, 400},
		Keywords:   []string{"context limit"},
		MatchMode:  MatchModeAll,
	})

	tests := []struct {
		name       string
		statusCode int
		body       string
		expected   bool
		reason     string
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

func TestPlatformMatches(t *testing.T) {
	svc := newTestService(nil)

	tests := []struct {
		name            string
		rulePlatforms   []string
		requestPlatform string
		expected        bool
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

func TestMatchRule_Priority(t *testing.T) {
	// Rules with lower priority numbers match first.
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

func TestMatchRule_EmptyRules(t *testing.T) {
	svc := newTestService([]*ErrorPassthroughRule{})
	matched := svc.MatchRule("anthropic", 422, []byte("error"))

	assert.Nil(t, matched, "没有规则时应返回 nil")
}

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
		name     string
		body     string
		expected bool
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

func TestMatchRule_RealWorldScenario_ContextLimitPassthrough(t *testing.T) {
	// Pass through an upstream 422 reporting "context limit has been reached".
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

	// OpenAI is outside the platform filter.
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

func TestMatchRule_RealWorldScenario_CustomErrorMessage(t *testing.T) {
	// A custom response hides upstream details.
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

func TestErrorPassthroughRule_Validate(t *testing.T) {
	tests := []struct {
		name        string
		rule        *ErrorPassthroughRule
		expectError bool
		errorField  string
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

func testIntPtr(i int) *int { return &i }

func testStrPtr(s string) *string { return &s }

func (r *controlledRules) List(ctx context.Context) ([]*ErrorPassthroughRule, error) {
	r.mu.Lock()
	snapshot := cloneRule(r.current)
	block := r.block
	r.block = false
	r.mu.Unlock()
	if block {
		close(r.entered)
		select {
		case <-r.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []*ErrorPassthroughRule{snapshot}, nil
}

func (r *controlledRules) Update(_ context.Context, rule *ErrorPassthroughRule) (*ErrorPassthroughRule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = cloneRule(rule)
	return cloneRule(rule), nil
}

func fixtureRule() *ErrorPassthroughRule {
	message := "original"
	code := 502
	return &ErrorPassthroughRule{ID: 1, Name: "fixture", Enabled: true, MatchMode: MatchModeAny, ErrorCodes: []int{503}, ResponseCode: &code, CustomMessage: &message, Keywords: []string{}, Platforms: []string{"openai"}}
}

func blockedRules() *controlledRules {
	return &controlledRules{current: fixtureRule(), entered: make(chan struct{}), resume: make(chan struct{}), block: true}
}

// TestRuleUpdateCannotBeOverwrittenByOlderLoad checks ordered publication of reads and administrative writes.
func TestRuleUpdateCannotBeOverwrittenByOlderLoad(t *testing.T) {
	repo := blockedRules()
	svc := NewErrorPassthroughService(repo, nil)
	defer svc.Stop()
	loaded := make(chan error, 1)
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
	// A later reload also reads the updated authoritative state.
	require.NoError(t, svc.reloadRulesFromDB(context.Background()))
	require.Nil(t, svc.MatchRule("openai", 503, nil))
}

func TestRuleUpdateCoordinatorWaitHonorsCancellation(t *testing.T) {
	repo := blockedRules()
	svc := NewErrorPassthroughService(repo, nil)
	defer svc.Stop()
	loaded := make(chan error, 1)
	go func() { loaded <- svc.reloadRulesFromDB(context.Background()) }()
	<-repo.entered
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

// TestRuleSnapshotOwnership checks that inputs and returned nested fields cannot mutate compiled rules.
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

// TestStopCancelsStartupRuleLoad checks that stopping cancels the in-progress startup repository call.
func TestStopCancelsStartupRuleLoad(t *testing.T) {
	repo := blockedRules()
	svc := NewErrorPassthroughService(repo, nil)
	started := make(chan error, 1)
	go func() { started <- svc.StartContext(context.Background()) }()
	<-repo.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, svc.StopContext(ctx))
	require.ErrorIs(t, <-started, context.Canceled)
	require.NoError(t, svc.StopContext(ctx))
	require.NoError(t, svc.StartContext(ctx))
}

func (c *callbackRuleCache) Get(context.Context) ([]*ErrorPassthroughRule, bool) { return nil, false }

func (c *callbackRuleCache) Set(context.Context, []*ErrorPassthroughRule) error { return nil }

func (c *callbackRuleCache) SubscribeUpdates(_ context.Context, callback func()) {
	c.callback = callback
}

func (c *callbackRuleCache) StopSubscription() {
	if c.done != nil {
		<-c.done
	}
}

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
	go func() { cache.callback(); close(cache.done) }()
	<-repo.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, svc.StopContext(ctx))
}
