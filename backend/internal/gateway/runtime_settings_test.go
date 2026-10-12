package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

type openAIFastPolicyRepoStub struct {
	values map[string]string
}

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

func TestGlobalForceUltrafastPersists(t *testing.T) {
	svc := NewRuntimeSettings(&openAIFastPolicyRepoStub{}, settingscore.ErrSettingNotFound, nil)
	settings := &tierpolicy.OpenAIFastPolicySettings{Rules: []tierpolicy.OpenAIFastPolicyRule{{ServiceTier: "all", Scope: "all", Action: "force_ultrafast", ModelWhitelist: []string{"gpt-*"}, FallbackAction: "force_ultrafast"}}}
	require.NoError(t, svc.SetOpenAIFastPolicySettings(context.Background(), settings))
	got, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, settings, got)
}

func (s *openAIFastPolicyRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

func (s *openAIFastPolicyRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", settingscore.ErrSettingNotFound
}

func (s *openAIFastPolicyRepoStub) Set(ctx context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

// GetMultiple returns fixture values for requested keys; the settings reader supplies missing defaults.
func (s *openAIFastPolicyRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (s *openAIFastPolicyRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *openAIFastPolicyRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *openAIFastPolicyRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}
