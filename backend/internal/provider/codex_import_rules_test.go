package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestParseCodexSessionImportEntriesSupportsRawTokenJSONAndArray mixes raw,
// object, and array content in one request. It checks three entries and their
// normalized token or decoded email values in source order.
func TestParseCodexSessionImportEntriesSupportsRawTokenJSONAndArray(t *testing.T) {
	token1 := "raw-access-token-1"
	token2 := buildCodexTestJWTForTest(t, time.Now().Add(time.Hour), map[string]any{
		"email": "json@example.com",
	})
	token3 := "raw-access-token-3"

	req := CodexSessionImportRequest{
		Content: fmt.Sprintf("%s\n{\"accessToken\":%q}\n[%q]", token1, token2, token3),
	}

	entries, err := ParseCodexSessionImportEntries(req)
	if err != nil {
		t.Fatalf("ParseCodexSessionImportEntries error = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}

	first, err := NormalizeCodexImportEntry(entries[0], CodexImportOptions{Now: time.Now, OAuthClientID: "app_EMoamEEZ73f0CkXaXp7hrann"})
	if err != nil {
		t.Fatalf("normalize raw token error = %v", err)
	}
	if first.Credentials["access_token"] != token1 {
		t.Fatalf("raw token access_token = %v, want %s", first.Credentials["access_token"], token1)
	}

	second, err := NormalizeCodexImportEntry(entries[1], CodexImportOptions{Now: time.Now, OAuthClientID: "app_EMoamEEZ73f0CkXaXp7hrann"})
	if err != nil {
		t.Fatalf("normalize json token error = %v", err)
	}
	if second.Email != "json@example.com" {
		t.Fatalf("email = %q, want json@example.com", second.Email)
	}

	third, err := NormalizeCodexImportEntry(entries[2], CodexImportOptions{Now: time.Now, OAuthClientID: "app_EMoamEEZ73f0CkXaXp7hrann"})
	if err != nil {
		t.Fatalf("normalize array token error = %v", err)
	}
	if third.Credentials["access_token"] != token3 {
		t.Fatalf("array token access_token = %v, want %s", third.Credentials["access_token"], token3)
	}
}

// TestParseCodexSessionImportEntriesFallsBackToLineModeForMixedJSONAndToken
// starts with JSON followed by a raw token. It checks recovered two-entry
// parsing and each normalized token value after whole-stream decoding fails.
func TestParseCodexSessionImportEntriesFallsBackToLineModeForMixedJSONAndToken(t *testing.T) {
	req := CodexSessionImportRequest{
		Content: "{\"accessToken\":\"json-line-token\"}\nraw-line-token",
	}

	entries, err := ParseCodexSessionImportEntries(req)
	if err != nil {
		t.Fatalf("ParseCodexSessionImportEntries error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}

	first, err := NormalizeCodexImportEntry(entries[0], CodexImportOptions{Now: time.Now, OAuthClientID: "app_EMoamEEZ73f0CkXaXp7hrann"})
	if err != nil {
		t.Fatalf("normalize json line error = %v", err)
	}
	if first.Credentials["access_token"] != "json-line-token" {
		t.Fatalf("json line access_token = %v, want json-line-token", first.Credentials["access_token"])
	}

	second, err := NormalizeCodexImportEntry(entries[1], CodexImportOptions{Now: time.Now, OAuthClientID: "app_EMoamEEZ73f0CkXaXp7hrann"})
	if err != nil {
		t.Fatalf("normalize raw line error = %v", err)
	}
	if second.Credentials["access_token"] != "raw-line-token" {
		t.Fatalf("raw line access_token = %v, want raw-line-token", second.Credentials["access_token"])
	}
}

// TestNormalizeCodexSessionJSONExtractsCredentialsAndIgnoresSessionToken
// combines explicit account/user data with different JWT hints and a browser
// session token. It checks explicit identity precedence, ignored renewal
// material, session metadata, and parsed token expiry.
func TestNormalizeCodexSessionJSONExtractsCredentialsAndIgnoresSessionToken(t *testing.T) {
	accessToken := buildCodexTestJWTForTest(t, time.Now().Add(time.Hour), map[string]any{
		"email": "claim@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-from-claim",
			"chatgpt_user_id":    "user-from-claim",
			"chatgpt_plan_type":  "plus",
			"poid":               "org-from-claim",
		},
	})
	raw := map[string]any{
		"user": map[string]any{
			"id":    "user-from-json",
			"name":  "Sup OO",
			"email": "json@example.com",
			"image": "https://example.com/avatar.png",
		},
		"account": map[string]any{
			"id":       "acct-from-json",
			"planType": "free",
		},
		"accessToken":  accessToken,
		"sessionToken": "secret-session-token",
		"expires":      "2026-08-05T13:40:42.836Z",
	}

	item, err := NormalizeCodexImportEntry(CodexImportEntry{Index: 1, Value: raw}, CodexImportOptions{Now: time.Now, OAuthClientID: "app_EMoamEEZ73f0CkXaXp7hrann"})
	if err != nil {
		t.Fatalf("NormalizeCodexImportEntry error = %v", err)
	}
	if item.Credentials["access_token"] != accessToken {
		t.Fatalf("access_token not stored")
	}
	if item.Credentials["email"] != "json@example.com" {
		t.Fatalf("email = %v, want json@example.com", item.Credentials["email"])
	}
	if item.Credentials["chatgpt_account_id"] != "acct-from-json" {
		t.Fatalf("chatgpt_account_id = %v, want acct-from-json", item.Credentials["chatgpt_account_id"])
	}
	if item.Credentials["chatgpt_user_id"] != "user-from-json" {
		t.Fatalf("chatgpt_user_id = %v, want user-from-json", item.Credentials["chatgpt_user_id"])
	}
	if item.Credentials["plan_type"] != "free" {
		t.Fatalf("plan_type = %v, want free", item.Credentials["plan_type"])
	}
	if _, ok := item.Credentials["session_token"]; ok {
		t.Fatalf("session_token should not be written to credentials")
	}
	if item.Extra["session_token_present"] != true {
		t.Fatalf("session_token_present = %v, want true", item.Extra["session_token_present"])
	}
	if item.Extra["session_expires_at"] != "2026-08-05T13:40:42Z" {
		t.Fatalf("session_expires_at = %v", item.Extra["session_expires_at"])
	}
	if item.TokenExpiresAt == nil {
		t.Fatalf("TokenExpiresAt should be parsed from accessToken")
	}
}

// TestMergeCodexImportCredentialsPreservesExistingRefreshFieldsWhenIncomingHasNoRefreshToken
// supplies an access-only update over renewable credentials. It checks
// incoming token/account replacement, retained refresh/client fields, removed
// ID token, and the listed unrelated fields.
func TestMergeCodexImportCredentialsPreservesExistingRefreshFieldsWhenIncomingHasNoRefreshToken(t *testing.T) {
	existing := map[string]any{
		"access_token":       "old-access-token",
		"refresh_token":      "old-refresh-token",
		"client_id":          "old-client-id",
		"id_token":           "old-id-token",
		"model_mapping":      map[string]any{"from": "existing"},
		"chatgpt_account_id": "acct-old",
		"unrelated_existing": "keep",
	}
	incoming := map[string]any{
		"access_token":       "new-access-token",
		"expires_at":         "2026-08-05T13:40:42Z",
		"chatgpt_account_id": "acct-new",
	}
	item := &CodexImportProvider{
		AccessToken: "new-access-token",
	}

	merged := MergeCodexImportCredentials(existing, incoming, item)

	if merged["access_token"] != "new-access-token" {
		t.Fatalf("access_token = %v, want new-access-token", merged["access_token"])
	}
	if merged["chatgpt_account_id"] != "acct-new" {
		t.Fatalf("chatgpt_account_id = %v, want acct-new", merged["chatgpt_account_id"])
	}
	if merged["refresh_token"] != "old-refresh-token" {
		t.Fatalf("refresh_token = %v, want old-refresh-token", merged["refresh_token"])
	}
	if merged["client_id"] != "old-client-id" {
		t.Fatalf("client_id = %v, want old-client-id", merged["client_id"])
	}
	if _, ok := merged["id_token"]; ok {
		t.Fatalf("id_token should be cleared")
	}
	if merged["unrelated_existing"] != "keep" {
		t.Fatalf("unrelated_existing = %v, want keep", merged["unrelated_existing"])
	}
	if _, ok := merged["model_mapping"]; !ok {
		t.Fatalf("model_mapping should be preserved")
	}
}

// TestMergeCodexImportCredentialsKeepsRefreshFieldsWhenIncomingHasRefreshToken
// supplies new renewal, client, and ID token data. It checks those incoming
// values replace the configured existing credential fields.
func TestMergeCodexImportCredentialsKeepsRefreshFieldsWhenIncomingHasRefreshToken(t *testing.T) {
	existing := map[string]any{
		"refresh_token": "old-refresh-token",
		"client_id":     "old-client-id",
		"id_token":      "old-id-token",
	}
	incoming := map[string]any{
		"access_token":  "new-access-token",
		"refresh_token": "new-refresh-token",
		"client_id":     "new-client-id",
		"id_token":      "new-id-token",
	}
	item := &CodexImportProvider{
		AccessToken:  "new-access-token",
		RefreshToken: "new-refresh-token",
		IDToken:      "new-id-token",
	}

	merged := MergeCodexImportCredentials(existing, incoming, item)

	if merged["refresh_token"] != "new-refresh-token" {
		t.Fatalf("refresh_token = %v, want new-refresh-token", merged["refresh_token"])
	}
	if merged["client_id"] != "new-client-id" {
		t.Fatalf("client_id = %v, want new-client-id", merged["client_id"])
	}
	if merged["id_token"] != "new-id-token" {
		t.Fatalf("id_token = %v, want new-id-token", merged["id_token"])
	}
}

// TestNormalizeCodexImportRejectsExpiredAccessToken imports an unsigned
// metadata JWT expired an hour earlier. It checks normalization returns an
// error containing the configured expired-token diagnostic.
func TestNormalizeCodexImportRejectsExpiredAccessToken(t *testing.T) {
	expiredToken := buildCodexTestJWTForTest(t, time.Now().Add(-time.Hour), map[string]any{})

	_, err := NormalizeCodexImportEntry(CodexImportEntry{Index: 1, Value: expiredToken}, CodexImportOptions{Now: time.Now, OAuthClientID: "app_EMoamEEZ73f0CkXaXp7hrann"})
	if err == nil {
		t.Fatal("NormalizeCodexImportEntry error = nil, want expired token error")
	}
	if !strings.Contains(err.Error(), "has expired") {
		t.Fatalf("error = %v, want expired token message", err)
	}
}

// TestResolveCodexImportExpiryForNoRefreshTokenUsesTokenExpiry supplies token
// expiry with an explicitly disabled pause request. It checks both selected
// lifetimes, forced pause, and at least one returned warning.
func TestResolveCodexImportExpiryForNoRefreshTokenUsesTokenExpiry(t *testing.T) {
	tokenExpiresAt := time.Now().Add(time.Hour).UTC()
	item := &CodexImportProvider{
		AccessToken:    "access-token",
		Credentials:    map[string]any{"access_token": "access-token"},
		TokenExpiresAt: &tokenExpiresAt,
		WarningTexts:   []string{},
	}
	disabled := false
	req := CodexSessionImportRequest{AutoPauseOnExpired: &disabled}

	providerExpiresAt, credentialExpiresAt, autoPause, warnings, err := ResolveCodexImportExpiry(req, item, time.Now)
	if err != nil {
		t.Fatalf("ResolveCodexImportExpiry error = %v", err)
	}
	if providerExpiresAt == nil || *providerExpiresAt != tokenExpiresAt.Unix() {
		t.Fatalf("provider expires_at = %v, want %d", providerExpiresAt, tokenExpiresAt.Unix())
	}
	if credentialExpiresAt == nil || credentialExpiresAt.Unix() != tokenExpiresAt.Unix() {
		t.Fatalf("credential expires_at = %v, want %s", credentialExpiresAt, tokenExpiresAt)
	}
	if autoPause == nil || !*autoPause {
		t.Fatalf("autoPause = %v, want true", autoPause)
	}
	if len(warnings) == 0 {
		t.Fatalf("warnings should not be empty")
	}
}

// TestResolveCodexImportExpiryForNoRefreshTokenRequiresExpiry supplies an
// opaque token without renewal or expiry. It checks rejection with the
// missing-access-token-expiration diagnostic before provider scheduling
// lifetime can be selected.
func TestResolveCodexImportExpiryForNoRefreshTokenRequiresExpiry(t *testing.T) {
	item := &CodexImportProvider{
		AccessToken:  "opaque-access-token",
		Credentials:  map[string]any{"access_token": "opaque-access-token"},
		WarningTexts: []string{},
	}

	_, _, _, _, err := ResolveCodexImportExpiry(CodexSessionImportRequest{}, item, time.Now)
	if err == nil {
		t.Fatal("ResolveCodexImportExpiry error = nil, want missing expiry error")
	}
	if !strings.Contains(err.Error(), "accessToken expiry could not be parsed") {
		t.Fatalf("error = %v, want missing expiry message", err)
	}
}

// TestResolveCodexImportExpiryForNoRefreshTokenUsesEarlierRequestExpiry
// supplies a request expiry earlier than token expiry. It checks that both
// provider and credential lifetimes select the earlier configured value.
func TestResolveCodexImportExpiryForNoRefreshTokenUsesEarlierRequestExpiry(t *testing.T) {
	tokenExpiresAt := time.Now().Add(2 * time.Hour).UTC()
	requestExpiresAt := time.Now().Add(time.Hour).UTC()
	item := &CodexImportProvider{
		AccessToken:    "access-token",
		Credentials:    map[string]any{"access_token": "access-token"},
		TokenExpiresAt: &tokenExpiresAt,
		WarningTexts:   []string{},
	}
	reqUnix := requestExpiresAt.Unix()
	req := CodexSessionImportRequest{ExpiresAt: &reqUnix}

	providerExpiresAt, credentialExpiresAt, _, _, err := ResolveCodexImportExpiry(req, item, time.Now)
	if err != nil {
		t.Fatalf("ResolveCodexImportExpiry error = %v", err)
	}
	if providerExpiresAt == nil || *providerExpiresAt != requestExpiresAt.Unix() {
		t.Fatalf("provider expires_at = %v, want %d", providerExpiresAt, requestExpiresAt.Unix())
	}
	if credentialExpiresAt == nil || credentialExpiresAt.Unix() != requestExpiresAt.Unix() {
		t.Fatalf("credential expires_at = %v, want %s", credentialExpiresAt, requestExpiresAt)
	}
}

// TestCodexIdentityKeysPreferStrongIdentifiers varies renewal presence and
// account/user availability. It checks user-first/account-last ordering, email
// exclusion or fallback, and fingerprint-only matching for access-only
// credentials.
func TestCodexIdentityKeysPreferStrongIdentifiers(t *testing.T) {
	keys := BuildCodexImportIdentityKeys("acct-1", "user-1", "same@example.com", "token", "refresh")
	if len(keys) == 0 || keys[0] != "user:user-1" {
		t.Fatalf("user key should have highest priority when refresh token exists: %v", keys)
	}
	if keys[len(keys)-1] != "provider:acct-1" {
		t.Fatalf("shared provider key should be the last fallback: %v", keys)
	}
	for _, key := range keys {
		if strings.HasPrefix(key, "email:") {
			t.Fatalf("strong identity should not include email fallback: %v", keys)
		}
	}

	keys = BuildCodexImportIdentityKeys("", "", "same@example.com", "token", "refresh")
	hasEmail := false
	for _, key := range keys {
		if key == "email:same@example.com" {
			hasEmail = true
		}
	}
	if !hasEmail {
		t.Fatalf("weak identity should include email fallback: %v", keys)
	}

	keys = BuildCodexImportIdentityKeys("acct-1", "user-1", "same@example.com", "token", "")
	if len(keys) != 1 || !strings.HasPrefix(keys[0], "access:") {
		t.Fatalf("accessToken-only identity should use only access fingerprint: %v", keys)
	}
}

// TestNormalizeCodexImportUsesJWTSubForAccessTokenOnlyIdentity supplies a
// subject claim without a ChatGPT user claim. It checks the subject fills user
// metadata while access-only matching still uses only a fingerprint key.
func TestNormalizeCodexImportUsesJWTSubForAccessTokenOnlyIdentity(t *testing.T) {
	accessToken := buildCodexTestJWTForTest(t, time.Now().Add(time.Hour), map[string]any{
		"sub": "user-from-access-token",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "workspace-1",
		},
	})

	item, err := NormalizeCodexImportEntry(CodexImportEntry{Index: 1, Value: accessToken}, CodexImportOptions{Now: time.Now, OAuthClientID: "app_EMoamEEZ73f0CkXaXp7hrann"})
	if err != nil {
		t.Fatalf("NormalizeCodexImportEntry error = %v", err)
	}
	if item.UserID != "user-from-access-token" {
		t.Fatalf("UserID = %q, want JWT sub", item.UserID)
	}
	if len(item.IdentityKeys) != 1 || !strings.HasPrefix(item.IdentityKeys[0], "access:") {
		t.Fatalf("IdentityKeys = %v, want access fingerprint only for accessToken-only import", item.IdentityKeys)
	}
	if got := item.Credentials["chatgpt_user_id"]; got != "user-from-access-token" {
		t.Fatalf("credential chatgpt_user_id = %v, want JWT sub", got)
	}
}

// buildCodexTestJWTForTest creates an unsigned three-part metadata token with
// supplied expiry and overriding claims. Extra claims can replace defaults;
// marshal failure terminates the calling test, and the empty signature
// supports decode-only fixtures.
func buildCodexTestJWTForTest(t *testing.T, exp time.Time, extraClaims map[string]any) string {
	t.Helper()
	header := map[string]any{
		"alg": "none",
		"typ": "JWT",
	}
	claims := map[string]any{
		"sub": "user-from-sub",
		"exp": exp.Unix(),
		"iat": time.Now().Unix(),
	}
	maps.Copy(claims, extraClaims)
	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimBytes, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(claimBytes) + "."
}

// TestCodexImportInjectedClockAndExpiryBoundaries fixes the clock around the
// two-minute grace boundary. It checks strict JWT expiry versus inclusive
// access-only expiry, captured client ID, and Agent Identity expiry bypass
// without reading that clock.
func TestCodexImportInjectedClockAndExpiryBoundaries(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	options := CodexImportOptions{Now: func() time.Time { return now }, OAuthClientID: "fixture-client"}
	for _, delta := range []int64{-121, -120, 1} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{"exp": now.Unix() + delta})
			require.NoError(t, err)
			token := "e30." + base64.RawURLEncoding.EncodeToString(payload) + "."
			item, err := NormalizeCodexImportEntry(CodexImportEntry{Index: 1, Value: map[string]any{"access_token": token, "refresh_token": "fixture-refresh"}}, options)
			if delta < -120 {
				require.ErrorContains(t, err, "access_token has expired")
				return
			}
			require.NoError(t, err)
			require.Equal(t, "fixture-client", item.Credentials["client_id"])
			item.RefreshToken = ""
			_, _, _, _, err = ResolveCodexImportExpiry(CodexSessionImportRequest{}, item, options.Now)
			if delta <= -120 {
				require.ErrorContains(t, err, "expiry time is in the past")
			} else {
				require.NoError(t, err)
			}
		})
	}
	_, _, _, _, err := ResolveCodexImportExpiry(CodexSessionImportRequest{}, &CodexImportProvider{IsAgentIdentity: true}, func() time.Time { t.Fatal("Agent Identity 不应读取 OAuth 有效期时钟"); return now })
	require.NoError(t, err)
}

// TestCodexImportCredentialMergeIndependentNestedValues mutates a supported
// nested map after merge. It checks retained source values and nil versus
// empty slice presence for those configured copied containers.
func TestCodexImportCredentialMergeIndependentNestedValues(t *testing.T) {
	input := map[string]any{"nested": map[string]any{"values": []any{"original"}}, "nil": []any(nil), "empty": []any{}}
	out := MergeCodexImportMap(nil, input)
	nested, ok := out["nested"].(map[string]any)
	require.True(t, ok)
	nested["values"] = []any{"changed"}
	original, ok := input["nested"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []any{"original"}, original["values"])
	require.Equal(t, []any(nil), out["nil"])
	require.Equal(t, []any{}, out["empty"])
}

// TestBuildCodexAgentIdentityKeysUseChatGPTAccountOnly supplies one signing
// account identifier. It checks that the generated key list contains exactly
// that provider account key for this import mode.
func TestBuildCodexAgentIdentityKeysUseChatGPTAccountOnly(t *testing.T) {
	keys := BuildCodexAgentIdentityKeys("team-a")
	require.Equal(t, []string{"provider:team-a"}, keys)
}
