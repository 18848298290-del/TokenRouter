package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// openAIFastPolicyRepoStub stores JSON policy values for direct save-and-read tests.
type openAIFastPolicyRepoStub struct {
	values map[string]string // stored policy setting values
}

// TestSetOpenAIFastPolicySettings_Validation supplies invalid actions, tiers,
// and user IDs before saving valid rules. Rejected configurations return
// errors, and accepted rules round-trip their order, actions, tier names, and
// user IDs.
func TestSetOpenAIFastPolicySettings_Validation(t *testing.T) {
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	svc := NewRuntimeSettings(repo, settingscore.ErrSettingNotFound, nil)

	err := svc.SetOpenAIFastPolicySettings(context.Background(), &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier: tierpolicy.OpenAIFastTierPriority,
			Action:      "bogus",
			Scope:       anthropic.BetaPolicyScopeAll,
		}},
	})
	require.Error(t, err)

	err = svc.SetOpenAIFastPolicySettings(context.Background(), &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier: "turbo",
			Action:      anthropic.BetaPolicyActionPass,
			Scope:       anthropic.BetaPolicyScopeAll,
		}},
	})
	require.Error(t, err)

	err = svc.SetOpenAIFastPolicySettings(context.Background(), &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier: tierpolicy.OpenAIFastTierPriority,
			Action:      anthropic.BetaPolicyActionPass,
			Scope:       anthropic.BetaPolicyScopeAll,
			UserIDs:     []int64{0},
		}},
	})
	require.Error(t, err)

	err = svc.SetOpenAIFastPolicySettings(context.Background(), &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier: tierpolicy.OpenAIFastTierPriority,
			Action:      anthropic.BetaPolicyActionPass,
			Scope:       anthropic.BetaPolicyScopeAll,
			UserIDs:     []int64{42, 42},
		}},
	})
	require.Error(t, err)

	err = svc.SetOpenAIFastPolicySettings(context.Background(), &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{
			{
				ServiceTier: tierpolicy.OpenAIFastTierPriority,
				Action:      tierpolicy.OpenAIFastPolicyActionForcePriority,
				Scope:       anthropic.BetaPolicyScopeAll,
				UserIDs:     []int64{42, 43},
			},
			{
				ServiceTier: tierpolicy.OpenAIFastTierUltrafast,
				Action:      anthropic.BetaPolicyActionPass,
				Scope:       anthropic.BetaPolicyScopeAll,
			},
		},
	})
	require.NoError(t, err)

	got, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Len(t, got.Rules, 2)
	require.Equal(t, tierpolicy.OpenAIFastTierPriority, got.Rules[0].ServiceTier)
	require.Equal(t, tierpolicy.OpenAIFastPolicyActionForcePriority, got.Rules[0].Action)
	require.Equal(t, []int64{42, 43}, got.Rules[0].UserIDs)
	require.Equal(t, tierpolicy.OpenAIFastTierUltrafast, got.Rules[1].ServiceTier)
	require.Equal(t, anthropic.BetaPolicyActionPass, got.Rules[1].Action)
}

// TestGlobalForceUltrafastPersists saves a force-ultrafast rule with a model
// pattern and matching fallback action. Reading the stored value returns an
// equal settings object, protecting persistence of that complete configuration.
func TestGlobalForceUltrafastPersists(t *testing.T) {
	svc := NewRuntimeSettings(&openAIFastPolicyRepoStub{}, settingscore.ErrSettingNotFound, nil)
	settings := &tierpolicy.OpenAIFastPolicySettings{Rules: []tierpolicy.OpenAIFastPolicyRule{{ServiceTier: "all", Scope: "all", Action: "force_ultrafast", ModelWhitelist: []string{"gpt-*"}, FallbackAction: "force_ultrafast"}}}
	require.NoError(t, svc.SetOpenAIFastPolicySettings(context.Background(), settings))
	got, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, settings, got)
}

// Get rejects setting-record reads in this scalar Fast-policy fixture. The
// tests use GetValue for decoded policy reads, so invocation panics to expose
// an unexpected record-object dependency.
func (s *openAIFastPolicyRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

// GetValue returns the configured scalar policy value for a key. Missing
// entries return settings.ErrSettingNotFound so the runtime policy reader can
// distinguish its defaults from a supplied value.
func (s *openAIFastPolicyRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", settingscore.ErrSettingNotFound
}

// Set writes the prepared Fast-policy JSON into the fixture map. A nil map is
// allocated before assignment, letting persistence tests start from an
// otherwise empty fixture and then read the saved value.
func (s *openAIFastPolicyRepoStub) Set(ctx context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

// GetMultiple returns requested fixture values in a newly allocated map.
// Missing keys are omitted for gateway readers to interpret, and the caller can
// modify the returned map independently of fixture storage.
func (s *openAIFastPolicyRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

// SetMultiple rejects bulk persistence in the direct Fast-policy fixture. These
// tests exercise the validated single-key Set path, and a call here panics to
// expose accidental use of coordinated bulk updates.
func (s *openAIFastPolicyRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

// GetAll rejects inventory reads in the direct Fast-policy fixture. Policy
// round trips read one named JSON value, and the panic exposes an unexpected
// dependency on loading every setting.
func (s *openAIFastPolicyRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

// Delete rejects setting removal in the Fast-policy fixture. Validation and
// persistence tests save or read the policy value, so deleting its key panics
// to expose an unexpected operation.
func (s *openAIFastPolicyRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}
