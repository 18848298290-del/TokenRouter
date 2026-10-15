package provider

// query_contracts.go defines provider listing filters and refresh-page
// metadata used by storage and background scans.
// Background refresh selects provider IDs before loading their complete records
// and associations. Records can disappear between those reads, so page metadata
// retains the highest selected ID and whether the original ID page filled its
// limit, independently of the hydrated record count.
//
// The scan advances through this metadata after processing a page. Listing
// sentinels separately identify an ungrouped provider query and an unset
// privacy setting, allowing HTTP readers and storage filters to share those
// exact values.

// Listing sentinels request ungrouped providers and missing or empty privacy
// values from the storage filters shared by administrator HTTP queries.
const (
	ProviderListGroupUngrouped     int64 = -1          // providers with no group association
	ProviderPrivacyModeUnsetFilter       = "__unset__" // missing or empty privacy setting
)

// OAuthRefreshPageOptions defines a bounded OAuth provider scan with an ID
// cursor.
// Platforms select the registered refreshers eligible for this scan.
type OAuthRefreshPageOptions struct {
	// Platforms supplies the nonempty upstream selection for the storage scan.
	// The storage query also requires schedulable=true independently of ActiveOnly.
	Platforms []string
	AfterID   int64 // exclusive ascending provider-ID cursor
	// Limit bounds the selected ID page. ProviderStore accepts values from one
	// through one thousand before loading their full records and associations.
	Limit      int
	ActiveOnly bool // require persisted active status
	// IncludeSetupToken admits setup-token records alongside OAuth and Qoder
	// COSY records; Qoder COSY remains eligible when this flag is false.
	IncludeSetupToken   bool
	RequireRefreshToken bool // require nonblank stored refresh_token
	// ExcludeRetryCooldown skips future temporary pauses whose reason starts
	// with token refresh retry exhausted. Other temporary pauses remain eligible.
	ExcludeRetryCooldown bool
}

// OAuthRefreshCandidatePage carries the cursor metadata from the SQL page.
// The selected ID page advances the scan even when detail loading loses records.
type OAuthRefreshCandidatePage struct {
	Providers []Record // ID-ordered hydrated provider records
	// NextAfterID and HasMore describe the original selected ID page. Hydration
	// can return fewer records after concurrent deletion, so the scan uses these
	// values rather than inferring progress from Providers alone.
	NextAfterID int64 // highest originally selected provider ID
	HasMore     bool  // original ID page filled its limit
}
