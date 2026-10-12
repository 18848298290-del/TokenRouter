package qoder

import (
	"slices"
	"testing"
)

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
