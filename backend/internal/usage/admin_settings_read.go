package usage

// Administrator settings expose the configured ranking metric and visibility
// switches together. Parsing passes ranking values through the same
// normalization used by the user page, while error-record access requires the
// stored string true.

// AdminReadSettings controls usage rankings and user access to error records.
type AdminReadSettings struct {
	// AllowUserViewErrorRequests is true when ReadAdminSettings reads the exact
	// persisted string true; missing or differently spelled values keep the view
	// disabled.
	AllowUserViewErrorRequests bool // user error-record visibility switch
	UsageRankingEnabled        bool // public ranking availability switch
	// UsageRankingLimit and UsageRankingSortBy hold normalized ranking inputs.
	// UsageRankingShowActualCost, UsageRankingShowRequests, and
	// UsageRankingShowTotalTokens expose the ordering metric and retain the other
	// visibility choices returned by ParseRankingSettings.
	UsageRankingLimit           int    // normalized ranking row limit
	UsageRankingShowActualCost  bool   // user charge visibility switch
	UsageRankingShowRequests    bool   // request count visibility switch
	UsageRankingShowTotalTokens bool   // token count visibility switch
	UsageRankingSortBy          string // normalized ranking metric name
}

// ReadAdminSettings returns normalized ranking settings and the user error-view
// switch for administrator reads. Missing ranking values select defaults; error
// viewing is enabled only by the exact string true.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	usageRanking := ParseRankingSettings(settings)
	result := &AdminReadSettings{}

	result.UsageRankingLimit = usageRanking.Limit
	result.UsageRankingEnabled = usageRanking.Enabled
	result.UsageRankingSortBy = string(usageRanking.SortBy)
	result.UsageRankingShowTotalTokens = usageRanking.ShowTotalTokens
	result.UsageRankingShowRequests = usageRanking.ShowRequests
	result.UsageRankingShowActualCost = usageRanking.ShowActualCost
	result.AllowUserViewErrorRequests = settings[SettingKeyAllowUserViewErrorRequests] == "true"
	return result
}
