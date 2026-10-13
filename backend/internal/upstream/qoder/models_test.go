package qoder

import (
	"slices"
	"testing"
)

// Published model IDs are compared as the sorted union expected by the request
// catalogue. Local import conversion is checked separately with supplied
// identity fields, keeping catalogue membership independent of the private token
// representation used for session creation.

// TestDefaultModels compares the sorted public model union with the expected
// alias set. It checks count and every sorted entry, catching missing,
// duplicated, or unexpected model names.
func TestDefaultModels(t *testing.T) {
	ids := DefaultRequestModelIDs()
	want := []string{
		"claude-opus-4-6",
		"auto",
		"performance",
		"efficient",
		"lite",
		"qwen3.8-max",
		"qwen3.7-max",
		"qwen3.7-plus",
		"kimi-k3",
		"kimi-k2.7-code",
		"glm-5.3",
		"glm-5.2",
		"deepseek-v4-pro",
		"deepseek-v4-flash",
		"minimax-m3",
		"qwen3.6-flash",
		"minimax-m2.7",
	}
	if len(ids) != len(want) {
		t.Fatalf("default model count = %d, want %d", len(ids), len(want))
	}
	slices.Sort(want)
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("default model %d = %q, want %q", i, ids[i], want[i])
		}
	}
}

// TestLocalAuthInfoToIdentity converts a local fixture with an existing COSY
// security token. It checks the name, UID, and chosen token in the resulting
// session identity.
func TestLocalAuthInfoToIdentity(t *testing.T) {
	info := &AuthInfo{
		Name:               "test user",
		UID:                "uid123",
		SecurityOauthToken: "dt-token",
		RefreshToken:       "drt-refresh",
		UserType:           "personal_standard",
	}
	id := info.ToAuthIdentity()
	if id.Name != "test user" {
		t.Errorf("name = %q", id.Name)
	}
	if id.UID != "uid123" {
		t.Errorf("uid = %q", id.UID)
	}
	if id.SecurityOauthToken != "dt-token" {
		t.Errorf("token = %q", id.SecurityOauthToken)
	}
}
