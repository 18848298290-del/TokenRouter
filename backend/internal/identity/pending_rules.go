package identity

import (
	"errors"
	"maps"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Pending browser responses carry account-choice steps beside stored provider
// hints and selected account identifiers. A payload can contain a frontend
// step without an issuable account target, while a completed login can still
// require profile adoption based on upstream hints.
//
// The helpers apply explicit overrides during merging and distinguish missing
// fields from present empty, false or unsupported values.
// Normalization removes immediate credentials and canonicalizes selected
// choice aliases while retaining the binding-login step. Those
// transformations remain shallow: nested values are shared, and eligibility
// checks do not reserve sessions or validate every account/lifetime
// condition. Storage and authentication entry points complete those decisions
// after interpreting this response state.

// These keys and labels identify stored completion/promotion values and the
// frontend account-choice and login states interpreted by pending flow helpers.
const (
	OAuthCompletionResponseKey = "completion_response"            // stored browser completion payload
	OAuthPromoCodeStateKey     = "promo_code"                     // stored signup promotion value
	OAuthPendingChoiceStep     = "choose_account_action_required" // canonical account-choice frontend step
	OAuthIntentLogin           = "login"                          // pending account login intent
)

// PendingOAuthPromoCode returns the trimmed string promotion stored in local
// pending state. Nil sessions, absent keys and unsupported value types
// produce empty text; this read performs no promotion validation or
// application.
func PendingOAuthPromoCode(session *PendingAuthSession) string {
	if session == nil {
		return ""
	}
	return PendingSessionStringValue(session.LocalFlowState, OAuthPromoCodeStateKey)
}

// ReadCompletionResponse returns the stored map[string]any completion value
// and whether that type is present. Missing or differently typed entries
// return nil,false; a typed nil map returns nil,true, and a returned nonnil
// map remains shared.
func ReadCompletionResponse(session map[string]any) (map[string]any, bool) {
	if len(session) == 0 {
		return nil, false
	}
	value, ok := session[OAuthCompletionResponseKey]
	if !ok {
		return nil, false
	}
	// Unsupported completion shapes supply no fields to the merge helpers; keep
	// them absent instead of coercing values into an unverified protocol map.
	result, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	return result, true
}

// ClonePendingMap delegates an outer-map copy for pending values. Nil and
// empty input become allocated empty maps; nested mutable values retain
// sharing, so callers copy those values separately before independent
// changes.
func ClonePendingMap(values map[string]any) map[string]any { return CopyPendingMap(values) }

// MergePendingCompletionResponse constructs an outer copy of a nonnil
// session's completion map and applies caller overrides. A nonblank session
// redirect fills only a missing key, and retains its original text; nil
// override values delete keys rather than storing null.
//
// After overrides, suggested profile hints can fill absent fields and force
// adoption_required when upstream hints exist. Existing payload keys, even
// empty or nil values, block those hint insertions. Nested objects stay
// shared, and this merge does not remove token fields; callers use
// NormalizePendingOAuthCompletionResponse when preparing a credential-free
// response.
func MergePendingCompletionResponse(session *PendingAuthSession, overrides map[string]any) map[string]any {
	// An absent or unsupported completion representation contributes an empty
	// working map; the session's redirect and caller overrides can still
	// establish frontend progression state.
	payload, _ := ReadCompletionResponse(session.LocalFlowState)
	merged := ClonePendingMap(payload)
	// The session redirect fills absence only, so an existing payload redirect,
	// including nil or empty text, retains the earlier caller selection.
	if strings.TrimSpace(session.RedirectTo) != "" {
		if _, exists := merged["redirect"]; !exists {
			merged["redirect"] = session.RedirectTo
		}
	}
	// Treat nil overrides as removal instructions, preserving the completion
	// API's omission semantics instead of publishing null values that would
	// block later missing-key fallbacks.
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

// PendingSessionStringValue returns trimmed text only for a stored string
// value. Nil/empty maps, missing keys and other value types return empty
// text; it performs no numeric, boolean or fmt.Stringer conversion.
func PendingSessionStringValue(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	raw, ok := values[key]
	if !ok {
		return ""
	}
	// Pending protocol text uses stored strings; unsupported representations
	// read as absent rather than becoming numeric or structured text that later
	// step predicates could recognize.
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// PendingSessionWantsInvitation recognizes invitation_required in the
// payload's string error value after trimming and case folding. Missing or
// unsupported error values return false, leaving the caller to interpret its
// other completion steps.
func PendingSessionWantsInvitation(payload map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(PendingSessionStringValue(payload, "error")), "invitation_required")
}

// PendingSessionRequiresEmailCompletion recognizes an explicit true
// requires_email_completion flag or a string email_completion step. Other
// flag types and false values fall through to the step check; callers defer
// adoption while completing that requested local email flow.
func PendingSessionRequiresEmailCompletion(payload map[string]any) bool {
	if v, ok := payload["requires_email_completion"].(bool); ok && v {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(PendingSessionStringValue(payload, "step")), "email_completion")
}

// PendingSessionRequiresBindLogin recognizes the bind_login_required string
// step after trimming and case folding. The caller keeps the pending session
// available for existing-account credential proof; this predicate does not
// consume it or enable account creation.
func PendingSessionRequiresBindLogin(payload map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(PendingSessionStringValue(payload, "step")), "bind_login_required")
}

// PendingOAuthCompletionCanIssueTokenPair checks whether the observed
// completion can proceed from login to token issuance. It requires a
// case-insensitive trimmed login intent, a positive account target, no
// invitation marker and no nonblank string step.
//
// A missing or unsupported step value reads as empty, so it does not block
// this predicate. Lifetime, browser binding, current account activity and
// actual credential issuance are checked by surrounding entry points. This
// helper reserves no session or user state; callers still complete those
// checks and handle later storage or token-generation failures.
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

// EnsurePendingOAuthCompleteRegistrationSession accepts a pending
// registration context or returns its input error. It requires a nonnil
// session, exact trimmed login intent, no positive target account and no
// bind_login_required step; nil or nonpositive target pointers remain
// permissible.
//
// The intent comparison is case-sensitive, while the rejected bind step is
// case-insensitive. Other steps and unsupported completion value types are
// not rejected here. This guard does not check lifetime, browser proof or
// registration policy, so the caller retains those checks before creating an
// account from the accepted context.
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

// BuildLegacyCompleteRegistrationPendingResponse upgrades a nonnil session's
// completion to account choice and strips immediate token fields through
// normalization. It fixes the step, adoption and create controls while
// retaining other merged values, including suggested profile hints.
//
// The session email fills only absent email and resolved_email keys; existing
// empty or nil entries remain. A missing choice reason selects forced email
// before email verification, then the generic signup reason, but a present
// reason is preserved. The returned outer map is new, while unrelated nested
// values retain sharing with the stored session.
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

	// Fill missing email keys from the session hint, keeping present nil or
	// empty entries intact so this compatibility response respects earlier
	// payload choices.
	if email := strings.TrimSpace(session.ResolvedEmail); email != "" {
		if _, exists := completionResponse["email"]; !exists {
			completionResponse["email"] = email
		}
		if _, exists := completionResponse["resolved_email"]; !exists {
			completionResponse["resolved_email"] = email
		}
	}
	// Only absence selects a fallback reason; preserve explicit payload reasons
	// and prioritize forced-email policy when both new requirement observations
	// are true.
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

// CloneOAuthMetadata delegates an outer copy of provider metadata. Nil and
// empty maps become allocated empty results; nested maps, slices and pointers
// retain sharing, so later independent mutation needs another copy.
func CloneOAuthMetadata(values map[string]any) map[string]any { return CopyPendingMap(values) }

// MergeOAuthMetadata copies the base map and overlays every supplied value.
// Overlay entries win even when nil, unlike completion-response deletion
// overrides; nested objects remain shared, and the returned outer map can be
// changed independently.
func MergeOAuthMetadata(base map[string]any, overlay map[string]any) map[string]any {
	merged := CloneOAuthMetadata(base)
	maps.Copy(merged, overlay)
	return merged
}

// NormalizeAdoptedOAuthDisplayName trims a provider hint and limits it to one
// hundred runes. Short and blank input retain the trimmed value; truncation
// uses complete rune values before profile adoption stores that selected
// name.
func NormalizeAdoptedOAuthDisplayName(value string) string {
	value = strings.TrimSpace(value)
	// Retain whole characters within the profile display-name limit.
	if len([]rune(value)) > 100 {
		value = string([]rune(value)[:100])
	}
	return value
}

// OauthIdentityIssuer returns an issuer hint for the supplied pending
// session. Exact trimmed oidc type prefers ProviderKey, then the string
// issuer claim; other types use that claim only, and missing values return
// nil without URL validation.
func OauthIdentityIssuer(session *PendingAuthSession) *string {
	if session == nil {
		return nil
	}
	switch strings.TrimSpace(session.ProviderType) {
	case "oidc":
		issuer := strings.TrimSpace(session.ProviderKey)
		// A missing OIDC namespace leaves no issuer hint, so consult the saved
		// string claim while preserving this provider-specific precedence over
		// other metadata.
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

// ShouldBindPendingOAuthIdentity selects binding from the pending intent and
// saved profile decision. Missing session or decision returns false, even for
// explicit binding intent. With a decision, bind_current_user, login and
// adopt_existing_user_by_email select binding after intent normalization.
//
// Other intents select binding only when display-name or avatar adoption is
// true. An explicit false/false decision can therefore still bind a
// recognized authentication intent, while leaving unrelated intents unbound.
// The helper performs no ownership query or persistence; the caller passes
// this selection into its separately validated binding operation.
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

// ShouldSkipAvatarAdoption recognizes wrapped invalid-format, oversized and
// non-image avatar errors as skippable adoption input. Nil and other errors
// return false, allowing the calling adoption flow to distinguish these
// rejected hints from storage or unrelated failures.
func ShouldSkipAvatarAdoption(err error) bool {
	return errors.Is(err, ErrAvatarInvalid) ||
		errors.Is(err, ErrAvatarTooLarge) ||
		errors.Is(err, ErrAvatarNotImage)
}

// ApplySuggestedProfileToCompletionResponse adds string profile hints to an
// already populated payload. Empty payload or upstream maps are skipped;
// nonblank display-name and avatar hints fill only missing corresponding
// keys, so present empty or nil values stay unchanged.
//
// Any usable upstream hint sets adoption_required=true even when a
// preexisting payload field prevented its insertion. This can replace an
// existing false flag. The operation mutates the supplied payload rather than
// returning a new map, so callers own its write access and keep both maps
// stable while this merge runs.
func ApplySuggestedProfileToCompletionResponse(payload map[string]any, upstream map[string]any) {
	if len(payload) == 0 || len(upstream) == 0 {
		return
	}

	displayName := PendingSessionStringValue(upstream, "suggested_display_name")
	avatarURL := PendingSessionStringValue(upstream, "suggested_avatar_url")

	// Usable upstream hints fill absent suggestion fields while leaving existing
	// values intact; their presence still requests adoption even when no new
	// suggestion is inserted.
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

// NormalizePendingOAuthCompletionResponse copies the outer payload and
// removes immediate token fields. Recognized choice aliases become
// OAuthPendingChoiceStep after trimmed case-insensitive comparison;
// bind_login_required and other step text retain their original
// representation.
//
// A canonical-equivalent choice step forces adoption_required=true without
// necessarily rewriting its original case. Otherwise, an absent adoption flag
// is added whenever email_binding_required exists, even if that value is
// false or unsupported. Existing unrelated keys and nested objects stay
// shared in the shallow result, so callers do not interpret this
// normalization as complete payload validation.
func NormalizePendingOAuthCompletionResponse(payload map[string]any) map[string]any {
	normalized := ClonePendingMap(payload)
	// Remove immediate credentials from this response copy so exchange derives
	// its new token result from current account/session checks instead of
	// forwarding previously stored credential fields.
	for _, key := range []string{"access_token", "refresh_token", "expires_in", "token_type"} {
		delete(normalized, key)
	}
	step := strings.ToLower(strings.TrimSpace(PendingSessionStringValue(normalized, "step")))
	// Translate only known choice aliases into the canonical frontend step;
	// binding-login state stays separate because the frontend requests existing-account proof for it.
	switch step {
	case "choice", "choose_account_action", "choose_account", "choose", "email_required":
		normalized["step"] = OAuthPendingChoiceStep
	}
	// Choice-step recognition forces adoption, while a missing adoption flag
	// also accepts mere email-binding-key presence; false or unsupported marker
	// values still select that compatibility behavior.
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

// PendingOAuthChoiceCompletionResponse overlays fixed account-choice controls
// on a nonnil session's merged completion payload. A nonblank trimmed email
// replaces both email and resolved_email; blank input preserves their
// existing values without resolving a local account target.
//
// The result has a new outer map but retains unrelated nested objects.
// MergePendingCompletionResponse can retain immediate token fields, so this
// constructor alone does not produce a credential-free response. The caller
// normalizes the payload for exchange and persists target selection
// separately from these frontend controls.
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
