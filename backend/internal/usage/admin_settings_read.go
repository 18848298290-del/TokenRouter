package usage

// AdminReadSettings controls usage rankings and user access to error records.
type AdminReadSettings struct {
	AllowUserViewErrorRequests  bool
	UsageRankingEnabled         bool
	UsageRankingLimit           int
	UsageRankingShowActualCost  bool
	UsageRankingShowRequests    bool
	UsageRankingShowTotalTokens bool
	UsageRankingSortBy          string
}

// ReadAdminSettings parses ranking and error-record visibility settings.
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
