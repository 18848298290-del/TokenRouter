package apikey_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/apikey/testkit"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/team"
)

type teamContextErrorRepository struct {
	team.TeamRepository
	err error
}

func TestAPIKeyService_SnapshotRoundTrip_PreservesGroupCaptureControls(t *testing.T) {
	svc := testkit.NewService(nil, nil, nil, nil, nil, nil, nil)
	svc.Start()
	groupID := int64(9)
	stickyWeighted := false
	lbTopK := 3
	apiKey := &apikey.APIKey{
		ID:      1,
		UserID:  2,
		GroupID: &groupID,
		Key:     "k-images-roundtrip",
		Status:  billing.StatusActive,
		User: &identity.User{
			ID:          2,
			Status:      billing.StatusActive,
			Role:        identity.RoleUser,
			Balance:     10,
			Concurrency: 3,
		},
		Group: &routing.Group{
			ID:            groupID,
			Name:          "openai-images",
			SchedulerType: routing.GroupSchedulerTypeAdvanced,
			AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
				StickyWeightedEnabled: &stickyWeighted,
				LBTopK:                &lbTopK,
			},
			Status:                  billing.StatusActive,
			RateMultiplier:          1,
			SessionIsolationEnabled: true,
			AllowImageGeneration:    true,
		},
	}

	snapshot := svc.KeySnapshotFromAPIKey(context.Background(), apiKey)
	roundTrip := svc.KeySnapshotToAPIKey(apiKey.Key, snapshot)

	require.NotNil(t, roundTrip)
	require.NotNil(t, roundTrip.Group)
	require.Equal(t, routing.GroupSchedulerTypeAdvanced, roundTrip.Group.SchedulerType)
	require.NotNil(t, roundTrip.Group.AdvancedSchedulerOverrides.StickyWeightedEnabled)
	require.False(t, *roundTrip.Group.AdvancedSchedulerOverrides.StickyWeightedEnabled)
	require.Equal(t, 3, *roundTrip.Group.AdvancedSchedulerOverrides.LBTopK)
	require.NotSame(t, apiKey.Group.AdvancedSchedulerOverrides.LBTopK, roundTrip.Group.AdvancedSchedulerOverrides.LBTopK)
	require.True(t, roundTrip.Group.SessionIsolationEnabled)
	require.True(t, roundTrip.Group.AllowImageGeneration)
}

// TestAPIKeyAuthSnapshotGroupForceOpenAIFastRoundtrip checks that serialization retains the group Fast flag.
func TestAPIKeyAuthSnapshotGroupForceOpenAIFastRoundtrip(t *testing.T) {
	groupID := int64(50)
	apiKey := &apikey.APIKey{
		ID: 82, UserID: 40, GroupID: &groupID, Key: "sk-fast-roundtrip", Status: billing.StatusActive,
		User: &identity.User{ID: 40, Status: billing.StatusActive},
		Group: &routing.Group{
			ID: groupID, Name: "fast-roundtrip", Status: billing.StatusActive,
			Hydrated: true, ForceOpenAIFast: true,
		},
	}
	svc := newAPIKeyTestService(apiKeyTestDependencies{})

	payload, err := json.Marshal(&apikey.APIKeyAuthCacheEntry{Snapshot: svc.KeySnapshotFromAPIKey(context.Background(), apiKey)})
	require.NoError(t, err)
	var cached apikey.APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))

	materialized, used, err := svc.KeyApplyAuthCacheEntry(apiKey.Key, &cached)
	require.NoError(t, err)
	require.True(t, used)
	require.NotNil(t, materialized.Group)
	require.True(t, materialized.Group.Hydrated)
	require.True(t, materialized.Group.ForceOpenAIFast)
	require.Equal(t, apikey.KeyApiKeyAuthSnapshotVersion, cached.Snapshot.Version)
}

// TestAuthSnapshotGroupOpenAIFastPolicy checks that serialization retains Ultra Fast and disabled policies.
func TestAuthSnapshotGroupOpenAIFastPolicy(t *testing.T) {
	for _, policy := range []string{"force_ultrafast", "force_off"} {
		key := &apikey.APIKey{ID: 1, UserID: 2, Status: billing.StatusActive, User: &identity.User{ID: 2, Status: billing.StatusActive}, Group: &routing.Group{ID: 3, Status: billing.StatusActive, Hydrated: true, OpenAIFastPolicy: policy}}
		svc := newAPIKeyTestService(apiKeyTestDependencies{})
		payload, err := json.Marshal(&apikey.APIKeyAuthCacheEntry{Snapshot: svc.KeySnapshotFromAPIKey(context.Background(), key)})
		require.NoError(t, err)
		var entry apikey.APIKeyAuthCacheEntry
		require.NoError(t, json.Unmarshal(payload, &entry))
		result, used, err := svc.KeyApplyAuthCacheEntry("sk-test", &entry)
		require.NoError(t, err)
		require.True(t, used)
		require.Equal(t, policy, result.Group.OpenAIFastPolicy)
	}
}

func TestAPIKeyAuthSnapshotGroupFastPolicyByKeyType(t *testing.T) {
	for _, composite := range []bool{false, true} {
		name := "ordinary"
		if composite {
			name = "composite"
		}
		t.Run(name, func(t *testing.T) {
			group := &routing.Group{
				ID:               3,
				OpenAIFastPolicy: "force_off",
				ForceOpenAIFast:  true,
			}
			key := &apikey.APIKey{User: &identity.User{ID: 2}, Group: group}
			if composite {
				key.Group = nil
				key.IsComposite = true
				key.CompositeGroups = []apikey.APIKeyCompositeGroup{{GroupID: group.ID, Group: group}}
			}
			svc := newAPIKeyTestService(apiKeyTestDependencies{})
			calls := 0
			svc.SetGroupFastPolicy(func(raw string, force bool) string {
				calls++
				require.Equal(t, "force_off", raw)
				require.True(t, force)
				return "force_ultrafast"
			})

			snapshot := svc.KeySnapshotFromAPIKey(context.Background(), key)
			payload, err := json.Marshal(snapshot)
			require.NoError(t, err)
			var decoded apikey.APIKeyAuthSnapshot
			require.NoError(t, json.Unmarshal(payload, &decoded))
			restored := svc.KeySnapshotToAPIKey("sk-fast-policy", &decoded)
			if composite {
				require.Zero(t, calls)
				require.Equal(t, "force_off", restored.CompositeGroups[0].Group.OpenAIFastPolicy)
			} else {
				require.Equal(t, 1, calls)
				require.Equal(t, "force_ultrafast", restored.Group.OpenAIFastPolicy)
			}
			require.Equal(t, "force_off", group.OpenAIFastPolicy)
		})
	}
}

func TestAPIKeyAuthSnapshotGroupPreservesNilAndEmpty(t *testing.T) {
	for _, group := range []*routing.Group{
		nil,
		{},
		{
			AllowedProtocols: []protocol.ProtocolID{},
			ModelRouting:     map[string][]int64{},
		},
	} {
		svc := newAPIKeyTestService(apiKeyTestDependencies{})
		key := &apikey.APIKey{User: &identity.User{ID: 2}, Group: group}
		snapshot := svc.KeySnapshotFromAPIKey(context.Background(), key)
		restored := svc.KeySnapshotToAPIKey("sk-empty-group", snapshot)
		if group == nil {
			require.Nil(t, snapshot.Group)
			require.Nil(t, restored.Group)
			continue
		}
		require.Equal(t, group.AllowedProtocols, snapshot.Group.AllowedProtocols)
		require.Equal(t, group.AllowedProtocols, restored.Group.AllowedProtocols)
		require.Equal(t, group.ModelRouting, snapshot.Group.ModelRouting)
		require.Equal(t, group.ModelRouting, restored.Group.ModelRouting)
		require.True(t, restored.Group.Hydrated)
	}
}

func TestAPIKeyAuthSnapshotGroupCopiesMutableFields(t *testing.T) {
	fallbackID := int64(9)
	topK := 3
	group := &routing.Group{
		ID: 3,
		AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
			LBTopK: &topK,
		},
		FallbackGroupID:                 &fallbackID,
		FallbackGroupIDOnInvalidRequest: &fallbackID,
		UnavailableFallbackGroupID:      &fallbackID,
		ModelRouting:                    map[string][]int64{"model": {1}},
		SupportedModelScopes:            []string{"openai"},
		AllowedProtocols:                []protocol.ProtocolID{protocol.ProtocolOpenAIResponses},
		ProtocolFallbacks: map[protocol.ProtocolID][]protocol.ProtocolID{
			protocol.ProtocolOpenAIResponses: {protocol.ProtocolOpenAIChatCompletions},
		},
		ModelsListConfig: routing.GroupModelsListConfig{Models: []string{"model"}},
		ReasoningEffortMappings: []routing.ReasoningEffortMapping{
			{From: "max", To: "xhigh"},
		},
		RoutingPolicy: routing.GroupRoutingPolicy{
			ModelMapping:  map[string]string{"alias": "model"},
			AllowedModels: []string{"model"},
			FeaturesConfig: map[string]any{
				"feature": map[string]any{"openai": true},
			},
		},
	}
	key := &apikey.APIKey{
		User:            &identity.User{ID: 2},
		Group:           group,
		IsComposite:     true,
		CompositeGroups: []apikey.APIKeyCompositeGroup{{GroupID: group.ID, Group: group}},
	}
	svc := newAPIKeyTestService(apiKeyTestDependencies{})
	snapshot := svc.KeySnapshotFromAPIKey(context.Background(), key)
	before, err := json.Marshal(snapshot)
	require.NoError(t, err)
	restored := svc.KeySnapshotToAPIKey("sk-copy-group", snapshot)
	for _, mutable := range []*routing.Group{group, restored.Group, restored.CompositeGroups[0].Group} {
		*mutable.AdvancedSchedulerOverrides.LBTopK = 10
		*mutable.FallbackGroupID = 10
		*mutable.FallbackGroupIDOnInvalidRequest = 10
		*mutable.UnavailableFallbackGroupID = 10
		mutable.ModelRouting["model"][0] = 10
		mutable.SupportedModelScopes[0] = "changed"
		mutable.AllowedProtocols[0] = protocol.ProtocolAnthropicMessages
		mutable.ProtocolFallbacks[protocol.ProtocolOpenAIResponses][0] = protocol.ProtocolAnthropicMessages
		mutable.ModelsListConfig.Models[0] = "changed"
		mutable.ReasoningEffortMappings[0].To = "changed"
		mutable.RoutingPolicy.ModelMapping["alias"] = "changed"
		mutable.RoutingPolicy.AllowedModels[0] = "changed"
		feature, ok := mutable.RoutingPolicy.FeaturesConfig["feature"].(map[string]any)
		require.True(t, ok)
		feature["openai"] = false
		after, err := json.Marshal(snapshot)
		require.NoError(t, err)
		require.Equal(t, string(before), string(after))
	}
}

func TestAPIKeyService_RejectsV13AuthSnapshotWithoutSessionIsolationFlag(t *testing.T) {
	groupID := int64(9)
	svc := newAPIKeyTestService(apiKeyTestDependencies{})

	apiKey, ok, err := svc.KeyApplyAuthCacheEntry("k-legacy-models-list", &apikey.APIKeyAuthCacheEntry{
		Snapshot: &apikey.APIKeyAuthSnapshot{
			Version:  13,
			APIKeyID: 1,
			UserID:   2,
			GroupID:  &groupID,
			Status:   billing.StatusActive,
			User: apikey.APIKeyAuthUserSnapshot{
				ID:          2,
				Status:      billing.StatusActive,
				Role:        identity.RoleUser,
				Balance:     10,
				Concurrency: 3,
			},
			Group: &apikey.APIKeyAuthGroupSnapshot{
				ID:   groupID,
				Name: "openai",

				Status:         billing.StatusActive,
				RateMultiplier: 1,
			},
		},
	})
	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatalf("expected v13 auth snapshot to be rejected after session isolation flag was added")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

func TestAPIKeyService_RejectsV21AuthSnapshotWithoutReasoningEffortPolicy(t *testing.T) {
	svc := newAPIKeyTestService(apiKeyTestDependencies{})

	apiKey, ok, err := svc.KeyApplyAuthCacheEntry("k-legacy-reasoning-mappings", &apikey.APIKeyAuthCacheEntry{
		Snapshot: &apikey.APIKeyAuthSnapshot{Version: 21},
	})
	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v21 auth snapshot to be rejected after reasoning effort policy was added")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

func TestAPIKeyServiceRejectsV26AuthSnapshotWithoutModelMapping(t *testing.T) {
	svc := newAPIKeyTestService(apiKeyTestDependencies{})

	apiKey, ok, err := svc.KeyApplyAuthCacheEntry("k-legacy-model-mapping", &apikey.APIKeyAuthCacheEntry{
		Snapshot: &apikey.APIKeyAuthSnapshot{Version: 26},
	})
	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok || apiKey != nil {
		t.Fatal("expected v26 auth snapshot to be rejected after model mapping was added")
	}
}

func TestAPIKeyServiceRejectsV29AuthSnapshotWithoutSchedulerType(t *testing.T) {
	svc := newAPIKeyTestService(apiKeyTestDependencies{})

	apiKey, ok, err := svc.KeyApplyAuthCacheEntry("k-legacy-scheduler-type", &apikey.APIKeyAuthCacheEntry{
		Snapshot: &apikey.APIKeyAuthSnapshot{Version: 29},
	})
	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok || apiKey != nil {
		t.Fatal("expected v29 auth snapshot to be rejected after scheduler_type was added")
	}
}

func TestAPIKeyServiceRejectsV30AuthSnapshotWithoutAdvancedSchedulerOverrides(t *testing.T) {
	svc := newAPIKeyTestService(apiKeyTestDependencies{})
	apiKey, ok, err := svc.KeyApplyAuthCacheEntry("k-legacy-advanced-overrides", &apikey.APIKeyAuthCacheEntry{
		Snapshot: &apikey.APIKeyAuthSnapshot{Version: 30},
	})
	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok || apiKey != nil {
		t.Fatal("expected v30 auth snapshot to be rejected after advanced scheduler overrides were added")
	}
}

func TestAPIKeyServiceRejectsV32AuthSnapshotWithoutGroupModelPricing(t *testing.T) {
	svc := newAPIKeyTestService(apiKeyTestDependencies{})
	apiKey, ok, err := svc.KeyApplyAuthCacheEntry("k-legacy-group-pricing", &apikey.APIKeyAuthCacheEntry{
		Snapshot: &apikey.APIKeyAuthSnapshot{Version: 32},
	})
	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok || apiKey != nil {
		t.Fatal("expected v32 auth snapshot to be rejected after group model pricing was added")
	}
}

// TestAPIKeyServiceRejectsV33AuthSnapshotWithoutGroupOpenAIFast ensures old
// snapshots cannot silently omit the group-level Fast policy.
func TestAPIKeyServiceRejectsV33AuthSnapshotWithoutGroupOpenAIFast(t *testing.T) {
	svc := newAPIKeyTestService(apiKeyTestDependencies{})
	apiKey, ok, err := svc.KeyApplyAuthCacheEntry("k-legacy-group-openai-fast", &apikey.APIKeyAuthCacheEntry{
		Snapshot: &apikey.APIKeyAuthSnapshot{Version: 33},
	})
	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok || apiKey != nil {
		t.Fatal("expected v33 auth snapshot to be rejected after group OpenAI Fast was added")
	}
}

// TestAPIKeyServiceRejectsV34AuthSnapshotWithoutReasoningEffortOverLimit rejects snapshots predating the over-limit action.
func TestAPIKeyServiceRejectsV34AuthSnapshotWithoutReasoningEffortOverLimit(t *testing.T) {
	svc := newAPIKeyTestService(apiKeyTestDependencies{})
	apiKey, ok, err := svc.KeyApplyAuthCacheEntry("k-legacy-reasoning-over-limit", &apikey.APIKeyAuthCacheEntry{
		Snapshot: &apikey.APIKeyAuthSnapshot{Version: 34},
	})
	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok || apiKey != nil {
		t.Fatal("expected v34 auth snapshot to be rejected after reasoning effort over-limit action was added")
	}
}

// TestAPIKeyServiceRejectsV35AuthSnapshotWithoutFreeOpenAIFast rejects the obsolete snapshot version.
func TestAPIKeyServiceRejectsV35AuthSnapshotWithoutFreeOpenAIFast(t *testing.T) {
	svc := newAPIKeyTestService(apiKeyTestDependencies{})
	apiKey, ok, err := svc.KeyApplyAuthCacheEntry("k-legacy-free-openai-fast", &apikey.APIKeyAuthCacheEntry{
		Snapshot: &apikey.APIKeyAuthSnapshot{Version: 35},
	})
	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok || apiKey != nil {
		t.Fatal("expected v35 auth snapshot to be rejected after free OpenAI Fast was added")
	}
}

func TestAPIKeyAuthSnapshotRoundTripPreservesBillingMode(t *testing.T) {
	preferredID := int64(101)
	key := &apikey.APIKey{
		ID:                      1,
		UserID:                  7,
		Key:                     "sk_billing_mode_snapshot",
		Status:                  apikey.StatusAPIKeyActive,
		BillingMode:             apikey.APIKeyBillingModeSubscription,
		PreferredSubscriptionID: &preferredID,
		User:                    &identity.User{ID: 7, Status: billing.StatusActive, Role: identity.RoleUser},
	}
	svc := testkit.NewService(nil, nil, nil, nil, nil, nil, nil)
	svc.Start()

	snapshot := svc.KeySnapshotFromAPIKey(context.Background(), key)
	restored := svc.KeySnapshotToAPIKey(key.Key, snapshot)

	require.Equal(t, apikey.APIKeyBillingModeSubscription, restored.BillingMode)
	require.NotNil(t, restored.PreferredSubscriptionID)
	require.Equal(t, preferredID, *restored.PreferredSubscriptionID)
}

func TestAPIKeyAuthGroupSnapshotPreservesExplicitEmptyClientProtocols(t *testing.T) {
	emptySnapshot := apikey.KeyAuthGroupSnapshotFromGroup(&routing.Group{
		ID: 1,

		AllowedProtocols: []protocol.ProtocolID{},
	})
	payload, err := json.Marshal(emptySnapshot)
	require.NoError(t, err)

	var decodedEmpty apikey.APIKeyAuthGroupSnapshot
	require.NoError(t, json.Unmarshal(payload, &decodedEmpty))
	require.NotNil(t, decodedEmpty.AllowedProtocols)
	require.Empty(t, decodedEmpty.AllowedProtocols)
	require.False(t, apikey.KeyGroupFromAuthSnapshot(&decodedEmpty).AllowsClientProtocol(protocol.ProtocolAnthropicMessages))
}

func TestAPIKeyServiceSnapshotRoundTripPreservesIndependentModelMapping(t *testing.T) {
	svc := testkit.NewService(nil, nil, nil, nil, nil, nil, &config.Config{})
	svc.Start()
	apiKey := &apikey.APIKey{
		ID:           1,
		UserID:       2,
		Key:          "k-model-mapping",
		Status:       billing.StatusActive,
		ModelMapping: map[string]string{"review": "gpt-5.6-luna"},
		User:         &identity.User{ID: 2, Status: billing.StatusActive},
	}

	snapshot := svc.KeySnapshotFromAPIKey(context.Background(), apiKey)
	require.Equal(t, apikey.KeyApiKeyAuthSnapshotVersion, snapshot.Version)
	roundTrip := svc.KeySnapshotToAPIKey(apiKey.Key, snapshot)
	require.Equal(t, apiKey.ModelMapping, roundTrip.ModelMapping)

	roundTrip.ModelMapping["review"] = "changed"
	require.Equal(t, "gpt-5.6-luna", snapshot.ModelMapping["review"])
}

func TestAPIKeyService_SnapshotRoundTrip_PreservesReasoningEffortPolicy(t *testing.T) {
	svc := testkit.NewService(nil, nil, nil, nil, nil, nil, &config.Config{})
	svc.Start()
	groupID := int64(9)
	apiKey := &apikey.APIKey{
		ID:      1,
		UserID:  2,
		GroupID: &groupID,
		Key:     "k-reasoning-policy",
		Status:  billing.StatusActive,
		User: &identity.User{
			ID:          2,
			Status:      billing.StatusActive,
			Role:        identity.RoleUser,
			Balance:     10,
			Concurrency: 3,
		},
		Group: &routing.Group{
			ID:   groupID,
			Name: "openai",

			Status:                      billing.StatusActive,
			RateMultiplier:              1,
			MaxReasoningEffort:          "medium",
			MaxReasoningEffortOverLimit: routing.ReasoningEffortOverLimitDeny,
			ReasoningEffortMappings: []routing.ReasoningEffortMapping{
				{From: "max", To: "xhigh"},
			},
		},
	}

	snapshot := svc.KeySnapshotFromAPIKey(context.Background(), apiKey)
	roundTrip := svc.KeySnapshotToAPIKey(apiKey.Key, snapshot)

	require.NotNil(t, roundTrip)
	require.NotNil(t, roundTrip.Group)
	require.Equal(t, "medium", roundTrip.Group.MaxReasoningEffort)
	require.Equal(t, routing.ReasoningEffortOverLimitDeny, roundTrip.Group.MaxReasoningEffortOverLimit)
	require.Equal(t, apiKey.Group.ReasoningEffortMappings, roundTrip.Group.ReasoningEffortMappings)
}

func TestAPIKeySnapshotPreservesIndependentRoutingPolicy(t *testing.T) {
	svc := testkit.NewService(nil, nil, nil, nil, nil, nil, &config.Config{})
	groupID := int64(9)
	key := &apikey.APIKey{
		ID: 1, UserID: 2, GroupID: &groupID, Key: "policy-snapshot", Status: billing.StatusActive,
		User: &identity.User{ID: 2, Status: billing.StatusActive}, Group: &routing.Group{
			ID: groupID,
			RoutingPolicy: routing.GroupRoutingPolicy{
				Enabled: true, RestrictModels: true, RestrictionModelSource: routing.BillingModelSourceUpstream,
				ModelMapping: map[string]string{"alias": "real"}, AllowedModels: []string{"real"},
				FeaturesConfig: map[string]any{"codex_image_generation_bridge": map[string]any{"openai": false}},
			},
		},
	}
	snapshot := svc.KeySnapshotFromAPIKey(context.Background(), key)
	require.Equal(t, apikey.KeyApiKeyAuthSnapshotVersion, snapshot.Version)
	restored := svc.KeySnapshotToAPIKey(key.Key, snapshot)
	require.Equal(t, key.Group.RoutingPolicy, restored.Group.RoutingPolicy)
	restored.Group.RoutingPolicy.ModelMapping["alias"] = "changed"
	restored.Group.RoutingPolicy.AllowedModels[0] = "changed"
	require.Equal(t, "real", snapshot.Group.RoutingPolicy.ModelMapping["alias"])
	require.Equal(t, "real", snapshot.Group.RoutingPolicy.AllowedModels[0])
	snapshot.Version = 40
	cached, ok, err := svc.KeyApplyAuthCacheEntry(key.Key, &apikey.APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, cached)
}

func TestHydrateTeamAPIKeyOnlyMapsMissingContextToMembershipError(t *testing.T) {
	repositoryFailure := errors.New("team repository unavailable")
	tests := []struct {
		name    string
		repoErr error
		want    error
	}{
		{name: "team_missing", repoErr: team.ErrTeamNotFound, want: team.ErrTeamMembershipRequired},
		{name: "repository_failure", repoErr: repositoryFailure, want: repositoryFailure},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := validTeamAPIKeyForLifecycleTest()
			key.Team = nil
			key.TeamMembership = nil
			key.ActorUser = nil
			key.User = nil
			service := newAPIKeyTestService(apiKeyTestDependencies{
				teamRepo: &teamContextErrorRepository{err: test.repoErr},
				cfg:      &config.Config{Team: config.TeamConfig{Enabled: true}},
			})

			_, err := service.KeyHydrateTeamAPIKey(context.Background(), key, nil)
			require.ErrorIs(t, err, test.want)
		})
	}
}

func (r *teamContextErrorRepository) GetContextByUserID(context.Context, int64) (*team.TeamContext, error) {
	return nil, r.err
}
