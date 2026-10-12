package identity

import (
	"errors"
	"maps"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Values shared by pending authentication flows.
const (
	OAuthCompletionResponseKey = "completion_response"
	OAuthPromoCodeStateKey     = "promo_code"
	OAuthPendingChoiceStep     = "choose_account_action_required"
	OAuthIntentLogin           = "login"
)

func PendingOAuthPromoCode(session *PendingAuthSession) string {
	if session == nil {
		return ""
	}
	return PendingSessionStringValue(session.LocalFlowState, OAuthPromoCodeStateKey)
}

func ReadCompletionResponse(session map[string]any) (map[string]any, bool) {
	if len(session) == 0 {
		return nil, false
	}
	value, ok := session[OAuthCompletionResponseKey]
	if !ok {
		return nil, false
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	return result, true
}

func ClonePendingMap(values map[string]any) map[string]any { return CopyPendingMap(values) }

func MergePendingCompletionResponse(session *PendingAuthSession, overrides map[string]any) map[string]any {
	payload, _ := ReadCompletionResponse(session.LocalFlowState)
	merged := ClonePendingMap(payload)
	if strings.TrimSpace(session.RedirectTo) != "" {
		if _, exists := merged["redirect"]; !exists {
			merged["redirect"] = session.RedirectTo
		}
	}
	for key, value := range overrides {
		if value == nil {
			delete(merged, key)
			continue
		}
		merged[key] = value
	}
	ApplySuggestedProfileToCompletionResponse(merged, session.UpstreamIdentityClaims)
	return merged
}

func PendingSessionStringValue(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	raw, ok := values[key]
	if !ok {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func PendingSessionWantsInvitation(payload map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(PendingSessionStringValue(payload, "error")), "invitation_required")
}

// PendingSessionRequiresEmailCompletion reports whether the callback requests a missing email.
// DingTalk profiles lacking an email use this step; exchange must defer adoption until completion.
func PendingSessionRequiresEmailCompletion(payload map[string]any) bool {
	if v, ok := payload["requires_email_completion"].(bool); ok && v {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(PendingSessionStringValue(payload, "step")), "email_completion")
}

// PendingSessionRequiresBindLogin reports whether the callback requires an existing account.
// DingTalk uses this step when registration and the organization exemption are disabled.
// Exchange must leave the session available for the subsequent /pending/bind-login request.
func PendingSessionRequiresBindLogin(payload map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(PendingSessionStringValue(payload, "step")), "bind_login_required")
}

func PendingOAuthCompletionCanIssueTokenPair(session *PendingAuthSession, payload map[string]any) bool {
	if session == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(session.Intent), OAuthIntentLogin) {
		return false
	}
	if session.TargetUserID == nil || *session.TargetUserID <= 0 {
		return false
	}
	if PendingSessionWantsInvitation(payload) {
		return false
	}
	return strings.TrimSpace(PendingSessionStringValue(payload, "step")) == ""
}

func EnsurePendingOAuthCompleteRegistrationSession(session *PendingAuthSession) error {
	if session == nil {
		return infraerrors.BadRequest("PENDING_AUTH_SESSION_INVALID", "pending auth registration context is invalid")
	}
	if strings.TrimSpace(session.Intent) != OAuthIntentLogin {
		return infraerrors.BadRequest("PENDING_AUTH_SESSION_INVALID", "pending auth registration context is invalid")
	}
	if session.TargetUserID != nil && *session.TargetUserID > 0 {
		return infraerrors.BadRequest("PENDING_AUTH_SESSION_INVALID", "pending auth registration context is invalid")
	}
	payload, _ := ReadCompletionResponse(session.LocalFlowState)
	if strings.EqualFold(strings.TrimSpace(PendingSessionStringValue(payload, "step")), "bind_login_required") {
		return infraerrors.BadRequest("PENDING_AUTH_SESSION_INVALID", "pending auth registration context is invalid")
	}
	return nil
}

func BuildLegacyCompleteRegistrationPendingResponse(
	session *PendingAuthSession,
	forceEmailOnSignup bool,
	emailVerificationRequired bool,
) map[string]any {
	completionResponse := NormalizePendingOAuthCompletionResponse(MergePendingCompletionResponse(session, map[string]any{
		"step":                   OAuthPendingChoiceStep,
		"adoption_required":      true,
		"create_account_allowed": true,
		"force_email_on_signup":  forceEmailOnSignup,
	}))

	if email := strings.TrimSpace(session.ResolvedEmail); email != "" {
		if _, exists := completionResponse["email"]; !exists {
			completionResponse["email"] = email
		}
		if _, exists := completionResponse["resolved_email"]; !exists {
			completionResponse["resolved_email"] = email
		}
	}
	if _, exists := completionResponse["choice_reason"]; !exists {
		switch {
		case forceEmailOnSignup:
			completionResponse["choice_reason"] = "force_email_on_signup"
		case emailVerificationRequired:
			completionResponse["choice_reason"] = "email_verification_required"
		default:
			completionResponse["choice_reason"] = "third_party_signup"
		}
	}
	return completionResponse
}

func CloneOAuthMetadata(values map[string]any) map[string]any { return CopyPendingMap(values) }

func MergeOAuthMetadata(base map[string]any, overlay map[string]any) map[string]any {
	merged := CloneOAuthMetadata(base)
	maps.Copy(merged, overlay)
	return merged
}

func NormalizeAdoptedOAuthDisplayName(value string) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > 100 {
		value = string([]rune(value)[:100])
	}
	return value
}

func OauthIdentityIssuer(session *PendingAuthSession) *string {
	if session == nil {
		return nil
	}
	switch strings.TrimSpace(session.ProviderType) {
	case "oidc":
		issuer := strings.TrimSpace(session.ProviderKey)
		if issuer == "" {
			issuer = PendingSessionStringValue(session.UpstreamIdentityClaims, "issuer")
		}
		if issuer == "" {
			return nil
		}
		return &issuer
	default:
		issuer := PendingSessionStringValue(session.UpstreamIdentityClaims, "issuer")
		if issuer == "" {
			return nil
		}
		return &issuer
	}
}

func ShouldBindPendingOAuthIdentity(session *PendingAuthSession, decision *IdentityAdoptionDecision) bool {
	if session == nil || decision == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(session.Intent)) {
	case "bind_current_user", "login", "adopt_existing_user_by_email":
		return true
	default:
		return decision.AdoptDisplayName || decision.AdoptAvatar
	}
}

func ShouldSkipAvatarAdoption(err error) bool {
	return errors.Is(err, ErrAvatarInvalid) ||
		errors.Is(err, ErrAvatarTooLarge) ||
		errors.Is(err, ErrAvatarNotImage)
}

func ApplySuggestedProfileToCompletionResponse(payload map[string]any, upstream map[string]any) {
	if len(payload) == 0 || len(upstream) == 0 {
		return
	}

	displayName := PendingSessionStringValue(upstream, "suggested_display_name")
	avatarURL := PendingSessionStringValue(upstream, "suggested_avatar_url")

	if displayName != "" {
		if _, exists := payload["suggested_display_name"]; !exists {
			payload["suggested_display_name"] = displayName
		}
	}
	if avatarURL != "" {
		if _, exists := payload["suggested_avatar_url"]; !exists {
			payload["suggested_avatar_url"] = avatarURL
		}
	}
	if displayName != "" || avatarURL != "" {
		payload["adoption_required"] = true
	}
}

func NormalizePendingOAuthCompletionResponse(payload map[string]any) map[string]any {
	normalized := ClonePendingMap(payload)
	for _, key := range []string{"access_token", "refresh_token", "expires_in", "token_type"} {
		delete(normalized, key)
	}
	step := strings.ToLower(strings.TrimSpace(PendingSessionStringValue(normalized, "step")))
	// Normalize choice aliases to OAuthPendingChoiceStep. Keep bind_login_required separate:
	// the frontend renders its needsBindLogin form for that step.
	switch step {
	case "choice", "choose_account_action", "choose_account", "choose", "email_required":
		normalized["step"] = OAuthPendingChoiceStep
	}
	if strings.EqualFold(strings.TrimSpace(PendingSessionStringValue(normalized, "step")), OAuthPendingChoiceStep) {
		normalized["adoption_required"] = true
	}
	if _, exists := normalized["adoption_required"]; !exists {
		if _, hasChoiceFields := normalized["email_binding_required"]; hasChoiceFields {
			normalized["adoption_required"] = true
		}
	}
	return normalized
}

func PendingOAuthChoiceCompletionResponse(session *PendingAuthSession, email string) map[string]any {
	response := MergePendingCompletionResponse(session, map[string]any{
		"step":                      OAuthPendingChoiceStep,
		"adoption_required":         true,
		"force_email_on_signup":     true,
		"email_binding_required":    true,
		"existing_account_bindable": true,
	})
	if email = strings.TrimSpace(email); email != "" {
		response["email"] = email
		response["resolved_email"] = email
	}
	return response
}
