package identityhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/identityadoptiondecision"
	"github.com/TokenFlux/TokenRouter/ent/pendingauthsession"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
)

// A pending browser session separates an external subject from proof that it
// may create or bind a local account. SQLite cases seed intent, target,
// suggested profile and completion data, then compare preview, finalization and
// rejected requests against the stored user, identity, decision and consumption
// fields. Existing-login exchange also checks issued token versions and removal
// of saved legacy credentials.
//
// Invitation, referral and first-bind scenarios inspect their respective
// persisted rows or in-memory observations. Subscription assertions read
// captured assignment inputs, while TOTP sessions use a borrowed-pointer map
// cache and plaintext encryptor fixture. Injected failure and
// ownership-conflict cases inspect the state left after rejection; their setup
// calls differ from a complete external OAuth login, and the choice-state
// attack case directly seeds its pending row.

// oauthPendingFlowAvatarRecord holds the two avatar columns asserted after
// profile adoption.
type oauthPendingFlowAvatarRecord struct {
	StorageProvider string // avatar storage provider
	URL             string // saved avatar URL
}

// TestApplySuggestedProfileToCompletionResponse starts with a token-bearing
// payload and supplied profile suggestions. The helper adds the display name,
// avatar URL and adoption_required=true to that response map.
func TestApplySuggestedProfileToCompletionResponse(t *testing.T) {
	payload := map[string]any{
		"access_token": "token",
	}
	upstream := map[string]any{
		"suggested_display_name": "Alice",
		"suggested_avatar_url":   "https://cdn.example/avatar.png",
	}

	identityhttp.ApplySuggestedProfileToCompletionResponse(payload, upstream)

	require.Equal(t, "Alice", payload["suggested_display_name"])
	require.Equal(t, "https://cdn.example/avatar.png", payload["suggested_avatar_url"])
	require.Equal(t, true, payload["adoption_required"])
}

// TestApplySuggestedProfileToCompletionResponseKeepsExistingPayloadValues
// supplies an existing display name and adoption_required=false. The helper
// retains that name, adds the missing avatar suggestion, and changes
// adoption_required to true.
func TestApplySuggestedProfileToCompletionResponseKeepsExistingPayloadValues(t *testing.T) {
	payload := map[string]any{
		"suggested_display_name": "Existing",
		"adoption_required":      false,
	}
	upstream := map[string]any{
		"suggested_display_name": "Alice",
		"suggested_avatar_url":   "https://cdn.example/avatar.png",
	}

	identityhttp.ApplySuggestedProfileToCompletionResponse(payload, upstream)

	require.Equal(t, "Existing", payload["suggested_display_name"])
	require.Equal(t, "https://cdn.example/avatar.png", payload["suggested_avatar_url"])
	require.Equal(t, true, payload["adoption_required"])
}

// TestSetOAuthPendingSessionCookieUsesProviderCompletionPathPrefix emits a
// pending cookie from an OIDC callback context. Its path covers
// /api/v1/auth/oauth so later provider completion requests can send the same
// browser state.
func TestSetOAuthPendingSessionCookieUsesProviderCompletionPathPrefix(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/oidc/callback", nil)

	identityhttp.SetOAuthPendingSessionCookie(ginCtx, "pending-session-token", false)

	cookie := findCookie(recorder.Result().Cookies(), identityhttp.OauthPendingSessionCookieName)
	require.NotNil(t, cookie)
	require.Equal(t, "/api/v1/auth/oauth", cookie.Path)
}

// TestExchangePendingOAuthCompletionPreviewThenFinalizeAppliesAdoptionDecision
// previews saved profile suggestions before submitting both adoption flags.
// Preview leaves username and consumption unchanged; finalization updates
// username, identity metadata, avatar and decision linkage, then consumes the
// row.
func TestExchangePendingOAuthCompletionPreviewThenFinalizeAppliesAdoptionDecision(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	ctx := context.Background()

	userEntity, err := client.User.Create().
		SetEmail("linuxdo-123@linuxdo-connect.invalid").
		SetUsername("legacy-name").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("pending-session-token").
		SetIntent("login").
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("123").
		SetTargetUserID(userEntity.ID).
		SetResolvedEmail(userEntity.Email).
		SetBrowserSessionKey("browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username":               "linuxdo_user",
			"suggested_display_name": "Alice Example",
			"suggested_avatar_url":   "https://cdn.example/alice.png",
		}).
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"access_token": "access-token",
				"redirect":     "/dashboard",
			},
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	previewRecorder := httptest.NewRecorder()
	previewCtx, _ := gin.CreateTestContext(previewRecorder)
	previewReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", nil)
	previewReq.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	previewReq.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("browser-session-key")})
	previewCtx.Request = previewReq

	handler.ExchangePendingOAuthCompletion(previewCtx)

	require.Equal(t, http.StatusOK, previewRecorder.Code)
	previewData := decodeJSONResponseData(t, previewRecorder)
	require.Equal(t, "Alice Example", previewData["suggested_display_name"])
	require.Equal(t, "https://cdn.example/alice.png", previewData["suggested_avatar_url"])
	require.Equal(t, true, previewData["adoption_required"])

	storedUser, err := client.User.Get(ctx, userEntity.ID)
	require.NoError(t, err)
	require.Equal(t, "legacy-name", storedUser.Username)

	previewSession, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.IDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.Nil(t, previewSession.ConsumedAt)

	body := bytes.NewBufferString(`{"adopt_display_name":true,"adopt_avatar":true}`)
	finalizeRecorder := httptest.NewRecorder()
	finalizeCtx, _ := gin.CreateTestContext(finalizeRecorder)
	finalizeReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", body)
	finalizeReq.Header.Set("Content-Type", "application/json")
	finalizeReq.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	finalizeReq.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("browser-session-key")})
	finalizeCtx.Request = finalizeReq

	handler.ExchangePendingOAuthCompletion(finalizeCtx)

	require.Equal(t, http.StatusOK, finalizeRecorder.Code)

	storedUser, err = client.User.Get(ctx, userEntity.ID)
	require.NoError(t, err)
	require.Equal(t, "Alice Example", storedUser.Username)

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("linuxdo"),
			authidentity.ProviderKeyEQ("linuxdo"),
			authidentity.ProviderSubjectEQ("123"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, userEntity.ID, identity.UserID)
	require.Equal(t, "Alice Example", identity.Metadata["display_name"])
	require.Equal(t, "https://cdn.example/alice.png", identity.Metadata["avatar_url"])

	avatar := loadUserAvatarRecord(t, client, userEntity.ID)
	require.NotNil(t, avatar)
	require.Equal(t, "remote_url", avatar.StorageProvider)
	require.Equal(t, "https://cdn.example/alice.png", avatar.URL)

	decision, err := client.IdentityAdoptionDecision.Query().
		Where(identityadoptiondecision.PendingAuthSessionIDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, decision.IdentityID)
	require.Equal(t, identity.ID, *decision.IdentityID)
	require.True(t, decision.AdoptDisplayName)
	require.True(t, decision.AdoptAvatar)

	consumed, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.IDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, consumed.ConsumedAt)
}

// TestExchangePendingOAuthCompletionSkipsInvalidAvatarAdoptionWithoutBlockingCompletion
// requests both profile choices with a relative avatar URL. Exchange completes
// display-name adoption and consumes the session, while identity metadata and
// avatar storage omit that invalid URL.
func TestExchangePendingOAuthCompletionSkipsInvalidAvatarAdoptionWithoutBlockingCompletion(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	ctx := context.Background()

	userEntity, err := client.User.Create().
		SetEmail("invalid-avatar@example.com").
		SetUsername("legacy-name").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("pending-invalid-avatar-token").
		SetIntent("login").
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("invalid-avatar-123").
		SetTargetUserID(userEntity.ID).
		SetResolvedEmail(userEntity.Email).
		SetBrowserSessionKey("browser-invalid-avatar-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username":               "linuxdo_user",
			"suggested_display_name": "Alice Example",
			"suggested_avatar_url":   "/avatars/alice.png",
		}).
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"access_token": "access-token",
				"redirect":     "/dashboard",
			},
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"adopt_display_name":true,"adopt_avatar":true}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("browser-invalid-avatar-key")})
	ginCtx.Request = req

	handler.ExchangePendingOAuthCompletion(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("linuxdo"),
			authidentity.ProviderKeyEQ("linuxdo"),
			authidentity.ProviderSubjectEQ("invalid-avatar-123"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, "Alice Example", identity.Metadata["display_name"])
	_, hasAdoptedAvatar := identity.Metadata["avatar_url"]
	require.False(t, hasAdoptedAvatar)

	avatar := loadUserAvatarRecord(t, client, userEntity.ID)
	require.Nil(t, avatar)

	consumed, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.IDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, consumed.ConsumedAt)
}

// TestExchangePendingOAuthCompletionBindCurrentUserPreviewThenFinalizeBindsIdentityWithoutAdoption
// previews current-user binding, then submits both flags as false. Preview
// creates no identity; finalization binds the subject with retained
// suggestions, preserves username, links false decisions and consumes pending
// state.
func TestExchangePendingOAuthCompletionBindCurrentUserPreviewThenFinalizeBindsIdentityWithoutAdoption(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	ctx := context.Background()

	userEntity, err := client.User.Create().
		SetEmail("bind-target@example.com").
		SetUsername("legacy-name").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("bind-pending-session-token").
		SetIntent("bind_current_user").
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("bind-123").
		SetTargetUserID(userEntity.ID).
		SetResolvedEmail(userEntity.Email).
		SetBrowserSessionKey("bind-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username":               "linuxdo_user",
			"suggested_display_name": "Bound Example",
			"suggested_avatar_url":   "https://cdn.example/bound.png",
		}).
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"access_token": "access-token",
				"redirect":     "/settings/profile",
			},
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	previewRecorder := httptest.NewRecorder()
	previewCtx, _ := gin.CreateTestContext(previewRecorder)
	previewReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", nil)
	previewReq.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	previewReq.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("bind-browser-session-key")})
	previewCtx.Request = previewReq

	handler.ExchangePendingOAuthCompletion(previewCtx)

	require.Equal(t, http.StatusOK, previewRecorder.Code)
	previewData := decodeJSONResponseData(t, previewRecorder)
	require.Equal(t, "Bound Example", previewData["suggested_display_name"])
	require.Equal(t, "https://cdn.example/bound.png", previewData["suggested_avatar_url"])
	require.Equal(t, true, previewData["adoption_required"])

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("linuxdo"),
			authidentity.ProviderKeyEQ("linuxdo"),
			authidentity.ProviderSubjectEQ("bind-123"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, identityCount)

	previewSession, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.IDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.Nil(t, previewSession.ConsumedAt)

	body := bytes.NewBufferString(`{"adopt_display_name":false,"adopt_avatar":false}`)
	finalizeRecorder := httptest.NewRecorder()
	finalizeCtx, _ := gin.CreateTestContext(finalizeRecorder)
	finalizeReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", body)
	finalizeReq.Header.Set("Content-Type", "application/json")
	finalizeReq.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	finalizeReq.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("bind-browser-session-key")})
	finalizeCtx.Request = finalizeReq

	handler.ExchangePendingOAuthCompletion(finalizeCtx)

	require.Equal(t, http.StatusOK, finalizeRecorder.Code)

	storedUser, err := client.User.Get(ctx, userEntity.ID)
	require.NoError(t, err)
	require.Equal(t, "legacy-name", storedUser.Username)

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("linuxdo"),
			authidentity.ProviderKeyEQ("linuxdo"),
			authidentity.ProviderSubjectEQ("bind-123"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, userEntity.ID, identity.UserID)
	require.Equal(t, "Bound Example", identity.Metadata["suggested_display_name"])
	require.Equal(t, "https://cdn.example/bound.png", identity.Metadata["suggested_avatar_url"])
	_, hasDisplayName := identity.Metadata["display_name"]
	require.False(t, hasDisplayName)
	_, hasAvatarURL := identity.Metadata["avatar_url"]
	require.False(t, hasAvatarURL)

	decision, err := client.IdentityAdoptionDecision.Query().
		Where(identityadoptiondecision.PendingAuthSessionIDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, decision.IdentityID)
	require.Equal(t, identity.ID, *decision.IdentityID)
	require.False(t, decision.AdoptDisplayName)
	require.False(t, decision.AdoptAvatar)

	consumed, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.IDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, consumed.ConsumedAt)
}

// TestExchangePendingOAuthCompletionBindCurrentUserOwnershipConflict targets a
// user while the external subject belongs to another owner. Exchange reports an
// adoption-apply error, retains that owner, leaves the decision unlinked with
// false flags, and keeps the session unconsumed.
func TestExchangePendingOAuthCompletionBindCurrentUserOwnershipConflict(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	ctx := context.Background()

	targetUser, err := client.User.Create().
		SetEmail("bind-conflict-target@example.com").
		SetUsername("target-user").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	ownerUser, err := client.User.Create().
		SetEmail("bind-conflict-owner@example.com").
		SetUsername("owner-user").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	existingIdentity, err := client.AuthIdentity.Create().
		SetUserID(ownerUser.ID).
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("conflict-123").
		SetMetadata(map[string]any{"username": "owner-user"}).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("bind-conflict-session-token").
		SetIntent("bind_current_user").
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("conflict-123").
		SetTargetUserID(targetUser.ID).
		SetResolvedEmail(targetUser.Email).
		SetBrowserSessionKey("bind-conflict-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"suggested_display_name": "Conflict Example",
			"suggested_avatar_url":   "https://cdn.example/conflict.png",
		}).
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"access_token": "access-token",
			},
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"adopt_display_name":false,"adopt_avatar":false}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("bind-conflict-browser-session-key")})
	ginCtx.Request = req

	handler.ExchangePendingOAuthCompletion(ginCtx)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	payload := decodeJSONBody(t, recorder)
	require.Equal(t, "PENDING_AUTH_ADOPTION_APPLY_FAILED", payload["reason"])

	identity, err := client.AuthIdentity.Get(ctx, existingIdentity.ID)
	require.NoError(t, err)
	require.Equal(t, ownerUser.ID, identity.UserID)

	decision, err := client.IdentityAdoptionDecision.Query().
		Where(identityadoptiondecision.PendingAuthSessionIDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.Nil(t, decision.IdentityID)
	require.False(t, decision.AdoptDisplayName)
	require.False(t, decision.AdoptAvatar)

	storedSession, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.IDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.Nil(t, storedSession.ConsumedAt)
}

// TestExchangePendingOAuthCompletionLoginFalseFalseBindsIdentityWithoutAdoption
// submits both profile flags as false for seeded login state. Exchange binds
// the selected user, records the false decisions against that identity and
// consumes the pending row.
func TestExchangePendingOAuthCompletionLoginFalseFalseBindsIdentityWithoutAdoption(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	ctx := context.Background()

	userEntity, err := client.User.Create().
		SetEmail("login-false@example.com").
		SetUsername("legacy-name").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("login-false-session-token").
		SetIntent("login").
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("login-false-123").
		SetTargetUserID(userEntity.ID).
		SetResolvedEmail(userEntity.Email).
		SetBrowserSessionKey("login-false-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"suggested_display_name": "Login Example",
			"suggested_avatar_url":   "https://cdn.example/login.png",
		}).
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"access_token": "access-token",
			},
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"adopt_display_name":false,"adopt_avatar":false}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("login-false-browser-session-key")})
	ginCtx.Request = req

	handler.ExchangePendingOAuthCompletion(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("linuxdo"),
			authidentity.ProviderKeyEQ("linuxdo"),
			authidentity.ProviderSubjectEQ("login-false-123"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, userEntity.ID, identity.UserID)

	decision, err := client.IdentityAdoptionDecision.Query().
		Where(identityadoptiondecision.PendingAuthSessionIDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, decision.IdentityID)
	require.Equal(t, identity.ID, *decision.IdentityID)
	require.False(t, decision.AdoptDisplayName)
	require.False(t, decision.AdoptAvatar)

	storedSession, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.IDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, storedSession.ConsumedAt)
}

// TestExchangePendingOAuthCompletionLoginReassignsExistingDecisionIdentityReference
// seeds an earlier decision pointing at the same identity. Finalizing a later
// session clears the earlier reference, links the current decision to that
// identity and consumes the current row.
func TestExchangePendingOAuthCompletionLoginReassignsExistingDecisionIdentityReference(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	ctx := context.Background()

	userEntity, err := client.User.Create().
		SetEmail("login-reassign@example.com").
		SetUsername("legacy-name").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	existingIdentity, err := client.AuthIdentity.Create().
		SetUserID(userEntity.ID).
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("login-reassign-123").
		SetMetadata(map[string]any{}).
		Save(ctx)
	require.NoError(t, err)

	previousSession, err := client.PendingAuthSession.Create().
		SetSessionToken("login-reassign-previous-session-token").
		SetIntent("login").
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("login-reassign-123").
		SetTargetUserID(userEntity.ID).
		SetResolvedEmail(userEntity.Email).
		SetBrowserSessionKey("login-reassign-previous-browser-session-key").
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"access_token": "previous-access-token",
			},
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	previousDecision, err := client.IdentityAdoptionDecision.Create().
		SetPendingAuthSessionID(previousSession.ID).
		SetIdentityID(existingIdentity.ID).
		SetAdoptDisplayName(true).
		SetAdoptAvatar(true).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("login-reassign-session-token").
		SetIntent("login").
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("login-reassign-123").
		SetTargetUserID(userEntity.ID).
		SetResolvedEmail(userEntity.Email).
		SetBrowserSessionKey("login-reassign-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"suggested_display_name": "Login Reassign",
			"suggested_avatar_url":   "https://cdn.example/login-reassign.png",
		}).
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"access_token": "access-token",
			},
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.IdentityAdoptionDecision.Create().
		SetPendingAuthSessionID(session.ID).
		SetAdoptDisplayName(false).
		SetAdoptAvatar(false).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"adopt_display_name":false,"adopt_avatar":false}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("login-reassign-browser-session-key")})
	ginCtx.Request = req

	handler.ExchangePendingOAuthCompletion(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)

	reloadedPrevious, err := client.IdentityAdoptionDecision.Get(ctx, previousDecision.ID)
	require.NoError(t, err)
	require.Nil(t, reloadedPrevious.IdentityID)

	currentDecision, err := client.IdentityAdoptionDecision.Query().
		Where(identityadoptiondecision.PendingAuthSessionIDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, currentDecision.IdentityID)
	require.Equal(t, existingIdentity.ID, *currentDecision.IdentityID)

	storedSession, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.IDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, storedSession.ConsumedAt)
}

// TestExchangePendingOAuthCompletionLoginWithoutDecisionStillBindsIdentity
// seeds login completion without an adoption-decision row. Exchange still
// creates the subject binding for the target user and consumes the matching
// browser session.
func TestExchangePendingOAuthCompletionLoginWithoutDecisionStillBindsIdentity(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	ctx := context.Background()

	userEntity, err := client.User.Create().
		SetEmail("login-nodecision@example.com").
		SetUsername("legacy-name").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("login-nodecision-session-token").
		SetIntent("login").
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("login-nodecision-123").
		SetTargetUserID(userEntity.ID).
		SetResolvedEmail(userEntity.Email).
		SetBrowserSessionKey("login-nodecision-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username": "login-nodecision-user",
		}).
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"access_token": "access-token",
			},
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", nil)
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("login-nodecision-browser-session-key")})
	ginCtx.Request = req

	handler.ExchangePendingOAuthCompletion(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("linuxdo"),
			authidentity.ProviderKeyEQ("linuxdo"),
			authidentity.ProviderSubjectEQ("login-nodecision-123"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, userEntity.ID, identity.UserID)

	storedSession, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.IDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, storedSession.ConsumedAt)
}

// TestExchangePendingOAuthCompletionExistingLoginWithSuggestedProfileSkipsAdoptionPrompt
// seeds an existing identity and legacy token fields in completion. Exchange
// issues different tokens with the current user version, returns suggestions
// without an adoption prompt, records one decision, and scrubs stored token
// fields.
func TestExchangePendingOAuthCompletionExistingLoginWithSuggestedProfileSkipsAdoptionPrompt(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	ctx := context.Background()

	userEntity, err := client.User.Create().
		SetEmail("existing-login@example.com").
		SetUsername("existing-login-user").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.AuthIdentity.Create().
		SetUserID(userEntity.ID).
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("existing-login-123").
		SetMetadata(map[string]any{
			"username": "existing-login-user",
		}).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("existing-login-session-token").
		SetIntent("login").
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("existing-login-123").
		SetTargetUserID(userEntity.ID).
		SetResolvedEmail(userEntity.Email).
		SetBrowserSessionKey("existing-login-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"suggested_display_name": "Existing Login Example",
			"suggested_avatar_url":   "https://cdn.example/existing-login.png",
		}).
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"access_token":  "legacy-access-token",
				"refresh_token": "legacy-refresh-token",
				"expires_in":    float64(3600),
				"token_type":    "Bearer",
				"redirect":      "/dashboard",
			},
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", nil)
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("existing-login-browser-session-key")})
	ginCtx.Request = req

	handler.ExchangePendingOAuthCompletion(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)

	payload := decodeJSONResponseData(t, recorder)
	require.NotEmpty(t, payload["access_token"])
	require.NotEmpty(t, payload["refresh_token"])
	require.NotEqual(t, "legacy-access-token", payload["access_token"])
	require.NotEqual(t, "legacy-refresh-token", payload["refresh_token"])
	require.Equal(t, "/dashboard", payload["redirect"])
	require.Equal(t, "Existing Login Example", payload["suggested_display_name"])
	require.Equal(t, "https://cdn.example/existing-login.png", payload["suggested_avatar_url"])
	require.NotContains(t, payload, "adoption_required")

	accessToken, ok := payload["access_token"].(string)
	require.True(t, ok)
	claims, err := handler.authService.ValidateToken(accessToken)
	require.NoError(t, err)
	reloadedUser, err := handler.userService.GetByID(ctx, userEntity.ID)
	require.NoError(t, err)
	require.Equal(t, reloadedUser.TokenVersion, claims.TokenVersion)

	decisionCount, err := client.IdentityAdoptionDecision.Query().
		Where(identityadoptiondecision.PendingAuthSessionIDEQ(session.ID)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, decisionCount)

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, storedSession.ConsumedAt)

	completion, ok := storedSession.LocalFlowState[identityhttp.OauthCompletionResponseKey].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, completion, "access_token")
	require.NotContains(t, completion, "refresh_token")
	require.NotContains(t, completion, "expires_in")
	require.NotContains(t, completion, "token_type")
	require.Equal(t, "/dashboard", completion["redirect"])
}

// TestExchangePendingOAuthCompletionBlocksBackendModeBeforeReturningTokenPayload
// supplies saved login state for a regular user while backend mode is enabled.
// Exchange returns HTTP 403 and leaves that pending session unconsumed.
func TestExchangePendingOAuthCompletionBlocksBackendModeBeforeReturningTokenPayload(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		settingValues: map[string]string{
			gateway.SettingKeyBackendModeEnabled: "true",
		},
	})
	ctx := context.Background()

	userEntity, err := client.User.Create().
		SetEmail("blocked@example.com").
		SetUsername("blocked-user").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("blocked-backend-mode-session-token").
		SetIntent("login").
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("blocked-subject-123").
		SetTargetUserID(userEntity.ID).
		SetResolvedEmail(userEntity.Email).
		SetBrowserSessionKey("blocked-backend-mode-browser-session-key").
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
				"expires_in":    float64(3600),
				"token_type":    "Bearer",
			},
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", nil)
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("blocked-backend-mode-browser-session-key")})
	ginCtx.Request = req

	handler.ExchangePendingOAuthCompletion(ginCtx)

	require.Equal(t, http.StatusForbidden, recorder.Code)

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.Nil(t, storedSession.ConsumedAt)
}

// TestExchangePendingOAuthCompletionRejectsDisabledTargetUser seeds login state
// targeting a disabled account with a profile suggestion. Exchange returns HTTP
// 403 and leaves the saved browser session unconsumed.
func TestExchangePendingOAuthCompletionRejectsDisabledTargetUser(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	ctx := context.Background()

	userEntity, err := client.User.Create().
		SetEmail("disabled-linked@example.com").
		SetUsername("disabled-linked-user").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusDisabled).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("disabled-linked-session-token").
		SetIntent("login").
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("disabled-linked-subject").
		SetTargetUserID(userEntity.ID).
		SetResolvedEmail(userEntity.Email).
		SetBrowserSessionKey("disabled-linked-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"suggested_display_name": "Disabled Linked User",
		}).
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"redirect": "/dashboard",
			},
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", nil)
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("disabled-linked-browser-session-key")})
	ginCtx.Request = req

	handler.ExchangePendingOAuthCompletion(ginCtx)

	require.Equal(t, http.StatusForbidden, recorder.Code)

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.Nil(t, storedSession.ConsumedAt)
}

// TestExchangePendingOAuthCompletionChoiceStateDoesNotBindIdentity directly
// seeds choice state targeting a victim with an attacker's external subject.
// Submitting adoption flags returns the choice step without a token, identity
// binding, username change or session consumption.
func TestExchangePendingOAuthCompletionChoiceStateDoesNotBindIdentity(t *testing.T) {
	// Directly seed a choice row targeting the victim to isolate exchange from the
	// earlier OAuth and create-account endpoints. Adoption flags supply no
	// password or email-code proof of that target account's ownership.
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	ctx := context.Background()

	victim, err := client.User.Create().
		SetEmail("victim@example.com").
		SetUsername("victim-user").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("choice-state-attack-session-token").
		SetIntent("login").
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("attacker-subject-123").
		SetTargetUserID(victim.ID).
		SetResolvedEmail(victim.Email).
		SetBrowserSessionKey("choice-state-attack-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username":               "attacker_linuxdo_user",
			"suggested_display_name": "Attacker Display Name",
			"suggested_avatar_url":   "https://cdn.example/attacker.png",
		}).
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"step":                      identityhttp.OauthPendingChoiceStep,
				"adoption_required":         true,
				"force_email_on_signup":     true,
				"email_binding_required":    true,
				"existing_account_bindable": true,
				"email":                     victim.Email,
				"resolved_email":            victim.Email,
				"redirect":                  "/dashboard",
			},
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"adopt_display_name":true,"adopt_avatar":true}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("choice-state-attack-browser-session-key")})
	ginCtx.Request = req

	handler.ExchangePendingOAuthCompletion(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)
	data := decodeJSONResponseData(t, recorder)
	require.NotContains(t, data, "access_token")
	require.Equal(t, identityhttp.OauthPendingChoiceStep, data["step"])

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("linuxdo"),
			authidentity.ProviderKeyEQ("linuxdo"),
			authidentity.ProviderSubjectEQ("attacker-subject-123"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, identityCount)

	storedVictim, err := client.User.Get(ctx, victim.ID)
	require.NoError(t, err)
	require.Equal(t, "victim-user", storedVictim.Username)

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.Nil(t, storedSession.ConsumedAt)
}

// TestNormalizePendingOAuthCompletionResponseScrubsLegacyTokenPayload supplies
// saved access, refresh, lifetime and token-type fields beside a redirect.
// Normalization removes those credential fields while retaining the local
// redirect value.
func TestNormalizePendingOAuthCompletionResponseScrubsLegacyTokenPayload(t *testing.T) {
	payload := identityhttp.NormalizePendingOAuthCompletionResponse(map[string]any{
		"access_token":  "legacy-access-token",
		"refresh_token": "legacy-refresh-token",
		"expires_in":    float64(3600),
		"token_type":    "Bearer",
		"redirect":      "/dashboard",
	})

	require.NotContains(t, payload, "access_token")
	require.NotContains(t, payload, "refresh_token")
	require.NotContains(t, payload, "expires_in")
	require.NotContains(t, payload, "token_type")
	require.Equal(t, "/dashboard", payload["redirect"])
}

// TestExchangePendingOAuthCompletionInvitationRequiredFalseFalsePersistsDecisionWithoutBinding
// submits false adoption flags while a seeded completion still requires
// invitation proof. Exchange retains that error, creates an unlinked false
// decision, binds no subject, and leaves the session unconsumed.
func TestExchangePendingOAuthCompletionInvitationRequiredFalseFalsePersistsDecisionWithoutBinding(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, true)
	ctx := context.Background()

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("invitation-required-session-token").
		SetIntent("login").
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("invitation-123").
		SetBrowserSessionKey("invitation-required-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"suggested_display_name": "Invite Example",
			"suggested_avatar_url":   "https://cdn.example/invite.png",
		}).
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"error": "invitation_required",
			},
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"adopt_display_name":false,"adopt_avatar":false}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("invitation-required-browser-session-key")})
	ginCtx.Request = req

	handler.ExchangePendingOAuthCompletion(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)
	data := decodeJSONResponseData(t, recorder)
	require.Equal(t, "invitation_required", data["error"])
	require.Equal(t, true, data["adoption_required"])

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("linuxdo"),
			authidentity.ProviderKeyEQ("linuxdo"),
			authidentity.ProviderSubjectEQ("invitation-123"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, identityCount)

	decision, err := client.IdentityAdoptionDecision.Query().
		Where(identityadoptiondecision.PendingAuthSessionIDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.Nil(t, decision.IdentityID)
	require.False(t, decision.AdoptDisplayName)
	require.False(t, decision.AdoptAvatar)

	storedSession, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.IDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.Nil(t, storedSession.ConsumedAt)
}

// TestCreateOIDCOAuthAccountCreatesUserBindsIdentityAndConsumesSession submits
// a seeded verification code and password for a new email. The response
// contains a Bearer token pair, the active account owns the OIDC subject, and
// the browser session is consumed.
func TestCreateOIDCOAuthAccountCreatesUserBindsIdentityAndConsumesSession(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandlerWithEmailVerification(t, false, "fresh@example.com", "246810")
	ctx := context.Background()

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("create-account-session-token").
		SetIntent("login").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-create-123").
		SetBrowserSessionKey("create-account-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username":               "oidc_user",
			"suggested_display_name": "Fresh OIDC User",
			"suggested_avatar_url":   "https://cdn.example/fresh.png",
		}).
		SetRedirectTo("/profile").
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"email":"fresh@example.com","verify_code":"246810","password":"secret-123","adopt_display_name":false,"adopt_avatar":false}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/create-account", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("create-account-browser-session-key")})
	ginCtx.Request = req

	handler.CreateOIDCOAuthAccount(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.NotEmpty(t, payload["access_token"])
	require.NotEmpty(t, payload["refresh_token"])
	require.Equal(t, "Bearer", payload["token_type"])

	createdUser, err := client.User.Query().Where(dbuser.EmailEQ("fresh@example.com")).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, billing.StatusActive, createdUser.Status)

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("oidc"),
			authidentity.ProviderKeyEQ("https://issuer.example"),
			authidentity.ProviderSubjectEQ("oidc-create-123"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, createdUser.ID, identity.UserID)

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, storedSession.ConsumedAt)
}

// TestCreateOIDCOAuthAccountExistingEmailReturnsChoicePendingSessionState
// submits an existing account's email during pending signup. It returns
// suggestions and a choice targeting that account, updates resolved email,
// creates no subject binding, and keeps pending state unconsumed.
func TestCreateOIDCOAuthAccountExistingEmailReturnsChoicePendingSessionState(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandlerWithEmailVerification(t, false, "owner@example.com", "135790")
	ctx := context.Background()

	existingUser, err := client.User.Create().
		SetEmail("owner@example.com").
		SetUsername("owner-user").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("existing-email-session-token").
		SetIntent("login").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-existing-123").
		SetBrowserSessionKey("existing-email-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username":               "oidc_user",
			"suggested_display_name": "Existing OIDC User",
			"suggested_avatar_url":   "https://cdn.example/existing.png",
		}).
		SetRedirectTo("/dashboard").
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"email":"owner@example.com","verify_code":"135790","password":"secret-123"}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/create-account", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("existing-email-browser-session-key")})
	ginCtx.Request = req

	handler.CreateOIDCOAuthAccount(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "pending_session", payload["auth_result"])
	require.Equal(t, identityhttp.OauthIntentLogin, payload["intent"])
	require.Equal(t, "oidc", payload["provider"])
	require.Equal(t, "/dashboard", payload["redirect"])
	require.Equal(t, true, payload["adoption_required"])
	require.Equal(t, identityhttp.OauthPendingChoiceStep, payload["step"])
	require.Equal(t, "owner@example.com", payload["email"])
	require.Equal(t, "Existing OIDC User", payload["suggested_display_name"])
	require.Equal(t, "https://cdn.example/existing.png", payload["suggested_avatar_url"])

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, identityhttp.OauthIntentLogin, storedSession.Intent)
	require.NotNil(t, storedSession.TargetUserID)
	require.Equal(t, existingUser.ID, *storedSession.TargetUserID)
	require.Equal(t, "owner@example.com", storedSession.ResolvedEmail)
	require.Nil(t, storedSession.ConsumedAt)

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("oidc"),
			authidentity.ProviderKeyEQ("https://issuer.example"),
			authidentity.ProviderSubjectEQ("oidc-existing-123"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, identityCount)
}

// TestCreateOIDCOAuthAccountExistingEmailNormalizesLegacySpacingAndCase matches
// a lowercase request email to a padded, mixed-case stored account. The
// returned choice and saved pending target select that account with normalized
// resolved email.
func TestCreateOIDCOAuthAccountExistingEmailNormalizesLegacySpacingAndCase(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandlerWithEmailVerification(t, false, "owner@example.com", "135790")
	ctx := context.Background()

	existingUser, err := client.User.Create().
		SetEmail(" Owner@Example.com ").
		SetUsername("owner-user").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("existing-email-normalized-session-token").
		SetIntent("login").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-existing-normalized-123").
		SetBrowserSessionKey("existing-email-normalized-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username":               "oidc_user",
			"suggested_display_name": "Existing OIDC User",
			"suggested_avatar_url":   "https://cdn.example/existing.png",
		}).
		SetRedirectTo("/dashboard").
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"email":"owner@example.com","verify_code":"135790","password":"secret-123"}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/create-account", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("existing-email-normalized-browser-session-key")})
	ginCtx.Request = req

	handler.CreateOIDCOAuthAccount(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, identityhttp.OauthIntentLogin, payload["intent"])
	require.Equal(t, identityhttp.OauthPendingChoiceStep, payload["step"])

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, storedSession.TargetUserID)
	require.Equal(t, existingUser.ID, *storedSession.TargetUserID)
	require.Equal(t, "owner@example.com", storedSession.ResolvedEmail)
}

// TestCreateOIDCOAuthAccountRejectsEmailOutsideRegistrationSuffixWhitelist
// seeds a valid code for an email excluded by the configured suffix list.
// Account creation reports EMAIL_SUFFIX_NOT_ALLOWED and leaves that email
// absent from users.
func TestCreateOIDCOAuthAccountRejectsEmailOutsideRegistrationSuffixWhitelist(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		emailVerifyEnabled: true,
		emailCache: &oauthPendingFlowEmailCacheStub{
			verificationCodes: map[string]*identitycore.VerificationCodeData{
				"foo@gmail.com": {
					Code:      "135790",
					CreatedAt: time.Now().UTC(),
					// Give mismatched attempts a future expiry for their remaining-lifetime
					// update.
					ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
				},
			},
		},
		settingValues: map[string]string{
			identitycore.SettingKeyRegistrationEmailSuffixWhitelist: `["@qq.com"]`,
		},
	})
	ctx := context.Background()

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("suffix-whitelist-session-token").
		SetIntent("login").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-suffix-whitelist-123").
		SetBrowserSessionKey("suffix-whitelist-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username": "oidc_user",
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"email":"foo@gmail.com","verify_code":"135790","password":"secret-123"}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/create-account", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("suffix-whitelist-browser-session-key")})
	ginCtx.Request = req

	handler.CreateOIDCOAuthAccount(ginCtx)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	payload := decodeJSONBody(t, recorder)
	require.Equal(t, "EMAIL_SUFFIX_NOT_ALLOWED", payload["reason"])

	count, err := client.User.Query().Where(dbuser.EmailEQ("foo@gmail.com")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

// TestSendPendingOAuthVerifyCodeExistingEmailReturnsBindLoginState submits an
// existing email to the pending verification-code endpoint. It returns a choice
// response and updates the session's target, login intent and resolved email to
// that account.
func TestSendPendingOAuthVerifyCodeExistingEmailReturnsBindLoginState(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandlerWithEmailVerification(t, false, "owner@example.com", "135790")
	ctx := context.Background()

	existingUser, err := client.User.Create().
		SetEmail("owner@example.com").
		SetUsername("owner-user").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("existing-email-send-code-session-token").
		SetIntent("login").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-existing-send-code-123").
		SetBrowserSessionKey("existing-email-send-code-browser-session-key").
		SetLocalFlowState(map[string]any{
			identityhttp.OauthCompletionResponseKey: map[string]any{
				"step": "email_required",
			},
		}).
		SetRedirectTo("/dashboard").
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"email":"owner@example.com"}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/send-verify-code", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("existing-email-send-code-browser-session-key")})
	ginCtx.Request = req

	handler.SendPendingOAuthVerifyCode(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "pending_session", payload["auth_result"])
	require.Equal(t, identityhttp.OauthPendingChoiceStep, payload["step"])
	require.Equal(t, "owner@example.com", payload["email"])

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, identityhttp.OauthIntentLogin, storedSession.Intent)
	require.NotNil(t, storedSession.TargetUserID)
	require.Equal(t, existingUser.ID, *storedSession.TargetUserID)
	require.Equal(t, "owner@example.com", storedSession.ResolvedEmail)
}

// TestCreateOIDCOAuthAccountBlocksBackendModeBeforeCreatingUser supplies a
// seeded verification code and password while backend mode is enabled. The
// request returns HTTP 403, creates no matching user, and leaves pending state
// unconsumed.
func TestCreateOIDCOAuthAccountBlocksBackendModeBeforeCreatingUser(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		emailVerifyEnabled: true,
		emailCache: &oauthPendingFlowEmailCacheStub{
			verificationCodes: map[string]*identitycore.VerificationCodeData{
				"fresh@example.com": {
					Code:      "246810",
					CreatedAt: time.Now().UTC(),
					// Give mismatched attempts a future expiry for their remaining-lifetime
					// update.
					ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
				},
			},
		},
		settingValues: map[string]string{
			gateway.SettingKeyBackendModeEnabled: "true",
		},
	})
	ctx := context.Background()

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("create-account-backend-mode-session-token").
		SetIntent("login").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-create-backend-mode-123").
		SetBrowserSessionKey("create-account-backend-mode-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username": "oidc_user",
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"email":"fresh@example.com","verify_code":"246810","password":"secret-123"}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/create-account", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("create-account-backend-mode-browser-session-key")})
	ginCtx.Request = req

	handler.CreateOIDCOAuthAccount(ginCtx)

	require.Equal(t, http.StatusForbidden, recorder.Code)

	userCount, err := client.User.Query().Where(dbuser.EmailEQ("fresh@example.com")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, userCount)

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.Nil(t, storedSession.ConsumedAt)
}

// TestLogoutClearsPendingOAuthAndBindCookies sends pending-session, browser and
// temporary bind cookies to logout. The handler succeeds and emits negative
// MaxAge for each of those three cookie names.
func TestLogoutClearsPendingOAuthAndBindCookies(t *testing.T) {
	handler, _ := newOAuthPendingFlowTestHandler(t, false)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue("pending-session-token")})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("pending-browser-key")})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthBindAccessTokenCookieName, Value: "bind-token"})
	ginCtx.Request = req

	handler.Logout(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, -1, findCookie(recorder.Result().Cookies(), identityhttp.OauthPendingSessionCookieName).MaxAge)
	require.Equal(t, -1, findCookie(recorder.Result().Cookies(), identityhttp.OauthPendingBrowserCookieName).MaxAge)
	require.Equal(t, -1, findCookie(recorder.Result().Cookies(), identityhttp.OauthBindAccessTokenCookieName).MaxAge)
}

// TestCreateOIDCOAuthAccountRollsBackCreatedUserWhenBindingFails seeds a
// subject already owned by another account, plus invitation and referral
// inputs. Rejection leaves the requested account absent, inviter count
// unchanged, invitation unused and pending state unconsumed.
func TestCreateOIDCOAuthAccountRollsBackCreatedUserWhenBindingFails(t *testing.T) {
	affiliateRepo := newOAuthPendingFlowAffiliateRepo()
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		invitationEnabled:  true,
		emailVerifyEnabled: true,
		emailCache: &oauthPendingFlowEmailCacheStub{
			verificationCodes: map[string]*identitycore.VerificationCodeData{
				"fresh@example.com": {
					Code:      "246810",
					CreatedAt: time.Now().UTC(),
					// Give mismatched attempts a future expiry for their remaining-lifetime
					// update.
					ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
				},
			},
		},
		settingValues: map[string]string{
			promotion.SettingKeyAffiliateEnabled: "true",
		},
		affiliateRepo: affiliateRepo,
	})
	ctx := context.Background()

	inviter, err := client.User.Create().
		SetEmail("inviter@example.com").
		SetUsername("inviter").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	affiliateRepo.setCode(t, inviter.ID, "AFFROLL")

	conflictOwner, err := client.User.Create().
		SetEmail("owner@example.com").
		SetUsername("owner-user").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.AuthIdentity.Create().
		SetUserID(conflictOwner.ID).
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-conflict-123").
		SetMetadata(map[string]any{
			"username": "owner-user",
		}).
		Save(ctx)
	require.NoError(t, err)

	invitation, err := client.RedeemCode.Create().
		SetCode("INVITE123").
		SetType(billing.RedeemTypeInvitation).
		SetStatus(billing.StatusUnused).
		SetValue(0).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("create-account-conflict-session-token").
		SetIntent("login").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-conflict-123").
		SetBrowserSessionKey("create-account-conflict-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username": "oidc_user",
		}).
		SetRedirectTo("/profile").
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"email":"fresh@example.com","verify_code":"246810","password":"secret-123","invitation_code":"INVITE123","aff_code":"AFFROLL"}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/create-account", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("create-account-conflict-browser-session-key")})
	ginCtx.Request = req

	handler.CreateOIDCOAuthAccount(ginCtx)

	require.Equal(t, http.StatusConflict, recorder.Code)

	userCount, err := client.User.Query().Where(dbuser.EmailEQ("fresh@example.com")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, userCount)
	require.Equal(t, 0, affiliateRepo.affCountFor(t, inviter.ID))

	storedInvitation, err := client.RedeemCode.Get(ctx, invitation.ID)
	require.NoError(t, err)
	require.Equal(t, billing.StatusUnused, storedInvitation.Status)
	require.Nil(t, storedInvitation.UsedBy)
	require.Nil(t, storedInvitation.UsedAt)

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.Nil(t, storedSession.ConsumedAt)
}

// TestCreateOIDCOAuthAccountRollsBackPostBindFailureBeforeIdentityCanCommit
// injects a failure after binding with deletion guarded against remaining
// identities. The HTTP error leaves both the requested user and subject binding
// absent, while the pending row remains unconsumed.
func TestCreateOIDCOAuthAccountRollsBackPostBindFailureBeforeIdentityCanCommit(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		emailVerifyEnabled: true,
		emailCache: &oauthPendingFlowEmailCacheStub{
			verificationCodes: map[string]*identitycore.VerificationCodeData{
				"fresh@example.com": {
					Code:      "246810",
					CreatedAt: time.Now().UTC(),
					// Give mismatched attempts a future expiry for their remaining-lifetime
					// update.
					ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
				},
			},
		},
		userRepoOptions: oauthPendingFlowUserRepoOptions{
			rejectDeleteWhileAuthIdentityExists: true,
		},
	})
	ctx := context.Background()

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("create-account-finalize-failure-session-token").
		SetIntent("login").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-finalize-failure-123").
		SetBrowserSessionKey("create-account-finalize-failure-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username": "oidc_user",
		}).
		SetRedirectTo("/profile").
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	// Fail the account flow after binding through its package-global hook, then
	// reset the hook at cleanup so subsequent fixtures do not inherit that
	// failure.
	pendingOAuthCreateAccountPreCommitHook = func(context.Context, *dbent.PendingAuthSession) error {
		return errors.New("forced post-bind failure")
	}
	t.Cleanup(func() {
		pendingOAuthCreateAccountPreCommitHook = nil
	})

	body := bytes.NewBufferString(`{"email":"fresh@example.com","verify_code":"246810","password":"secret-123"}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/create-account", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("create-account-finalize-failure-browser-session-key")})
	ginCtx.Request = req

	handler.CreateOIDCOAuthAccount(ginCtx)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)

	userCount, err := client.User.Query().Where(dbuser.EmailEQ("fresh@example.com")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, userCount)

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("oidc"),
			authidentity.ProviderKeyEQ("https://issuer.example"),
			authidentity.ProviderSubjectEQ("oidc-finalize-failure-123"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, identityCount)

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.Nil(t, storedSession.ConsumedAt)
}

// TestBindOIDCOAuthLoginBindsExistingUserAndConsumesSession submits the
// selected account's password against its seeded hash for account adoption. It
// receives a Bearer token pair, assigns the OIDC subject to that account and
// consumes the session.
func TestBindOIDCOAuthLoginBindsExistingUserAndConsumesSession(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	ctx := context.Background()

	passwordHash, err := handler.authService.HashPassword("secret-123")
	require.NoError(t, err)

	existingUser, err := client.User.Create().
		SetEmail("owner@example.com").
		SetUsername("owner-user").
		SetPasswordHash(passwordHash).
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("bind-login-session-token").
		SetIntent("adopt_existing_user_by_email").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-bind-123").
		SetTargetUserID(existingUser.ID).
		SetResolvedEmail(existingUser.Email).
		SetBrowserSessionKey("bind-login-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username":               "oidc_user",
			"suggested_display_name": "Bound OIDC User",
			"suggested_avatar_url":   "https://cdn.example/bound.png",
		}).
		SetRedirectTo("/profile").
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"email":"owner@example.com","password":"secret-123","adopt_display_name":false,"adopt_avatar":false}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/bind-login", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("bind-login-browser-session-key")})
	ginCtx.Request = req

	handler.BindOIDCOAuthLogin(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.NotEmpty(t, payload["access_token"])
	require.NotEmpty(t, payload["refresh_token"])
	require.Equal(t, "Bearer", payload["token_type"])

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("oidc"),
			authidentity.ProviderKeyEQ("https://issuer.example"),
			authidentity.ProviderSubjectEQ("oidc-bind-123"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, existingUser.ID, identity.UserID)

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, storedSession.ConsumedAt)
}

// TestBindOIDCOAuthLoginBlocksBackendModeBeforeTokenIssue submits a valid
// password for a regular account while backend mode is enabled. HTTP 403 leaves
// the OIDC subject unbound and the pending row unconsumed.
func TestBindOIDCOAuthLoginBlocksBackendModeBeforeTokenIssue(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		settingValues: map[string]string{
			gateway.SettingKeyBackendModeEnabled: "true",
		},
	})
	ctx := context.Background()

	passwordHash, err := handler.authService.HashPassword("secret-123")
	require.NoError(t, err)

	existingUser, err := client.User.Create().
		SetEmail("owner@example.com").
		SetUsername("owner-user").
		SetPasswordHash(passwordHash).
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("bind-login-backend-mode-session-token").
		SetIntent("adopt_existing_user_by_email").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-bind-backend-mode-123").
		SetTargetUserID(existingUser.ID).
		SetResolvedEmail(existingUser.Email).
		SetBrowserSessionKey("bind-login-backend-mode-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username": "oidc_user",
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"email":"owner@example.com","password":"secret-123"}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/bind-login", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("bind-login-backend-mode-browser-session-key")})
	ginCtx.Request = req

	handler.BindOIDCOAuthLogin(ginCtx)

	require.Equal(t, http.StatusForbidden, recorder.Code)

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("oidc"),
			authidentity.ProviderKeyEQ("https://issuer.example"),
			authidentity.ProviderSubjectEQ("oidc-bind-backend-mode-123"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, identityCount)

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.Nil(t, storedSession.ConsumedAt)
}

// TestBindOIDCOAuthLoginRejectsInvalidPasswordWithoutConsumingSession supplies
// the selected account's email with a wrong password. The handler reports
// INVALID_CREDENTIALS with HTTP 401, creates no subject binding, and leaves
// pending state unconsumed.
func TestBindOIDCOAuthLoginRejectsInvalidPasswordWithoutConsumingSession(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	ctx := context.Background()

	passwordHash, err := handler.authService.HashPassword("secret-123")
	require.NoError(t, err)

	existingUser, err := client.User.Create().
		SetEmail("owner@example.com").
		SetUsername("owner-user").
		SetPasswordHash(passwordHash).
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("bind-login-invalid-password-session-token").
		SetIntent("adopt_existing_user_by_email").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-bind-invalid-123").
		SetTargetUserID(existingUser.ID).
		SetResolvedEmail(existingUser.Email).
		SetBrowserSessionKey("bind-login-invalid-password-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username":               "oidc_user",
			"suggested_display_name": "Bound OIDC User",
			"suggested_avatar_url":   "https://cdn.example/bound.png",
		}).
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"email":"owner@example.com","password":"wrong-password"}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/bind-login", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("bind-login-invalid-password-browser-session-key")})
	ginCtx.Request = req

	handler.BindOIDCOAuthLogin(ginCtx)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
	payload := decodeJSONBody(t, recorder)
	require.Equal(t, "INVALID_CREDENTIALS", payload["reason"])

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("oidc"),
			authidentity.ProviderKeyEQ("https://issuer.example"),
			authidentity.ProviderSubjectEQ("oidc-bind-invalid-123"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, identityCount)

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.Nil(t, storedSession.ConsumedAt)
}

// TestBindOIDCOAuthLoginReclaimsIdentityOwnedBySoftDeletedUser deletes the
// earlier owner before submitting the new target's valid password. Successful
// binding changes the retained identity record's owner to that authenticated
// target.
func TestBindOIDCOAuthLoginReclaimsIdentityOwnedBySoftDeletedUser(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	ctx := context.Background()

	oldOwnerHash, err := handler.authService.HashPassword("old-secret")
	require.NoError(t, err)
	oldOwner, err := client.User.Create().
		SetEmail("old-owner@example.com").
		SetUsername("old-owner").
		SetPasswordHash(oldOwnerHash).
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	identity, err := client.AuthIdentity.Create().
		SetUserID(oldOwner.ID).
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-bind-soft-deleted-123").
		SetMetadata(map[string]any{"username": "old-owner"}).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.User.Delete().Where(dbuser.IDEQ(oldOwner.ID)).Exec(ctx)
	require.NoError(t, err)

	newOwnerHash, err := handler.authService.HashPassword("secret-123")
	require.NoError(t, err)
	newOwner, err := client.User.Create().
		SetEmail("owner@example.com").
		SetUsername("owner-user").
		SetPasswordHash(newOwnerHash).
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("bind-login-soft-deleted-owner-session-token").
		SetIntent("adopt_existing_user_by_email").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-bind-soft-deleted-123").
		SetTargetUserID(newOwner.ID).
		SetResolvedEmail(newOwner.Email).
		SetBrowserSessionKey("bind-login-soft-deleted-owner-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"username":               "oidc_user",
			"suggested_display_name": "Recovered OIDC User",
		}).
		SetRedirectTo("/profile").
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"email":"owner@example.com","password":"secret-123","adopt_display_name":false,"adopt_avatar":false}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/bind-login", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("bind-login-soft-deleted-owner-browser-session-key")})
	ginCtx.Request = req

	handler.BindOIDCOAuthLogin(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)

	identity, err = client.AuthIdentity.Get(ctx, identity.ID)
	require.NoError(t, err)
	require.Equal(t, newOwner.ID, identity.UserID)
}

// TestBindOIDCOAuthLoginAppliesFirstBindGrantOnce binds two different OIDC
// subjects to the same account sequentially. Balance, concurrency and the grant
// row reflect one award, while the subscription fixture captures exactly one
// assignment input for that user and plan.
func TestBindOIDCOAuthLoginAppliesFirstBindGrantOnce(t *testing.T) {
	defaultSubAssigner := &oauthPendingFlowDefaultSubAssignerStub{}
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		settingValues: map[string]string{
			identitycore.SettingKeyAuthSourceDefaultOIDCBalance:          "12.5",
			identitycore.SettingKeyAuthSourceDefaultOIDCConcurrency:      "3",
			identitycore.SettingKeyAuthSourceDefaultOIDCSubscriptions:    `[{"plan_id":101}]`,
			identitycore.SettingKeyAuthSourceDefaultOIDCGrantOnFirstBind: "true",
		},
		defaultSubAssigner: defaultSubAssigner,
	})
	ctx := context.Background()

	passwordHash, err := handler.authService.HashPassword("secret-123")
	require.NoError(t, err)

	existingUser, err := client.User.Create().
		SetEmail("owner@example.com").
		SetUsername("owner-user").
		SetPasswordHash(passwordHash).
		SetBalance(5).
		SetConcurrency(2).
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	firstSession, err := client.PendingAuthSession.Create().
		SetSessionToken("first-bind-session-token").
		SetIntent("adopt_existing_user_by_email").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-bind-first-123").
		SetTargetUserID(existingUser.ID).
		SetResolvedEmail(existingUser.Email).
		SetBrowserSessionKey("first-bind-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"suggested_display_name": "Bound OIDC User",
			"suggested_avatar_url":   "https://cdn.example/bound.png",
		}).
		SetRedirectTo("/profile").
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	firstBody := bytes.NewBufferString(`{"email":"owner@example.com","password":"secret-123","adopt_display_name":false,"adopt_avatar":false}`)
	firstRecorder := httptest.NewRecorder()
	firstGinCtx, _ := gin.CreateTestContext(firstRecorder)
	firstReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/bind-login", firstBody)
	firstReq.Header.Set("Content-Type", "application/json")
	firstReq.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(firstSession.SessionToken)})
	firstReq.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("first-bind-browser-session-key")})
	firstGinCtx.Request = firstReq

	handler.BindOIDCOAuthLogin(firstGinCtx)

	require.Equal(t, http.StatusOK, firstRecorder.Code)

	storedUser, err := client.User.Get(ctx, existingUser.ID)
	require.NoError(t, err)
	require.Equal(t, 17.5, storedUser.Balance)
	require.Equal(t, 5, storedUser.Concurrency)
	require.Zero(t, storedUser.TotalRecharged)
	require.Len(t, defaultSubAssigner.calls, 1)
	require.Equal(t, existingUser.ID, defaultSubAssigner.calls[0].UserID)
	require.Equal(t, int64(101), defaultSubAssigner.calls[0].PlanID)
	require.Equal(t, 1, countProviderGrantRecords(t, client, existingUser.ID, "oidc", "first_bind"))

	secondSession, err := client.PendingAuthSession.Create().
		SetSessionToken("second-bind-session-token").
		SetIntent("adopt_existing_user_by_email").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-bind-second-456").
		SetTargetUserID(existingUser.ID).
		SetResolvedEmail(existingUser.Email).
		SetBrowserSessionKey("second-bind-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"suggested_display_name": "Second OIDC User",
			"suggested_avatar_url":   "https://cdn.example/second.png",
		}).
		SetRedirectTo("/profile").
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	secondBody := bytes.NewBufferString(`{"email":"owner@example.com","password":"secret-123","adopt_display_name":false,"adopt_avatar":false}`)
	secondRecorder := httptest.NewRecorder()
	secondGinCtx, _ := gin.CreateTestContext(secondRecorder)
	secondReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/bind-login", secondBody)
	secondReq.Header.Set("Content-Type", "application/json")
	secondReq.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(secondSession.SessionToken)})
	secondReq.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("second-bind-browser-session-key")})
	secondGinCtx.Request = secondReq

	handler.BindOIDCOAuthLogin(secondGinCtx)

	require.Equal(t, http.StatusOK, secondRecorder.Code)

	storedUser, err = client.User.Get(ctx, existingUser.ID)
	require.NoError(t, err)
	require.Equal(t, 17.5, storedUser.Balance)
	require.Equal(t, 5, storedUser.Concurrency)
	require.Zero(t, storedUser.TotalRecharged)
	require.Len(t, defaultSubAssigner.calls, 1)
	require.Equal(t, 1, countProviderGrantRecords(t, client, existingUser.ID, "oidc", "first_bind"))
}

// TestResolvePendingOAuthTargetUserIDNormalizesLegacySpacingAndCase supplies
// normalized resolved email beside a padded, mixed-case stored account. Target
// resolution returns that existing account's ID from the saved pending session.
func TestResolvePendingOAuthTargetUserIDNormalizesLegacySpacingAndCase(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandler(t, false)
	_ = handler
	ctx := context.Background()

	existingUser, err := client.User.Create().
		SetEmail(" Owner@Example.com ").
		SetUsername("owner-user").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("resolve-target-session-token").
		SetIntent("login").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-target-123").
		SetResolvedEmail("owner@example.com").
		SetBrowserSessionKey("resolve-target-browser-session-key").
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	resolvedUserID, err := identitypostgres.ResolvePendingOAuthTargetUserID(ctx, client, session)
	require.NoError(t, err)
	require.Equal(t, existingUser.ID, resolvedUserID)
}

// TestBindOIDCOAuthLoginReturns2FAChallengeWhenUserHasTotp submits a valid
// password for a TOTP-enabled account. It receives masked-email challenge data
// and a cached pending-bind reference, while the identity remains absent and
// browser state unconsumed.
func TestBindOIDCOAuthLoginReturns2FAChallengeWhenUserHasTotp(t *testing.T) {
	totpCache := &oauthPendingFlowTotpCacheStub{}
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		settingValues: map[string]string{
			identitycore.SettingKeyTotpEnabled: "true",
		},
		totpCache:     totpCache,
		totpEncryptor: oauthPendingFlowTotpEncryptorStub{},
	})
	ctx := context.Background()

	passwordHash, err := handler.authService.HashPassword("secret-123")
	require.NoError(t, err)
	// Supply prior enablement for an account already configured with TOTP.
	totpEnabledAt := time.Now().UTC().Add(-time.Hour)
	secret := "JBSWY3DPEHPK3PXP"

	existingUser, err := client.User.Create().
		SetEmail("owner@example.com").
		SetUsername("owner-user").
		SetPasswordHash(passwordHash).
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		SetTotpEnabled(true).
		SetTotpSecretEncrypted(secret).
		SetTotpEnabledAt(totpEnabledAt).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("bind-login-2fa-session-token").
		SetIntent("adopt_existing_user_by_email").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-bind-2fa-123").
		SetTargetUserID(existingUser.ID).
		SetResolvedEmail(existingUser.Email).
		SetBrowserSessionKey("bind-login-2fa-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"suggested_display_name": "Bound OIDC User",
			"suggested_avatar_url":   "https://cdn.example/bound.png",
		}).
		SetRedirectTo("/profile").
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"email":"owner@example.com","password":"secret-123","adopt_display_name":false,"adopt_avatar":false}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/oidc/bind-login", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue("bind-login-2fa-browser-session-key")})
	ginCtx.Request = req

	handler.BindOIDCOAuthLogin(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)
	data := decodeJSONResponseData(t, recorder)
	require.Equal(t, true, data["requires_2fa"])
	require.Equal(t, "o***r@example.com", data["user_email_masked"])
	tempToken, ok := data["temp_token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, tempToken)

	loginSession, err := totpCache.GetLoginSession(ctx, tempToken)
	require.NoError(t, err)
	require.NotNil(t, loginSession)
	require.NotNil(t, loginSession.PendingOAuthBind)
	require.Equal(t, session.SessionToken, loginSession.PendingOAuthBind.PendingSessionToken)
	require.Equal(t, session.BrowserSessionKey, loginSession.PendingOAuthBind.BrowserSessionKey)

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("oidc"),
			authidentity.ProviderKeyEQ("https://issuer.example"),
			authidentity.ProviderSubjectEQ("oidc-bind-2fa-123"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, identityCount)

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.Nil(t, storedSession.ConsumedAt)
}

// TestLogin2FACompletesPendingOAuthBindAndConsumesSession submits a generated
// TOTP code for a seeded pending-bind challenge. It issues tokens with the
// current version, binds the subject, consumes browser and temporary cache
// state, and checks one balance/concurrency award with no subscription call.
func TestLogin2FACompletesPendingOAuthBindAndConsumesSession(t *testing.T) {
	totpCache := &oauthPendingFlowTotpCacheStub{}
	defaultSubAssigner := &oauthPendingFlowDefaultSubAssignerStub{}
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		settingValues: map[string]string{
			identitycore.SettingKeyTotpEnabled:                           "true",
			identitycore.SettingKeyAuthSourceDefaultOIDCBalance:          "8",
			identitycore.SettingKeyAuthSourceDefaultOIDCConcurrency:      "2",
			identitycore.SettingKeyAuthSourceDefaultOIDCGrantOnFirstBind: "true",
		},
		defaultSubAssigner: defaultSubAssigner,
		totpCache:          totpCache,
		totpEncryptor:      oauthPendingFlowTotpEncryptorStub{},
	})
	ctx := context.Background()

	passwordHash, err := handler.authService.HashPassword("secret-123")
	require.NoError(t, err)
	// Supply prior enablement for an account already configured with TOTP.
	totpEnabledAt := time.Now().UTC().Add(-time.Hour)
	secret := "JBSWY3DPEHPK3PXP"

	existingUser, err := client.User.Create().
		SetEmail("owner@example.com").
		SetUsername("owner-user").
		SetPasswordHash(passwordHash).
		SetBalance(1.5).
		SetConcurrency(4).
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		SetTotpEnabled(true).
		SetTotpSecretEncrypted(secret).
		SetTotpEnabledAt(totpEnabledAt).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("login-2fa-pending-session-token").
		SetIntent("adopt_existing_user_by_email").
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("oidc-login-2fa-123").
		SetTargetUserID(existingUser.ID).
		SetResolvedEmail(existingUser.Email).
		SetBrowserSessionKey("login-2fa-browser-session-key").
		SetUpstreamIdentityClaims(map[string]any{
			"suggested_display_name": "Bound OIDC User",
			"suggested_avatar_url":   "https://cdn.example/bound.png",
		}).
		SetRedirectTo("/profile").
		// Seed a future expiry for the following completion-state request.
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.IdentityAdoptionDecision.Create().
		SetPendingAuthSessionID(session.ID).
		SetAdoptDisplayName(false).
		SetAdoptAvatar(false).
		Save(ctx)
	require.NoError(t, err)

	tempToken, err := handler.totpService.CreatePendingOAuthBindLoginSession(
		ctx,
		existingUser.ID,
		existingUser.Email,
		session.SessionToken,
		session.BrowserSessionKey,
	)
	require.NoError(t, err)

	code, err := totp.GenerateCode(secret, time.Now().UTC())
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"temp_token":"` + tempToken + `","totp_code":"` + code + `"}`)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/2fa", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingSessionCookieName, Value: identityhttp.EncodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: identityhttp.OauthPendingBrowserCookieName, Value: identityhttp.EncodeCookieValue(session.BrowserSessionKey)})
	ginCtx.Request = req

	handler.Login2FA(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)
	payload := decodeJSONResponseData(t, recorder)
	require.NotEmpty(t, payload["access_token"])
	require.NotEmpty(t, payload["refresh_token"])
	accessToken, ok := payload["access_token"].(string)
	require.True(t, ok)
	claims, err := handler.authService.ValidateToken(accessToken)
	require.NoError(t, err)
	reloadedUser, err := handler.userService.GetByID(ctx, existingUser.ID)
	require.NoError(t, err)
	require.Equal(t, reloadedUser.TokenVersion, claims.TokenVersion)

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("oidc"),
			authidentity.ProviderKeyEQ("https://issuer.example"),
			authidentity.ProviderSubjectEQ("oidc-login-2fa-123"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, existingUser.ID, identity.UserID)

	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, storedSession.ConsumedAt)

	loginSession, err := totpCache.GetLoginSession(ctx, tempToken)
	require.NoError(t, err)
	require.Nil(t, loginSession)

	storedUser, err := client.User.Get(ctx, existingUser.ID)
	require.NoError(t, err)
	require.Equal(t, 9.5, storedUser.Balance)
	require.Equal(t, 6, storedUser.Concurrency)
	require.Equal(t, 1, countProviderGrantRecords(t, client, existingUser.ID, "oidc", "first_bind"))
	require.Empty(t, defaultSubAssigner.calls)
}

// loadUserAvatarRecord reads the first matching SQLite avatar row through the
// retained client and fails t on query or scan errors. A missing row returns
// nil; cleanup closes rows while discarding its close error.
func loadUserAvatarRecord(t *testing.T, client *dbent.Client, userID int64) *oauthPendingFlowAvatarRecord {
	t.Helper()

	var rows entsql.Rows
	err := client.Driver().Query(
		context.Background(),
		`SELECT storage_provider, url FROM user_avatars WHERE user_id = ?`,
		[]any{userID},
		&rows,
	)
	require.NoError(t, err)
	// Release the result set after inspection. Close errors are discarded here;
	// query, scan and the listed row assertions determine the helper result.
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		require.NoError(t, rows.Err())
		return nil
	}

	var record oauthPendingFlowAvatarRecord
	require.NoError(t, rows.Scan(&record.StorageProvider, &record.URL))
	require.NoError(t, rows.Err())
	return &record
}

// countProviderGrantRecords returns the SQLite count for the supplied user,
// provider and reason, failing t on query or scan errors. It requires one
// result row and no second row; the final iteration does not check rows.Err.
func countProviderGrantRecords(
	t *testing.T,
	client *dbent.Client,
	userID int64,
	providerType string,
	grantReason string,
) int {
	t.Helper()

	var rows entsql.Rows
	err := client.Driver().Query(
		context.Background(),
		`SELECT COUNT(*) FROM user_provider_default_grants WHERE user_id = ? AND provider_type = ? AND grant_reason = ?`,
		[]any{userID, providerType, grantReason},
		&rows,
	)
	require.NoError(t, err)
	// Release the result set after inspection. Close errors are discarded here;
	// query, scan and the listed row assertions determine the helper result.
	defer func() { _ = rows.Close() }()

	require.True(t, rows.Next())
	var count int
	require.NoError(t, rows.Scan(&count))
	require.False(t, rows.Next())
	return count
}
