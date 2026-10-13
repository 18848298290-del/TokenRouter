package identity

import (
	"context"
	"strings"
)

// Email OAuth profile data becomes both a stable local identity input and a
// pending registration view. The provider subject identifies the account, while
// verified email and display hints supply registration values. Draft creation
// writes authoritative identity keys first and copies additional metadata only
// when those keys are absent, preventing metadata from replacing the selected
// subject or verified-email flag.
//
// Email registration draft maps are new containers, but mutable values copied
// from provider metadata remain shared. The completion response holds browser
// choices and invitation state separately from the provider claims used by
// finalization. Google One Tap enters this flow through validated claims; its
// conversion helper marks email verification true, so the caller completes
// credential and email checks before building that profile.

// EmailOAuthProfile carries provider identity values and optional local
// registration hints.
type EmailOAuthProfile struct {
	// Subject and Email have different roles: Subject is the stable provider
	// identity, while Email is a registration input whose eligibility depends on
	// EmailVerified and the caller's account policy checks. Subject: stable
	// provider account subject. Email: supplied provider email address.
	Subject, Email string
	// EmailVerified records provider verification evidence for the supplied email.
	// Registration callers check this flag before treating that email as verified.
	EmailVerified bool
	// Username, DisplayName and AvatarURL supply separate local profile hints.
	// Draft construction trims their text and includes optional display/avatar
	// claims only when their explicit values are nonblank. Username: suggested
	// local account name. DisplayName: suggested profile display name. AvatarURL:
	// suggested profile image URL.
	Username, DisplayName, AvatarURL string
	// Metadata holds additional provider claims. Identity input retains this map,
	// while draft creation copies its entries shallowly and keeps already selected
	// identity keys in preference to matching metadata keys.
	Metadata map[string]any
}

// GoogleIDTokenClaims holds accepted One Tap identity values extracted by a
// verifier.
type GoogleIDTokenClaims struct {
	// Subject and Email identify the Google account and its verified mailbox. The
	// production verifier checks both are nonblank before returning claims, and
	// the One Tap handler repeats that acceptance check. Subject: stable Google
	// account subject. Email: accepted Google mailbox address.
	Subject, Email string
	// EmailVerified is checked by the verifier and One Tap handler. Profile
	// conversion assumes accepted claims and marks its resulting email profile
	// verified.
	EmailVerified bool
	// Name, GivenName and Picture supply profile hints; Locale and HostedDomain
	// become optional metadata. Production verification treats missing or
	// non-string optional claims as empty text before conversion. Name: supplied
	// display name. GivenName: supplied given name. Picture: supplied avatar URL.
	// Locale: supplied locale text. HostedDomain: supplied organization domain.
	Name, GivenName, Picture, Locale, HostedDomain string
}

// GoogleIDTokenVerifier validates a browser credential for the configured
// Google audience.
type GoogleIDTokenVerifier interface {
	// Verify checks the credential for the supplied OAuth client audience and
	// returns accepted identity claims. The production adapter verifies signature,
	// issuer, audience, expiry and a verified nonempty email before returning
	// them.
	Verify(context.Context, string, string) (*GoogleIDTokenClaims, error)
}

// OAuthPendingDraft carries identity decisions and browser state for
// pending-session creation.
type OAuthPendingDraft struct {
	Intent   string                 // selected pending authentication intent
	Identity PendingAuthIdentityKey // selected provider identity tuple
	// TargetUserID selects an existing local account for binding or adoption
	// intents. Login-registration drafts leave it nil until their flow chooses a
	// target.
	TargetUserID *int64
	// ResolvedEmail supplies the selected account email; RedirectTo names the
	// frontend destination, and BrowserSessionKey ties the pending work to its
	// browser. The HTTP flow validates destinations and prepares browser state.
	// ResolvedEmail: selected local account email. RedirectTo: selected frontend
	// return path. BrowserSessionKey: pending browser session key.
	ResolvedEmail, RedirectTo, BrowserSessionKey string
	// UpstreamIdentityClaims carries provider inputs for finalization, while
	// CompletionResponse carries browser completion choices. Their containers and
	// nested mutable values remain shared with draft consumers until persistence
	// or another producer makes a copy. UpstreamIdentityClaims: selected provider
	// claim values. CompletionResponse: browser completion response values.
	UpstreamIdentityClaims, CompletionResponse map[string]any
	PromoCode                                  string // pending signup promotion code
}

// EmailOAuthIdentityFromProfile maps provider profile values into account
// identity input. A nil profile returns a zero input; non-nil profiles retain
// their text as supplied and share Metadata through UpstreamMetadata with the
// resulting value.
func EmailOAuthIdentityFromProfile(provider string, profile *EmailOAuthProfile) EmailOAuthIdentityInput {
	if profile == nil {
		return EmailOAuthIdentityInput{}
	}
	return EmailOAuthIdentityInput{
		ProviderType:     provider,
		ProviderKey:      provider,
		ProviderSubject:  profile.Subject,
		Email:            profile.Email,
		EmailVerified:    profile.EmailVerified,
		Username:         profile.Username,
		DisplayName:      profile.DisplayName,
		AvatarURL:        profile.AvatarURL,
		UpstreamMetadata: profile.Metadata,
	}
}

// PrepareEmailRegistrationDraft builds a login-registration draft from a
// non-nil accepted provider profile and prepared browser state. It normalizes
// the email, username and subject, while the caller supplies an accepted
// provider name and frontend destination for the pending flow.
//
// Selected identity claims are written before optional metadata, so matching
// metadata keys cannot replace those values. Nonblank explicit display/avatar
// hints and affiliate input also take that precedence; otherwise metadata can
// supply their absent keys. The new maps retain nested metadata values
// shallowly. Invitation requirements choose both completion error and reason
// together, while the trimmed promotion code remains a separate pending-draft
// field.
func PrepareEmailRegistrationDraft(provider, frontendCallback, redirectTo, browserSessionKey string, profile *EmailOAuthProfile, affCode, promoCode string, invitationRequired bool) OAuthPendingDraft {
	email := strings.TrimSpace(strings.ToLower(profile.Email))
	username := strings.TrimSpace(profile.Username)
	affCode = strings.TrimSpace(affCode)
	upstreamClaims := map[string]any{
		"email":            email,
		"email_verified":   profile.EmailVerified,
		"username":         username,
		"provider":         provider,
		"provider_key":     provider,
		"provider_subject": strings.TrimSpace(profile.Subject),
	}
	if strings.TrimSpace(profile.DisplayName) != "" {
		upstreamClaims["suggested_display_name"] = strings.TrimSpace(profile.DisplayName)
	}
	if strings.TrimSpace(profile.AvatarURL) != "" {
		upstreamClaims["suggested_avatar_url"] = strings.TrimSpace(profile.AvatarURL)
	}
	if affCode != "" {
		upstreamClaims["aff_code"] = affCode
	}
	// Fill absent metadata keys after the selected identity and explicit hints, so
	// provider extras cannot replace the subject, verified email or supplied
	// affiliate decision.
	for key, value := range profile.Metadata {
		if _, exists := upstreamClaims[key]; !exists {
			upstreamClaims[key] = value
		}
	}

	pendingError := "registration_completion_required"
	choiceReason := "registration_completion_required"
	if invitationRequired {
		pendingError = "invitation_required"
		choiceReason = "invitation_required"
	}
	completionResponse := map[string]any{
		"step":                      OAuthPendingChoiceStep,
		"error":                     pendingError,
		"choice_reason":             choiceReason,
		"adoption_required":         false,
		"create_account_allowed":    true,
		"existing_account_bindable": false,
		"force_email_on_signup":     true,
		"invitation_required":       invitationRequired,
		"email":                     email,
		"resolved_email":            email,
		"provider":                  provider,
		"redirect":                  redirectTo,
	}
	if strings.TrimSpace(frontendCallback) != "" {
		completionResponse["frontend_callback"] = strings.TrimSpace(frontendCallback)
	}

	return OAuthPendingDraft{
		Intent:                 OAuthIntentLogin,
		Identity:               PendingAuthIdentityKey{ProviderType: provider, ProviderKey: provider, ProviderSubject: strings.TrimSpace(profile.Subject)},
		ResolvedEmail:          email,
		RedirectTo:             redirectTo,
		BrowserSessionKey:      browserSessionKey,
		UpstreamIdentityClaims: upstreamClaims,
		CompletionResponse:     completionResponse,
		PromoCode:              strings.TrimSpace(promoCode),
	}
}

// GoogleEmailProfile builds an email OAuth profile from non-nil accepted Google
// claims. The caller validates the credential and verified-email flag first,
// because conversion marks email verification true regardless of the supplied
// flag. Username selection uses given name, then name, then email after
// trimming, and nonempty locale or hosted-domain text becomes additional
// metadata.
func GoogleEmailProfile(c *GoogleIDTokenClaims) *EmailOAuthProfile {
	metadata := map[string]any{"email_verified": true}
	if c.Locale != "" {
		metadata["locale"] = c.Locale
	}
	if c.HostedDomain != "" {
		metadata["hosted_domain"] = c.HostedDomain
	}
	return &EmailOAuthProfile{Subject: c.Subject, Email: c.Email, EmailVerified: true, Username: OAuthFirstNonEmpty(c.GivenName, c.Name, c.Email), DisplayName: c.Name, AvatarURL: c.Picture, Metadata: metadata}
}
