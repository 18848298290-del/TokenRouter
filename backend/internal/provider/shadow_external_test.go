package provider_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// sparkShadowGroupRepoStub supplies active groups for shadow group-selection tests.
// Unexpected methods on the embedded nil repository panic.
type sparkShadowGroupRepoStub struct {
	routing.GroupRepository
	groups []routing.Group
}

// bindFailRepoStub fails group binding to check compensating deletion of the new shadow.
type bindFailRepoStub struct {
	*sparkShadowRepoStub
}

// raceCreateRepoStub simulates a competing shadow creation that wins the unique index.
// The subsequent reread finds that shadow, allowing CreateShadow to return a structured 409.
type raceCreateRepoStub struct {
	*sparkShadowRepoStub
}

func (s *sparkShadowGroupRepoStub) ListActive(_ context.Context) ([]routing.Group, error) {
	return s.groups, nil
}

// TestCreateShadowDoesNotBindDefaultGroup checks that an ungrouped parent produces an ungrouped shadow.
func TestCreateShadowDoesNotBindDefaultGroup(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	groupRepo := &sparkShadowGroupRepoStub{
		groups: []routing.Group{
			{ID: 99, Name: capability.PlatformOpenAI + "-default"},
			{ID: 7, Name: "some-other-group"},
		},
	}
	svc := newProviderEditorForTest(repo, shadowGroupsFixture{groupRepo})

	parent := &providercore.Record{
		Name: "grp-parent", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billing.StatusActive, Credentials: map[string]any{"chatgpt_account_id": "org-g"},
	}
	require.NoError(t, repo.Create(ctx, parent))

	shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "grp-shadow"})
	require.NoError(t, err)
	require.Empty(t, repo.groupsOf[shadow.ID], "未指定分组且母提供商无分组时保留未分组状态")
}

// TestCreateShadow_InheritsParentGroups checks that omitted group_ids inherit
// the parent's existing groups, including custom routing groups.
func TestCreateShadow_InheritsParentGroups(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()

	groupRepo := &sparkShadowGroupRepoStub{groups: []routing.Group{{ID: 99, Name: capability.PlatformOpenAI + "-default"}}}
	svc := newProviderEditorForTest(repo, shadowGroupsFixture{groupRepo})

	parent := &providercore.Record{
		Name: "grp-parent", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billing.StatusActive, GroupIDs: []int64{11, 22},
		Credentials: map[string]any{"chatgpt_account_id": "org-grp"},
	}
	require.NoError(t, repo.Create(ctx, parent))

	shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "grp-shadow"})
	require.NoError(t, err)
	require.Equal(t, []int64{11, 22}, repo.groupsOf[shadow.ID], "未指定分组应继承母提供商分组,而非 openai-default")
}

func (s *bindFailRepoStub) BindGroups(_ context.Context, _ int64, _ []int64) error {
	return errors.New("simulated bind failure")
}

// TestCreateShadow_InvalidGroupRejectedNoOrphan checks that invalid groups are rejected
// before a shadow is created.
func TestCreateShadow_InvalidGroupRejectedNoOrphan(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	groupRepo := &sparkShadowValidatingGroupRepoStub{existing: map[int64]bool{7: true}}
	svc := newProviderEditorForTest(repo, shadowGroupsFixture{groupRepo})
	parent := &providercore.Record{
		Name: "p", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billing.StatusActive, Credentials: map[string]any{"chatgpt_account_id": "o"},
	}
	require.NoError(t, repo.Create(ctx, parent))

	_, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "s", GroupIDs: []int64{999}})
	require.Error(t, err, "无效分组应在创建前被拒")

	shadows, qerr := repo.ListShadowsByParent(ctx, parent.ID)
	require.NoError(t, qerr)
	require.Empty(t, shadows, "无效分组应在创建前被拒,不应建出影子")
}

// TestCreateShadow_BindFailureRollsBackShadow checks that failed binding deletes
// the newly created shadow, releasing the one-shadow-per-parent constraint for a retry.
func TestCreateShadow_BindFailureRollsBackShadow(t *testing.T) {
	ctx := context.Background()
	base := newSparkShadowRepoStub()
	repo := &bindFailRepoStub{sparkShadowRepoStub: base}
	groupRepo := &sparkShadowValidatingGroupRepoStub{existing: map[int64]bool{7: true}}
	svc := newProviderEditorForTest(repo, shadowGroupsFixture{groupRepo})
	parent := &providercore.Record{
		Name: "p", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billing.StatusActive, Credentials: map[string]any{"chatgpt_account_id": "o"},
	}
	require.NoError(t, base.Create(ctx, parent))

	_, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "s", GroupIDs: []int64{7}})
	require.Error(t, err, "绑组失败应返回错误")

	shadows, qerr := base.ListShadowsByParent(ctx, parent.ID)
	require.NoError(t, qerr)
	require.Empty(t, shadows, "绑组失败后应补偿删除影子,不留孤儿")
}

// TestCreateShadow checks parent identity, Spark quota dimension, and proxy inheritance.
//
// The shadow receives default model mappings and holds no authentication tokens.
// Creating another shadow for the same parent returns an error.
func TestCreateShadow(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)

	proxyID := int64(7)
	parent := &providercore.Record{
		Name:     "p",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
		ProxyID:  &proxyID,
		Credentials: map[string]any{
			"refresh_token":      "RT",
			"chatgpt_account_id": "org-x",
		},
	}
	require.NoError(t, repo.Create(ctx, parent))

	shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "p-spark", Priority: 50})
	require.NoError(t, err)
	require.NotNil(t, shadow)
	require.Equal(t, parent.ID, *shadow.ParentProviderID)
	require.Equal(t, providercore.QuotaDimensionSpark, shadow.QuotaDimension)
	require.Equal(t, provideradapter.DefaultSparkShadowModels(), shadow.Credentials["model_mapping"],
		"影子默认带 spark 恒等变体映射")
	require.Nil(t, shadow.Credentials["refresh_token"], "影子不得持有 auth token")
	require.Nil(t, shadow.Credentials["access_token"], "影子不得持有 auth token")
	require.Equal(t, parent.ProxyID, shadow.ProxyID)
	require.NotContains(t, shadow.Extra, "openai_long_context_billing_enabled")

	_, err = svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "dup"})
	require.Error(t, err)
}

// TestCreateShadow_BindGroups checks that supplied groups are bound after creation.
// The shadow must then be returned by ListSchedulableByGroupID.
func TestCreateShadow_BindGroups(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)

	parent := &providercore.Record{
		Name:     "parent",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"chatgpt_account_id": "org-y",
		},
	}
	require.NoError(t, repo.Create(ctx, parent))

	const testGroupID = int64(42)
	shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{
		Name:     "p-spark",
		GroupIDs: []int64{testGroupID},
	})
	require.NoError(t, err)
	require.NotNil(t, shadow)
	require.Equal(t, []int64{testGroupID}, shadow.GroupIDs, "CreateShadow should backfill GroupIDs into the returned shadow")

	providers, err := repo.ListSchedulableByGroupID(ctx, testGroupID)
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, shadow.ID, providers[0].ID)
}

// TestCreateShadow_InheritsParentConcurrency checks that omitted concurrency inherits
// the parent limit; zero would mean unlimited concurrency to the limiter.
func TestCreateShadow_InheritsParentConcurrency(t *testing.T) {
	ctx := context.Background()

	t.Run("unspecified_inherits_parent", func(t *testing.T) {
		repo := newSparkShadowRepoStub()
		svc := newProviderEditorForTest(repo)
		parent := &providercore.Record{
			Name: "conc-parent", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
			Status: billing.StatusActive, Concurrency: 3,
			Credentials: map[string]any{"chatgpt_account_id": "org-c"},
		}
		require.NoError(t, repo.Create(ctx, parent))

		shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "conc-shadow"})
		require.NoError(t, err)
		require.Equal(t, 3, shadow.Concurrency, "未指定并发应继承母提供商(非 0=无限)")
		require.Equal(t, 3, repo.providers[shadow.ID].Concurrency)
	})

	t.Run("explicit_positive_kept", func(t *testing.T) {
		repo := newSparkShadowRepoStub()
		svc := newProviderEditorForTest(repo)
		parent := &providercore.Record{
			Name: "conc-parent2", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
			Status: billing.StatusActive, Concurrency: 3,
			Credentials: map[string]any{"chatgpt_account_id": "org-c2"},
		}
		require.NoError(t, repo.Create(ctx, parent))

		shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "conc-shadow2", Concurrency: 2})
		require.NoError(t, err)
		require.Equal(t, 2, shadow.Concurrency, "显式正并发应保留")
	})
}

// TestCreateShadow_InheritsParentPriorityWhenOmitted checks priority inheritance for omitted values.
// Lower priorities schedule first; a name-only request uses the parent value instead of zero.
func TestCreateShadow_InheritsParentPriorityWhenOmitted(t *testing.T) {
	ctx := context.Background()

	t.Run("unspecified_inherits_parent", func(t *testing.T) {
		repo := newSparkShadowRepoStub()
		svc := newProviderEditorForTest(repo)
		parent := &providercore.Record{
			Name: "prio-parent", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
			Status: billing.StatusActive, Priority: 30,
			Credentials: map[string]any{"chatgpt_account_id": "org-p"},
		}
		require.NoError(t, repo.Create(ctx, parent))

		shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "prio-shadow"})
		require.NoError(t, err)
		require.Equal(t, 30, shadow.Priority, "未指定优先级应继承母提供商(而非 0=最高优先级)")
		require.Equal(t, 30, repo.providers[shadow.ID].Priority)
	})

	t.Run("explicit_positive_kept", func(t *testing.T) {
		repo := newSparkShadowRepoStub()
		svc := newProviderEditorForTest(repo)
		parent := &providercore.Record{
			Name: "prio-parent2", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
			Status: billing.StatusActive, Priority: 30,
			Credentials: map[string]any{"chatgpt_account_id": "org-p2"},
		}
		require.NoError(t, repo.Create(ctx, parent))

		shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "prio-shadow2", Priority: 7})
		require.NoError(t, err)
		require.Equal(t, 7, shadow.Priority, "显式正优先级应保留")
	})
}

// TestResolveCredentialProvider_RejectsParentShadow checks that credential resolution rejects a shadow parent.
func TestResolveCredentialProvider_RejectsParentShadow(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()

	grandparent := &providercore.Record{
		Name: "gp", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "RT"},
	}
	require.NoError(t, repo.Create(ctx, grandparent))

	parentShadow := &providercore.Record{
		Name: "parent-shadow", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive,
		Credentials: map[string]any{}, ParentProviderID: &grandparent.ID, QuotaDimension: providercore.QuotaDimensionSpark,
	}
	require.NoError(t, repo.Create(ctx, parentShadow))
	child := &providercore.Record{
		Name: "child-shadow", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive,
		Credentials: map[string]any{}, ParentProviderID: &parentShadow.ID, QuotaDimension: providercore.QuotaDimensionSpark,
	}
	require.NoError(t, repo.Create(ctx, child))

	_, err := providercore.ResolveCredentialRecord(ctx, repo.GetByID, child)
	require.Error(t, err, "父提供商本身是影子时凭据解析应拒绝(fail-closed)")
}

// TestCreateShadow_RejectsShadowAsParent checks that shadows cannot become parents of further shadows.
func TestCreateShadow_RejectsShadowAsParent(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)

	parent := &providercore.Record{
		Name: "real-parent", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billing.StatusActive, Credentials: map[string]any{"chatgpt_account_id": "org-x"},
	}
	require.NoError(t, repo.Create(ctx, parent))
	firstShadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "first-shadow"})
	require.NoError(t, err)

	_, err = svc.CreateShadow(ctx, firstShadow.ID, providercore.ShadowOptions{Name: "second-shadow"})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err), "影子当母应返回 400")
}

// TestCreateShadow_StructuredErrors checks structured 4xx responses for invalid parents and duplicates.
func TestCreateShadow_StructuredErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("non_oauth_parent_400", func(t *testing.T) {
		repo := newSparkShadowRepoStub()
		svc := newProviderEditorForTest(repo)
		parent := &providercore.Record{Name: "apikey-parent", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive}
		require.NoError(t, repo.Create(ctx, parent))
		_, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "s"})
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err), "非 OAuth 母提供商应 400")
	})

	t.Run("duplicate_409", func(t *testing.T) {
		repo := newSparkShadowRepoStub()
		svc := newProviderEditorForTest(repo)
		parent := &providercore.Record{Name: "p", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Credentials: map[string]any{"chatgpt_account_id": "o"}}
		require.NoError(t, repo.Create(ctx, parent))
		_, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "s1"})
		require.NoError(t, err)
		_, err = svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "s2"})
		require.Error(t, err)
		require.Equal(t, http.StatusConflict, httpx.ErrorCode(err), "重复创建应 409")
	})
}

func (s *raceCreateRepoStub) Create(ctx context.Context, provider *providercore.Record) error {
	if provider.ParentProviderID != nil {
		s.nextID++
		phantom := *provider
		phantom.ID = s.nextID
		s.providers[phantom.ID] = &phantom
		return errors.New(`duplicate key value violates unique constraint "uq_providers_spark_shadow_per_parent"`)
	}
	return s.sparkShadowRepoStub.Create(ctx, provider)
}

// TestCreateShadow_DefaultsNameFromParent checks that a blank name succeeds
// with the default "<parent name> (Spark)".
func TestCreateShadow_DefaultsNameFromParent(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)
	parent := &providercore.Record{
		Name: "mum", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billing.StatusActive, Credentials: map[string]any{"chatgpt_account_id": "o"},
	}
	require.NoError(t, repo.Create(ctx, parent))

	shadow, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "   "})
	require.NoError(t, err, "空/空白 name 不应 500,应默认命名")
	require.Equal(t, "mum (Spark)", shadow.Name)
}

// TestCreateShadow_ConcurrentCreateReturns409 checks a race after the precheck:
// a unique-index conflict with an existing shadow returns a structured 409.
func TestCreateShadow_ConcurrentCreateReturns409(t *testing.T) {
	ctx := context.Background()
	base := newSparkShadowRepoStub()
	repo := &raceCreateRepoStub{sparkShadowRepoStub: base}
	svc := newProviderEditorForTest(repo)
	parent := &providercore.Record{
		Name: "p", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
		Status: billing.StatusActive, Credentials: map[string]any{"chatgpt_account_id": "o"},
	}
	require.NoError(t, base.Create(ctx, parent))

	_, err := svc.CreateShadow(ctx, parent.ID, providercore.ShadowOptions{Name: "s"})
	require.Error(t, err)
	require.Equal(t, http.StatusConflict, httpx.ErrorCode(err), "并发竞态撞唯一索引应映射 409 而非 500")
}
