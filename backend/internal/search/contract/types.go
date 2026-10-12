package contract

// Search quota counts supplier calls, while search results contain the links
// and excerpts returned by that supplier. Persisted provider settings carry
// optional subscription and expiration timestamps; resolved proxy addresses
// accompany execution configurations after configuration publication.

// These values identify supported suppliers and set the result count requested
// when a search supplies no positive count.
const (
	DefaultMaxResults = 5 // default requested result count

	ProviderTypeBrave  = "brave"  // Brave supplier identifier
	ProviderTypeTavily = "tavily" // Tavily supplier identifier
)

// SearchResult contains one supplier's link, title, excerpt, and optional age.
type SearchResult struct {
	URL     string `json:"url"`                // result page address
	Title   string `json:"title"`              // result page title
	Snippet string `json:"snippet"`            // supplier text excerpt
	PageAge string `json:"page_age,omitempty"` // optional supplier age description
}

// SearchRequest carries query text, the requested result count, and proxy
// selection.
type SearchRequest struct {
	Query string // supplier query text
	// MaxResults requests a result count. Brave and Tavily use DefaultMaxResults
	// when nonpositive; Brave additionally caps it at twenty.
	MaxResults int
	// ProxyURL overrides the configured supplier proxy when nonempty. Executor
	// applies it before constructing the provider and its client.
	ProxyURL string
}

// SearchResponse pairs normalized supplier results with the submitted query
// text.
type SearchResponse struct {
	Results []SearchResult // normalized supplier result entries
	Query   string         // query submitted to supplier
}

// ProviderConfig supplies one Manager candidate's credentials, quota dates, and
// resolved proxy.
type ProviderConfig struct {
	Type   string `json:"type"`    // supplier identifier and quota-key suffix
	APIKey string `json:"api_key"` // supplier authentication secret
	// QuotaLimit caps confirmed supplier calls in Manager. Nonpositive values
	// bypass reservation, while successful positive-limit searches retain their
	// increments.
	QuotaLimit int64 `json:"quota_limit"`
	// SubscribedAt anchors Manager's monthly UTC counter lifetime in Unix seconds.
	// Nil or zero selects thirty-two days; dated lifetimes include a
	// twenty-four-hour buffer.
	SubscribedAt *int64 `json:"subscribed_at,omitempty"`
	// ProxyURL is the resolved supplier proxy address. A nonempty
	// SearchRequest.ProxyURL replaces it for the current executor call.
	ProxyURL string `json:"-"`
	// ProxyID identifies the configured proxy for Manager's failure markers. A
	// request proxy override bypasses this ID because it has no configured
	// search-proxy identity.
	ProxyID int64 `json:"-"`
	// ExpiresAt is an optional Unix-second expiration. Manager skips the candidate
	// after the current Unix second exceeds the stored value.
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}
