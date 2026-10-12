package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type createSnapshotStore struct {
	AdminStore
	privacyWrites int
}

// deprecatedCreateStore records the provider value supplied to creation.
type deprecatedCreateStore struct {
	AdminStore
	createdProvider *Record
}

func (s *createSnapshotStore) Create(_ context.Context, value *Record) error {
	value.ID = 1
	return nil
}

func (s *createSnapshotStore) UpdatePrivacyModeIfUnchanged(context.Context, UsageObservationVersion, string) (bool, error) {
	s.privacyWrites++
	return true, nil
}

// TestCreatePrivacyTaskHasIndependentReturnSnapshot checks that post-create privacy work cannot mutate the returned provider.
func TestCreatePrivacyTaskHasIndependentReturnSnapshot(t *testing.T) {
	store := &createSnapshotStore{}
	privacy := NewPrivacyService(store, nil, PrivacyOptions{Antigravity: func(context.Context, string, string, string) string { return AntigravityPrivacySet }})
	var task func()
	admin := NewAdmin(store, AdminOptions{
		Creation:    CreationOptions{Now: time.Now, LoadLocation: time.LoadLocation, NewSeed: func() string { return "00000000-0000-4000-8000-000000000001" }},
		Credentials: CreateCredentialHooks{Validate: func(context.Context, *Record) error { return nil }},
		Privacy:     privacy, Background: func(_ string, fn func()) bool { task = fn; return true }, Error: func(string, ...any) {},
	})
	result, err := admin.CreateProvider(context.Background(), &CreateProviderInput{Name: "snapshot", Platform: PlatformAntigravity, Type: ProviderTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}})
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Empty(t, result.Extra)
	task()
	require.Equal(t, 1, store.privacyWrites)
	require.Empty(t, result.Extra, "后置任务不能补写返回快照的 map")
}

func TestAdminServiceCreateProviderDiscardsDeprecatedLongContextBillingExtra(t *testing.T) {
	repo := &deprecatedCreateStore{}
	svc := NewAdmin(repo, AdminOptions{Creation: CreationOptions{Now: time.Now, LoadLocation: time.LoadLocation}, Credentials: CreateCredentialHooks{Validate: func(context.Context, *Record) error { return nil }}})

	provider, err := svc.CreateProvider(context.Background(), &CreateProviderInput{
		Name:        "openai-provider",
		Platform:    PlatformOpenAI,
		Type:        ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "test"},
		Extra:       map[string]any{"openai_long_context_billing_enabled": "malformed", "preserved": true},
	})

	require.NoError(t, err)
	require.Same(t, provider, repo.createdProvider)
	require.NotContains(t, provider.Extra, "openai_long_context_billing_enabled")
	require.Equal(t, true, provider.Extra["preserved"])
}

func (s *deprecatedCreateStore) Create(_ context.Context, value *Record) error {
	value.ID = 1
	s.createdProvider = value
	return nil
}
