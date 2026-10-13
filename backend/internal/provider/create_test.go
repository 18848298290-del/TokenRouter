package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// createSnapshotStore records conditional privacy writes for postcreation
// snapshot tests.
type createSnapshotStore struct {
	AdminStore        // unused administrative store methods
	privacyWrites int // conditional privacy write count
}

// deprecatedCreateStore retains the provider value submitted to creation for
// assertions.
type deprecatedCreateStore struct {
	AdminStore // unused administrative store methods
	// createdProvider retains the exact pointer supplied to Create. The test
	// compares that address and its normalized Extra with the creation result.
	createdProvider *Record
}

// Create assigns provider ID one to the supplied record and succeeds. The
// fixture stores no row, and the captured privacy task uses that assigned
// identity in its conditional write.
func (s *createSnapshotStore) Create(_ context.Context, value *Record) error {
	value.ID = 1
	return nil
}

// UpdatePrivacyModeIfUnchanged counts one conditional privacy write and reports
// its condition matched. It records the attempt rather than persisting Extra,
// allowing the test to inspect mutation of the separate background record.
func (s *createSnapshotStore) UpdatePrivacyModeIfUnchanged(context.Context, UsageObservationVersion, string) (bool, error) {
	s.privacyWrites++
	return true, nil
}

// TestCreatePrivacyTaskHasIndependentReturnSnapshot captures postcreation
// privacy work before running it. The task performs its conditional privacy
// write while the returned provider's Extra remains empty and separate from
// that background mutation.
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

// TestAdminServiceCreateProviderDiscardsDeprecatedLongContextBillingExtra
// creates a provider with an obsolete long-context billing key. Storage
// receives the returned provider value, the retired key is removed, and
// unrelated Extra remains available.
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

// Create assigns a provider ID and retains the supplied record pointer. This
// fixture exposes the value submitted to storage so the test can compare it
// with the returned provider.
func (s *deprecatedCreateStore) Create(_ context.Context, value *Record) error {
	value.ID = 1
	s.createdProvider = value
	return nil
}
