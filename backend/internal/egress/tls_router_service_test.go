package egress

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// tlsFingerprintRouterRepoStub keeps mutable router pointers for serial service
// matching and normalization tests.
type tlsFingerprintRouterRepoStub struct {
	// routers retains pointers supplied by the test or later writes. Fixture
	// methods return these pointers directly, so tests serialize access and can
	// observe normalization mutations.
	routers []*TLSFingerprintRouter // shared stored router pointers
}

// List returns the fixture's retained router pointer slice directly. Tests
// serialize reads and writes because the fixture performs no cloning,
// filtering, context checks, or synchronization for its stored records.
func (r *tlsFingerprintRouterRepoStub) List(context.Context) ([]*TLSFingerprintRouter, error) {
	return r.routers, nil
}

// GetByID returns the first retained nonnil router whose ID matches, or nil
// when absent. Returned pointers remain shared with the serial fixture,
// allowing callers to observe direct mutations.
func (r *tlsFingerprintRouterRepoStub) GetByID(_ context.Context, id int64) (*TLSFingerprintRouter, error) {
	for _, router := range r.routers {
		if router != nil && router.ID == id {
			return router, nil
		}
	}
	return nil, nil
}

// Create assigns a sequential fixture ID, appends the supplied router pointer,
// and returns it. The fixture retains the mutable input directly, and tests
// serialize later access to that pointer.
func (r *tlsFingerprintRouterRepoStub) Create(_ context.Context, router *TLSFingerprintRouter) (*TLSFingerprintRouter, error) {
	router.ID = int64(len(r.routers) + 1)
	r.routers = append(r.routers, router)
	return router, nil
}

// Update replaces the first retained router with a matching ID or appends the
// input when no match exists. It returns and retains the supplied pointer,
// leaving synchronization and mutation order to serial tests.
func (r *tlsFingerprintRouterRepoStub) Update(_ context.Context, router *TLSFingerprintRouter) (*TLSFingerprintRouter, error) {
	for i, existing := range r.routers {
		if existing != nil && existing.ID == router.ID {
			r.routers[i] = router
			return router, nil
		}
	}
	r.routers = append(r.routers, router)
	return router, nil
}

// Delete removes the first retained router matching id and otherwise succeeds
// without a change. It edits the fixture slice in place, so tests serialize
// this operation with other fixture access.
func (r *tlsFingerprintRouterRepoStub) Delete(_ context.Context, id int64) error {
	for i, router := range r.routers {
		if router != nil && router.ID == id {
			r.routers = append(r.routers[:i], r.routers[i+1:]...)
			return nil
		}
	}
	return nil
}

// newTLSFingerprintRouterTestService constructs a service using the supplied
// shared router pointers and no distributed cache. Runtime cache warming occurs
// later when matching misses its initially empty local map.
func newTLSFingerprintRouterTestService(routers ...*TLSFingerprintRouter) *TLSFingerprintRouterService {
	return NewTLSFingerprintRouterService(&tlsFingerprintRouterRepoStub{routers: routers}, nil)
}

// TestTLSFingerprintRouterService_MatchUserAgent configures ordered disabled,
// exact, contains, prefix, and regex rules. It checks matched profile IDs,
// selected header values, disabled-rule skipping, case handling, and retained router
// metadata for an unmatched User-Agent.
func TestTLSFingerprintRouterService_MatchUserAgent(t *testing.T) {
	svc := newTLSFingerprintRouterTestService(&TLSFingerprintRouter{
		ID:      1,
		Name:    "客户端路由",
		Enabled: true,
		Rules: []TLSFingerprintRouterRule{
			{
				Name:                    "禁用规则不会命中",
				Enabled:                 false,
				MatchType:               TLSRouterMatchContains,
				Pattern:                 "opencode",
				TLSFingerprintProfileID: 99,
			},
			{
				Name:                    "Codex 精确匹配",
				Enabled:                 true,
				MatchType:               TLSRouterMatchExact,
				Pattern:                 "codex_cli_rs/1.0",
				TLSFingerprintProfileID: 1,
				UpstreamUserAgent:       "codex_cli_rs/9.9",
				UpstreamOriginator:      "codex_cli_rs",
			},
			{
				Name:                    "OpenCode 大小写不敏感",
				Enabled:                 true,
				MatchType:               TLSRouterMatchContains,
				Pattern:                 "OpenCode",
				TLSFingerprintProfileID: 2,
			},
			{
				Name:                    "前缀匹配",
				Enabled:                 true,
				MatchType:               TLSRouterMatchPrefix,
				Pattern:                 "cursor/",
				TLSFingerprintProfileID: 3,
			},
			{
				Name:                    "正则匹配",
				Enabled:                 true,
				MatchType:               TLSRouterMatchRegex,
				Pattern:                 `myapp/\d+`,
				TLSFingerprintProfileID: 4,
			},
		},
	})

	tests := []struct {
		name      string // matching scenario display name
		ua        string // incoming User-Agent test value
		wantMatch bool   // expected rule match flag
		wantRule  string // expected matched rule name
		wantID    int64  // expected selected profile identity
	}{
		{name: "按顺序跳过禁用规则后命中 exact", ua: "codex_cli_rs/1.0", wantMatch: true, wantRule: "Codex 精确匹配", wantID: 1},
		{name: "contains 默认大小写不敏感", ua: "opencode/0.9", wantMatch: true, wantRule: "OpenCode 大小写不敏感", wantID: 2},
		{name: "prefix 命中", ua: "cursor/2.0", wantMatch: true, wantRule: "前缀匹配", wantID: 3},
		{name: "regex 命中", ua: "MyApp/123", wantMatch: true, wantRule: "正则匹配", wantID: 4},
		{name: "未命中返回 router 信息", ua: "curl/8.0", wantMatch: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := svc.MatchUserAgent(1, tt.ua)
			require.Equal(t, int64(1), result.RouterID)
			require.Equal(t, "客户端路由", result.RouterName)
			require.Equal(t, tt.wantMatch, result.Matched)
			if tt.wantMatch {
				require.Equal(t, tt.wantRule, result.RuleName)
				require.Equal(t, tt.wantID, result.TLSFingerprintProfileID)
				if tt.wantID == 1 {
					require.Equal(t, "codex_cli_rs/9.9", result.UpstreamUserAgent)
					require.Equal(t, "codex_cli_rs", result.UpstreamOriginator)
				}
			}
		})
	}
}

// TestTLSFingerprintRouterService_MatchUserAgent_CaseSensitive uses a
// case-sensitive contains rule with mixed-case pattern text. It checks that the
// lowercase User-Agent misses and the matching capitalization selects the rule.
func TestTLSFingerprintRouterService_MatchUserAgent_CaseSensitive(t *testing.T) {
	svc := newTLSFingerprintRouterTestService(&TLSFingerprintRouter{
		ID:      2,
		Name:    "大小写路由",
		Enabled: true,
		Rules: []TLSFingerprintRouterRule{
			{
				Name:                    "大小写敏感",
				Enabled:                 true,
				MatchType:               TLSRouterMatchContains,
				Pattern:                 "OpenCode",
				CaseSensitive:           true,
				TLSFingerprintProfileID: 7,
			},
		},
	})

	require.False(t, svc.MatchUserAgent(2, "opencode/1.0").Matched)
	require.True(t, svc.MatchUserAgent(2, "OpenCode/1.0").Matched)
}

// TestTLSFingerprintRouterService_Create_NormalizesChatGPTOAuthTokenConfig
// creates a router with padded names, token User-Agent text, random profile
// selection, and nil rules. It checks the persisted normalized strings,
// retained profile sentinel, and empty rule collection.
func TestTLSFingerprintRouterService_Create_NormalizesChatGPTOAuthTokenConfig(t *testing.T) {
	profileID := int64(-1)
	repo := &tlsFingerprintRouterRepoStub{}
	svc := NewTLSFingerprintRouterService(repo, nil)
	svc.Start()

	created, err := svc.Create(context.Background(), &TLSFingerprintRouter{
		Name:                                     "  token router  ",
		Enabled:                                  true,
		ChatGPTOAuthTokenUserAgent:               "  codex-token-ua  ",
		ChatGPTOAuthTokenTLSFingerprintProfileID: &profileID,
		Rules:                                    nil,
	})

	require.NoError(t, err)
	require.Equal(t, "token router", created.Name)
	require.Equal(t, "codex-token-ua", created.ChatGPTOAuthTokenUserAgent)
	require.Equal(t, int64(-1), *created.ChatGPTOAuthTokenTLSFingerprintProfileID)
	require.Empty(t, created.Rules)
}

// TestTLSRouterSnapshotIsolation mutates a returned token-profile pointer and
// then its source pointer after local publication. It checks later runtime
// router copies retain the sampled cached profile ID.
func TestTLSRouterSnapshotIsolation(t *testing.T) {
	id := int64(1)
	source := &TLSFingerprintRouter{ID: 1, Name: "router", Enabled: true, ChatGPTOAuthTokenTLSFingerprintProfileID: &id}
	svc := NewTLSFingerprintRouterService(nil, nil)
	svc.setLocalCache([]*TLSFingerprintRouter{source})
	first := svc.GetRuntimeRouter(1)
	*first.ChatGPTOAuthTokenTLSFingerprintProfileID = 2
	require.Equal(t, int64(1), *svc.GetRuntimeRouter(1).ChatGPTOAuthTokenTLSFingerprintProfileID)
	id = 3
	require.Equal(t, int64(1), *svc.GetRuntimeRouter(1).ChatGPTOAuthTokenTLSFingerprintProfileID)
}
