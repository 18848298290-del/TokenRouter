package contract

const (
	DefaultMaxResults = 5

	// Provider type identifiers.
	ProviderTypeBrave  = "brave"
	ProviderTypeTavily = "tavily"
)

// SearchResult represents a single web search result.
type SearchResult struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	PageAge string `json:"page_age,omitempty"`
}

// SearchRequest describes a web search to perform.
type SearchRequest struct {
	Query      string
	MaxResults int    // Defaults to DefaultMaxResults when nonpositive.
	ProxyURL   string // Optional proxy override applied by the executor.
}

// SearchResponse holds the results of a web search.
type SearchResponse struct {
	Results []SearchResult
	Query   string // the query that was actually executed
}

// ProviderConfig holds the configuration for a single search provider.
type ProviderConfig struct {
	Type         string `json:"type"`                    // ProviderTypeBrave | ProviderTypeTavily
	APIKey       string `json:"api_key"`                 // secret
	QuotaLimit   int64  `json:"quota_limit"`             // Nonpositive values mean unlimited.
	SubscribedAt *int64 `json:"subscribed_at,omitempty"` // Unix seconds; quota TTL includes a 24-hour buffer.
	ProxyURL     string `json:"-"`                       // resolved proxy URL (not persisted)
	ProxyID      int64  `json:"-"`                       // resolved proxy ID for unavailability tracking
	ExpiresAt    *int64 `json:"expires_at,omitempty"`    // optional expiration (unix seconds)
}
