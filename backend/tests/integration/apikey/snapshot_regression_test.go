package apikey_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/apikey/testkit"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/team"
)

// Authentication snapshots can outlive both their source objects and objects
// restored for individual requests. The mutation matrix serializes a snapshot
// before and after editing selected pointer, slice, and map paths in either
// object, while a JSON round trip checks composite-group Fast policies.

// isolationKey returns a fresh key with nested pointers, slices, maps, and
// authentication relationships for mutation cases.
func isolationKey() *apikey.APIKey {
	groupID, teamID, subscriptionID := int64(3), int64(4), int64(5)
	threshold := 3.0
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return &apikey.APIKey{
		ID: 1, UserID: 2, Key: "test-test-only", GroupID: &groupID, TeamID: &teamID, PreferredSubscriptionID: &subscriptionID, ExpiresAt: &now,
		IPWhitelist: []string{"127.0.0.1"}, IPBlacklist: []string{"192.0.2.1"}, ModelMapping: map[string]string{"alias": "original"},
		User: &identity.User{ID: 2, AllowedGroups: []int64{3}, BalanceNotifyThreshold: &threshold, BalanceNotifyExtraEmails: []billing.NotifyEmailSummary{{Email: "test@example.invalid"}}},
		Team: &team.Team{ID: 4}, TeamMembership: &team.TeamMembership{ID: 6, DailyWindowStart: &now},
		Group: &routing.Group{
			ID: 3, ModelRouting: map[string][]int64{"model": {7, 8}}, SupportedModelScopes: []string{"claude"},
			ModelsListConfig: routing.GroupModelsListConfig{Enabled: true, Models: []string{"model"}},
		},
	}
}

// TestAuthSnapshotIsolation mutates selected source-key and restored-request
// fields after snapshot creation. The snapshot JSON remains byte-identical for
// each pointer, slice, map, and nested relationship exercised.
func TestAuthSnapshotIsolation(t *testing.T) {
	changes := map[string]func(*apikey.APIKey){
		"group_id":         func(k *apikey.APIKey) { *k.GroupID = 99 },
		"team_id":          func(k *apikey.APIKey) { *k.TeamID = 99 },
		"subscription_id":  func(k *apikey.APIKey) { *k.PreferredSubscriptionID = 99 },
		"expiry":           func(k *apikey.APIKey) { *k.ExpiresAt = k.ExpiresAt.Add(time.Hour) },
		"whitelist":        func(k *apikey.APIKey) { k.IPWhitelist[0] = "changed" },
		"blacklist":        func(k *apikey.APIKey) { k.IPBlacklist[0] = "changed" },
		"model_mapping":    func(k *apikey.APIKey) { k.ModelMapping["alias"] = "changed" },
		"allowed_groups":   func(k *apikey.APIKey) { k.User.AllowedGroups[0] = 99 },
		"notify_threshold": func(k *apikey.APIKey) { *k.User.BalanceNotifyThreshold = 99 },
		"notify_emails":    func(k *apikey.APIKey) { k.User.BalanceNotifyExtraEmails[0].Email = "changed" },
		"member":           func(k *apikey.APIKey) { k.TeamMembership.ID = 99 },
		"member_window": func(k *apikey.APIKey) {
			*k.TeamMembership.DailyWindowStart = k.TeamMembership.DailyWindowStart.Add(time.Hour)
		},
		"routing_map":   func(k *apikey.APIKey) { k.Group.ModelRouting["extra"] = []int64{99} },
		"routing_slice": func(k *apikey.APIKey) { k.Group.ModelRouting["model"][0] = 99 },
		"scopes":        func(k *apikey.APIKey) { k.Group.SupportedModelScopes[0] = "changed" },
		"models":        func(k *apikey.APIKey) { k.Group.ModelsListConfig.Models[0] = "changed" },
	}
	for name, change := range changes {
		for _, direction := range []string{"source", "request"} {
			t.Run(name+"/"+direction, func(t *testing.T) {
				s := testkit.NewService(nil, nil, nil, nil, nil, nil, nil)
				key := isolationKey()
				snapshot := s.KeySnapshotFromAPIKey(context.Background(), key)
				before, err := json.Marshal(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				// Compare the retained snapshot after a mutation on either side of its
				// conversion, exposing shared storage in each selected mutable path.
				if direction == "source" {
					change(key)
				} else {
					change(s.KeySnapshotToAPIKey(key.Key, snapshot))
				}
				after, err := json.Marshal(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("请求或来源修改污染了认证缓存快照")
				}
			})
		}
	}
}

// TestCompositeSnapshotFastPolicy serializes composite-group force_off and
// force_ultrafast settings through JSON. Restoring the decoded authentication
// snapshot returns the same Fast policy for each supplied value.
func TestCompositeSnapshotFastPolicy(t *testing.T) {
	for _, policy := range []string{"force_off", "force_ultrafast"} {
		t.Run(policy, func(t *testing.T) {
			s := testkit.NewService(nil, nil, nil, nil, nil, nil, nil)
			key := &apikey.APIKey{ID: 1, UserID: 2, User: &identity.User{ID: 2}, IsComposite: true, CompositeGroups: []apikey.APIKeyCompositeGroup{{GroupID: 3, Group: &routing.Group{ID: 3, OpenAIFastPolicy: policy}}}}
			snapshot := s.KeySnapshotFromAPIKey(context.Background(), key)
			payload, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var decoded apikey.APIKeyAuthSnapshot
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatal(err)
			}
			restored := s.KeySnapshotToAPIKey("test-test-only", &decoded)
			if got := restored.CompositeGroups[0].Group.OpenAIFastPolicy; got != policy {
				t.Fatalf("Fast 策略丢失：got %q want %q", got, policy)
			}
		})
	}
}
