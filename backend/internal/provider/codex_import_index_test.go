package provider

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexProviderIndexDoesNotMatchDifferentUsersInSameChatGPTAccount(t *testing.T) {
	existing := Record{
		ID: 10,
		Credentials: map[string]any{
			"chatgpt_account_id": "team-1",
			"chatgpt_user_id":    "user-1",
			"access_token":       "token-1",
			"refresh_token":      "refresh-1",
		},
	}
	index := BuildCodexProviderIndex([]Record{existing})

	keys := BuildCodexImportIdentityKeys("team-1", "user-2", "", "token-2", "refresh-2")
	if got, _ := index.Find(keys, "user-2"); got != nil {
		t.Fatalf("Find matched provider ID %d for a different chatgpt_user_id in the same team", got.ID)
	}

	keys = BuildCodexImportIdentityKeys("team-1", "user-1", "", "token-2", "refresh-2")
	got, _ := index.Find(keys, "user-1")
	if got == nil || got.ID != existing.ID {
		t.Fatalf("Find by same chatgpt_user_id = %v, want provider ID %d", got, existing.ID)
	}
}

func TestCodexProviderIndexFallsBackToProviderKeyWhenRefreshTokenExistsAndUserIDMissing(t *testing.T) {
	// A full OAuth import with a user ID can match a stored provider
	// whose chatgpt_user_id is missing and fill that field.
	legacy := Record{
		ID: 20,
		Credentials: map[string]any{
			"chatgpt_account_id": "team-1",
			"access_token":       "token-old",
			"refresh_token":      "refresh-old",
		},
	}
	index := BuildCodexProviderIndex([]Record{legacy})

	keys := BuildCodexImportIdentityKeys("team-1", "user-1", "", "token-new", "refresh-new")
	got, matchedKey := index.Find(keys, "user-1")
	if got == nil || got.ID != legacy.ID {
		t.Fatalf("Find legacy provider without stored user id = %v, want provider ID %d", got, legacy.ID)
	}
	if matchedKey != "provider:team-1" {
		t.Fatalf("matched key = %q, want provider:team-1", matchedKey)
	}

	// Conversely, a full OAuth import lacking a user ID can match
	// an existing provider through its provider key.
	full := Record{
		ID: 21,
		Credentials: map[string]any{
			"chatgpt_account_id": "team-2",
			"chatgpt_user_id":    "user-9",
			"access_token":       "token-old",
			"refresh_token":      "refresh-old",
		},
	}
	index = BuildCodexProviderIndex([]Record{full})

	keys = BuildCodexImportIdentityKeys("team-2", "", "", "token-opaque", "refresh-new")
	got, _ = index.Find(keys, "")
	if got == nil || got.ID != full.ID {
		t.Fatalf("Find by provider key without entry user id = %v, want provider ID %d", got, full.ID)
	}
}

func TestCodexProviderIndexAccessTokenOnlyUsesTokenFingerprint(t *testing.T) {
	existing := Record{
		ID: 22,
		Credentials: map[string]any{
			"chatgpt_account_id": "team-1",
			"chatgpt_user_id":    "user-1",
			"access_token":       "token-old",
		},
	}
	index := BuildCodexProviderIndex([]Record{existing})

	keys := BuildCodexImportIdentityKeys("team-1", "user-1", "", "token-new", "")
	if got, matchedKey := index.Find(keys, "user-1"); got != nil {
		t.Fatalf("accessToken-only import matched by %q despite different token: provider ID %d", matchedKey, got.ID)
	}

	keys = BuildCodexImportIdentityKeys("team-1", "user-1", "", "token-old", "")
	got, matchedKey := index.Find(keys, "user-1")
	if got == nil || got.ID != existing.ID {
		t.Fatalf("Find accessToken-only duplicate by fingerprint = %v, want provider ID %d", got, existing.ID)
	}
	if !strings.HasPrefix(matchedKey, "access:") {
		t.Fatalf("matched key = %q, want access fingerprint", matchedKey)
	}
}

func TestCodexProviderIndexKeepsAllCandidatesForSharedProviderKey(t *testing.T) {
	legacy := Record{
		ID: 30,
		Credentials: map[string]any{
			"chatgpt_account_id": "team-1",
			"access_token":       "token-legacy",
			"refresh_token":      "refresh-legacy",
		},
	}
	member := Record{
		ID: 31,
		Credentials: map[string]any{
			"chatgpt_account_id": "team-1",
			"chatgpt_user_id":    "user-2",
			"access_token":       "token-member",
			"refresh_token":      "refresh-member",
		},
	}

	// Regardless of index order, a new user ID must skip the other member
	// and match the legacy provider with no user ID.
	for _, providers := range [][]Record{
		{member, legacy},
		{legacy, member},
	} {
		index := BuildCodexProviderIndex(providers)

		keys := BuildCodexImportIdentityKeys("team-1", "user-1", "", "token-new", "refresh-new")
		got, matchedKey := index.Find(keys, "user-1")
		if got == nil || got.ID != legacy.ID {
			t.Fatalf("Find with shared provider key = %v, want legacy provider ID %d", got, legacy.ID)
		}
		if matchedKey != "provider:team-1" {
			t.Fatalf("matched key = %q, want provider:team-1", matchedKey)
		}

		keys = BuildCodexImportIdentityKeys("team-1", "user-2", "", "token-new", "refresh-new")
		got, matchedKey = index.Find(keys, "user-2")
		if got == nil || got.ID != member.ID {
			t.Fatalf("Find by user key = %v, want member provider ID %d", got, member.ID)
		}
		if matchedKey != "user:user-2" {
			t.Fatalf("matched key = %q, want user:user-2", matchedKey)
		}
	}
}

func TestCodexProviderIndexUpsertReplacesSameProvider(t *testing.T) {
	legacy := Record{
		ID: 40,
		Credentials: map[string]any{
			"chatgpt_account_id": "team-1",
			"access_token":       "token-old",
		},
	}
	index := BuildCodexProviderIndex([]Record{legacy})

	backfilled := Record{
		ID: 40,
		Credentials: map[string]any{
			"chatgpt_account_id": "team-1",
			"chatgpt_user_id":    "user-1",
			"access_token":       "token-new",
			"refresh_token":      "refresh-new",
		},
	}
	index.Add(backfilled)

	// After backfill, the provider-key entry contains the new user ID.
	// Queries by other members must use that updated identity.
	keys := BuildCodexImportIdentityKeys("team-1", "user-2", "", "token-other", "refresh-other")
	if got, matchedKey := index.Find(keys, "user-2"); got != nil {
		t.Fatalf("stale candidate matched after upsert by %q: provider ID %d", matchedKey, got.ID)
	}

	keys = BuildCodexImportIdentityKeys("team-1", "user-1", "", "token-other", "refresh-other")
	got, _ := index.Find(keys, "user-1")
	if got == nil || got.ID != backfilled.ID {
		t.Fatalf("Find after upsert = %v, want provider ID %d", got, backfilled.ID)
	}
	if uid := CodexCredentialString(got.Credentials, "chatgpt_user_id"); uid != "user-1" {
		t.Fatalf("upsert did not replace credentials, chatgpt_user_id = %q", uid)
	}
}

func TestCodexProviderIndexUpdateRemovesAllPreviousKeys(t *testing.T) {
	legacy := Record{
		ID: 50,
		Credentials: map[string]any{
			"chatgpt_account_id": "team-old",
			"chatgpt_user_id":    "user-old",
			"email":              "old@example.com",
			"access_token":       "access-old",
			"agent_runtime_id":   "runtime-old",
		},
	}
	index := BuildCodexProviderIndex([]Record{legacy})

	updated := Record{
		ID: 50,
		Credentials: map[string]any{
			"chatgpt_account_id": "team-new",
			"chatgpt_user_id":    "user-new",
			"email":              "new@example.com",
			"access_token":       "access-new",
			"agent_runtime_id":   "runtime-new",
		},
	}
	index.Add(updated)

	oldKeys := append(BuildCodexStoredIdentityKeys("team-old", "user-old", "old@example.com", "access-old"), "agent:runtime-old")
	for _, key := range oldKeys {
		if got, matchedKey := index.Find([]string{key}, "user-old"); got != nil {
			t.Fatalf("stale provider matched by %q: provider ID %d", matchedKey, got.ID)
		}
	}

	newKeys := append(BuildCodexStoredIdentityKeys("team-new", "user-new", "new@example.com", "access-new"), "agent:runtime-new")
	for _, key := range newKeys {
		got, matchedKey := index.Find([]string{key}, "user-new")
		if got == nil || got.ID != updated.ID {
			t.Fatalf("updated provider not found by %q: provider=%v matched=%q", key, got, matchedKey)
		}
	}
}

func TestCodexProviderIndexUpdatePreservesSharedKeyCandidateOrder(t *testing.T) {
	first := Record{
		ID: 60,
		Credentials: map[string]any{
			"chatgpt_account_id": "team-shared",
			"access_token":       "access-first-old",
		},
	}
	second := Record{
		ID: 61,
		Credentials: map[string]any{
			"chatgpt_account_id": "team-shared",
			"access_token":       "access-second",
		},
	}
	index := BuildCodexProviderIndex([]Record{first, second})

	first.Credentials["access_token"] = "access-first-new"
	index.Add(first)

	got, matchedKey := index.Find([]string{"provider:team-shared"}, "")
	if got == nil || got.ID != first.ID {
		t.Fatalf("shared key candidate order changed after update: provider=%v matched=%q", got, matchedKey)
	}
}

func TestCodexIdentitySeenDistinguishesTeamMembers(t *testing.T) {
	seen := map[string]CodexSeenIdentity{}
	member1 := BuildCodexImportIdentityKeys("team-1", "user-1", "", "token-1", "refresh-1")
	MarkCodexIdentitySeen(seen, member1, 1, "user-1")

	member2 := BuildCodexImportIdentityKeys("team-1", "user-2", "", "token-2", "refresh-2")
	if index, ok := FirstSeenCodexIdentity(seen, member2, "user-2"); ok {
		t.Fatalf("different team member treated as duplicate of entry %d", index)
	}

	again := BuildCodexImportIdentityKeys("team-1", "user-1", "", "token-3", "refresh-3")
	index, ok := FirstSeenCodexIdentity(seen, again, "user-1")
	if !ok || index != 1 {
		t.Fatalf("same user re-entry dedup = (%d, %v), want (1, true)", index, ok)
	}

	// An entry without a user ID must not deduplicate against team members by provider ID.
	// Its access-token fingerprint determines duplication.
	opaque := BuildCodexImportIdentityKeys("team-1", "", "", "token-4", "")
	index, ok = FirstSeenCodexIdentity(seen, opaque, "")
	if ok {
		t.Fatalf("entry without user id dedup = (%d, %v), want no match", index, ok)
	}
}

// TestCodexImportIndexSnapshotIsolation checks independent stored and returned credential copies.
func TestCodexImportIndexSnapshotIsolation(t *testing.T) {
	original := Record{ID: 7, Credentials: map[string]any{"access_token": "token", "refresh_token": "refresh", "chatgpt_account_id": "team", "nested": map[string]any{"value": "original"}}}
	index := BuildCodexProviderIndex([]Record{original})
	nested, ok := original.Credentials["nested"].(map[string]any)
	require.True(t, ok)
	nested["value"] = "outside"
	first, key := index.Find([]string{"provider:team"}, "")
	require.NotNil(t, first)
	require.Equal(t, "provider:team", key)
	copy, ok := first.Credentials["nested"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "original", copy["value"])
	copy["value"] = "response"
	second, _ := index.Find([]string{"provider:team"}, "")
	require.NotNil(t, second)
	again, ok := second.Credentials["nested"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "original", again["value"])
}

func TestCodexAgentIdentityIndexSeparatesTeamsForSameUser(t *testing.T) {
	existing := Record{
		ID: 1,
		Credentials: map[string]any{
			"auth_mode":          OpenAIAuthModeAgentIdentity,
			"chatgpt_account_id": "team-a",
			"chatgpt_user_id":    "same-user",
			"agent_runtime_id":   "runtime-a",
		},
	}
	index := BuildCodexProviderIndex([]Record{existing})

	teamBKeys := BuildCodexAgentIdentityKeys("team-b")
	matched, _ := index.Find(teamBKeys, "same-user")
	require.Nil(t, matched)

	teamAKeys := BuildCodexAgentIdentityKeys("team-a")
	matched, matchedKey := index.Find(teamAKeys, "same-user")
	require.NotNil(t, matched)
	require.Equal(t, int64(1), matched.ID)
	require.Equal(t, "provider:team-a", matchedKey)
}
