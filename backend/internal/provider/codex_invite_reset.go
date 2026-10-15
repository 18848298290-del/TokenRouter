package provider

// codex_invite_reset.go defines Codex invitation status, reset-credit details,
// and invitation and consumption result values.
// Codex invitation eligibility and reset-credit availability are reported
// separately so an unavailable invitation entry point can coexist with usable
// credits. Optional upstream reward fields retain presence, and raw objects
// accompany normalized display fields for account operations and diagnostics.

// CodexInviteResetStatus combines invitation-query availability, reward hints,
// and existing reset credits. Raw maps remain borrowed from client responses;
// callers keep them stable or copy supported values before independently
// editing the display data.
type CodexInviteResetStatus struct {
	// queried invitation referral identifier
	ReferralKey string `json:"referral_key"`
	// InviteEligibility retains the client response map. Keep its nested
	// values stable while status normalization or later consumers read them.
	InviteEligibility map[string]any `json:"invite_eligibility,omitempty"`
	// normalized invitation rule text
	EligibilityRules []string `json:"eligibility_rules,omitempty"`
	// ShouldShow retains a converted upstream invitation display hint when
	// bool or string data is available. Missing, nil, or unsupported type stays
	// nil.
	ShouldShow *bool `json:"should_show,omitempty"`
	// GrantAction is trimmed converted reward-action text. GrantType combines
	// it with HasRewards, while this field remains available for administrator
	// diagnostics.
	GrantAction string `json:"grant_action,omitempty"`
	// GrantAmount retains a converted upstream reward amount when its key is
	// present. Missing key yields nil; present unsupported or nil input becomes
	// zero.
	GrantAmount *int `json:"grant_amount,omitempty"`
	// HasRewards preserves bool or string presence; other types yield nil.
	// Explicit false overrides recognized reward actions when GrantType is
	// selected.
	HasRewards *bool `json:"has_rewards,omitempty"`
	// GrantType is the administrator reward enum selected from HasRewards and
	// GrantAction. Explicit no-reward input wins; unresolved actions become
	// unknown.
	GrantType string `json:"grant_type,omitempty"`
	// InviteAvailable reports whether both invitation eligibility/rule queries
	// succeeded. It describes query availability and does not validate each
	// requested invitation.
	InviteAvailable bool `json:"invite_available"`
	// InviteUnavailableReason records the stable availability code when either
	// invitation request fails, while existing credit queries can still succeed.
	InviteUnavailableReason string `json:"invite_unavailable_reason,omitempty"`
	// InviteUnavailableMessage explains unavailable invitation queries to the
	// administrator while the status response can still include usable reset
	// credits.
	InviteUnavailableMessage string `json:"invite_unavailable_message,omitempty"`
	// RequiresConsent reads an exact upstream boolean or defaults to true.
	// Missing, nil, and other types therefore retain administrator confirmation.
	RequiresConsent bool `json:"requires_consent"`
	// AvailableCount prefers an explicit detail count after successful enrichment.
	// Otherwise usage count remains, with zero usage initially derived from
	// available entries.
	AvailableCount int `json:"available_count"`
	// normalized reset credit entries
	Credits []CodexInviteResetCredit `json:"credits"`
	// RawEligibilityRules retains the borrowed rule response for diagnostics.
	// Copy supported values before editing them independently of other consumers.
	RawEligibilityRules map[string]any `json:"raw_eligibility_rules,omitempty"`
	// RawCredits retains the borrowed credit observation selected by status
	// querying. Keep its nested objects stable or copy them before editing.
	RawCredits map[string]any `json:"raw_credits,omitempty"`
}

// CodexInviteResetCredit carries normalized display metadata for one
// identified upstream credit.
type CodexInviteResetCredit struct {
	// upstream reset credit identifier
	ID string `json:"id"`
	// upstream reset credit status
	Status string `json:"status,omitempty"`
	// upstream credit display title
	Title string `json:"title,omitempty"`
	// upstream credit display description
	Description string `json:"description,omitempty"`
	// upstream credit reset scope
	ResetType string `json:"reset_type,omitempty"`
	// upstream credit grant text
	GrantedAt string `json:"granted_at,omitempty"`
	// upstream credit expiry text
	ExpiresAt string `json:"expires_at,omitempty"`
	// related upstream user identifier
	ProfileUserID string `json:"profile_user_id,omitempty"`
	// related upstream profile image
	ProfileImageURL string `json:"profile_image_url,omitempty"`
	// Raw retains this credit's borrowed upstream object. Keep its nested values
	// stable while consumers retain the normalized credit for display.
	Raw map[string]any `json:"raw,omitempty"`
}

// CodexInviteResetInviteResult reports upstream invitation outcomes and
// retains their raw response.
type CodexInviteResetInviteResult struct {
	// Invites contains borrowed response objects in newly selected slice
	// storage. Keep those maps stable while consumers inspect invitation results.
	Invites []map[string]any `json:"invites,omitempty"`
	// reported failed email addresses
	FailedEmails []string `json:"failed_emails,omitempty"`
	// upstream invitation diagnostic text
	Message string `json:"message,omitempty"`
	// Raw retains the borrowed invitation response and its nested values. Copy
	// supported objects before independent edits while another consumer retains it.
	Raw map[string]any `json:"raw,omitempty"`
}

// CodexInviteResetConsumeResult associates consumption with its generated
// redeem request and remaining credits.
type CodexInviteResetConsumeResult struct {
	// upstream consumption result code
	Code string `json:"code,omitempty"`
	// requested selected credit identifier
	CreditID string `json:"credit_id,omitempty"`
	// generated consumption request identifier
	RedeemRequestID string `json:"redeem_request_id"`
	// observed reset window count
	WindowsReset int `json:"windows_reset"`
	// optional remaining credit count
	AvailableCount *int `json:"available_count,omitempty"`
	// RemainingCredits selects borrowed response objects into new slice storage.
	// Keep each map stable while displaying remaining credits after consumption.
	RemainingCredits []map[string]any `json:"remaining_credits,omitempty"`
	// Raw retains the borrowed consumption response for request correlation.
	// Keep nested values stable or copy them before independent consumer edits.
	Raw map[string]any `json:"raw,omitempty"`
}
