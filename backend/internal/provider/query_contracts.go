package provider

const (
	ProviderListGroupUngrouped     int64 = -1
	ProviderPrivacyModeUnsetFilter       = "__unset__"
)

// OAuthRefreshPageOptions defines a bounded OAuth provider scan with an ID cursor.
// Platforms correspond to refreshers registered with TokenRefreshService.
type OAuthRefreshPageOptions struct {
	Platforms            []string
	AfterID              int64
	Limit                int
	ActiveOnly           bool
	IncludeSetupToken    bool
	RequireRefreshToken  bool
	ExcludeRetryCooldown bool
}

// OAuthRefreshCandidatePage carries the cursor metadata from the SQL page.
// The original cursor advances the scan even when records are deleted before detail loading.
type OAuthRefreshCandidatePage struct {
	Providers   []Record
	NextAfterID int64
	HasMore     bool
}
