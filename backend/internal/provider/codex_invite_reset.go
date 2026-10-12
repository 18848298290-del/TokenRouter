package provider

// CodexInviteResetStatus exposes invitation eligibility and available rate-limit resets.
type CodexInviteResetStatus struct {
	ReferralKey       string         `json:"referral_key"`
	InviteEligibility map[string]any `json:"invite_eligibility,omitempty"`
	EligibilityRules  []string       `json:"eligibility_rules,omitempty"`
	// ShouldShow is the upstream suggestion to display invitations in Codex Desktop; admins see it as a status flag.
	ShouldShow *bool `json:"should_show,omitempty"`
	// GrantAction retains the raw upstream reward action for diagnosing new reward types.
	GrantAction string `json:"grant_action,omitempty"`
	// GrantAmount is the reward amount each party receives for a completed invitation.
	GrantAmount *int `json:"grant_amount,omitempty"`
	// HasRewards reports whether invitations grant rewards; nil means the field was absent upstream.
	HasRewards *bool `json:"has_rewards,omitempty"`
	// GrantType is the normalized reward type used by the admin UI.
	GrantType string `json:"grant_type,omitempty"`
	// InviteAvailable reports whether this provider can send further Codex referral invitations.
	InviteAvailable bool `json:"invite_available"`
	// InviteUnavailableReason is a stable reason code for an unavailable invitation entry point.
	InviteUnavailableReason string `json:"invite_unavailable_reason,omitempty"`
	// InviteUnavailableMessage is a nonfatal explanation displayed to the administrator.
	InviteUnavailableMessage string                   `json:"invite_unavailable_message,omitempty"`
	RequiresConsent          bool                     `json:"requires_consent"`
	AvailableCount           int                      `json:"available_count"`
	Credits                  []CodexInviteResetCredit `json:"credits"`
	RawEligibilityRules      map[string]any           `json:"raw_eligibility_rules,omitempty"`
	RawCredits               map[string]any           `json:"raw_credits,omitempty"`
}

type CodexInviteResetCredit struct {
	ID              string         `json:"id"`
	Status          string         `json:"status,omitempty"`
	Title           string         `json:"title,omitempty"`
	Description     string         `json:"description,omitempty"`
	ResetType       string         `json:"reset_type,omitempty"`
	GrantedAt       string         `json:"granted_at,omitempty"`
	ExpiresAt       string         `json:"expires_at,omitempty"`
	ProfileUserID   string         `json:"profile_user_id,omitempty"`
	ProfileImageURL string         `json:"profile_image_url,omitempty"`
	Raw             map[string]any `json:"raw,omitempty"`
}

type CodexInviteResetInviteResult struct {
	Invites      []map[string]any `json:"invites,omitempty"`
	FailedEmails []string         `json:"failed_emails,omitempty"`
	Message      string           `json:"message,omitempty"`
	Raw          map[string]any   `json:"raw,omitempty"`
}

type CodexInviteResetConsumeResult struct {
	Code             string           `json:"code,omitempty"`
	CreditID         string           `json:"credit_id,omitempty"`
	RedeemRequestID  string           `json:"redeem_request_id"`
	WindowsReset     int              `json:"windows_reset"`
	AvailableCount   *int             `json:"available_count,omitempty"`
	RemainingCredits []map[string]any `json:"remaining_credits,omitempty"`
	Raw              map[string]any   `json:"raw,omitempty"`
}
