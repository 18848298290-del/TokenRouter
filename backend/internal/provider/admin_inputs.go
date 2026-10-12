package provider

type CreateProviderInput struct {
	Name               string
	Notes              *string
	Platform           string
	Type               string
	Credentials        map[string]any
	Extra              map[string]any
	ProxyID            *int64
	Concurrency        int
	Priority           int
	RateMultiplier     *float64 // Provider billing multiplier; zero is allowed.
	LoadFactor         *int
	GroupIDs           []int64
	ExpiresAt          *int64
	AutoPauseOnExpired *bool
}

// ShadowOptions specifies creation settings for a shadow that inherits authentication credentials.
type ShadowOptions struct {
	Name        string
	Priority    int
	Concurrency int
	GroupIDs    []int64
}

type UpdateProviderInput struct {
	// PatchCredentials enables internal field maintenance and cannot be set through JSON input.
	PatchCredentials bool `json:"-"`
	// PatchExtra preserves unrelated settings from the latest locked record during field maintenance.
	PatchExtra bool `json:"-"`
	// ExpectedCredentials is supplied by internal refresh callers and excluded from JSON input.
	ExpectedCredentials *CredentialVersion `json:"-"`
	Name                string
	Notes               *string
	Type                string // Provider type.
	Credentials         map[string]any
	Extra               map[string]any
	ProxyID             *int64
	Concurrency         *int     // Nil leaves the value unchanged; a pointer to zero sets zero.
	Priority            *int     // Nil leaves the value unchanged; a pointer to zero sets zero.
	RateMultiplier      *float64 // Provider billing multiplier; zero is allowed.
	LoadFactor          *int
	Status              string
	GroupIDs            *[]int64
	ExpiresAt           *int64
	AutoPauseOnExpired  *bool
}

// BulkUpdateProvidersInput selects providers and the fields to update; omitted fields remain unchanged.
type BulkUpdateProvidersInput struct {
	ProviderIDs    []int64
	Filters        *BulkUpdateProviderFilters
	Name           string
	ProxyID        *int64
	Concurrency    *int
	Priority       *int
	RateMultiplier *float64 // Provider billing multiplier; zero is allowed.
	LoadFactor     *int
	Status         string
	Schedulable    *bool
	GroupIDs       *[]int64
	Credentials    map[string]any
	Extra          map[string]any
}

type BulkUpdateProviderFilters struct {
	Platform    string
	Type        string
	Status      string
	Group       string
	Search      string
	PrivacyMode string
}

// BulkUpdateProviderResult records one provider's update outcome.
type BulkUpdateProviderResult struct {
	ProviderID int64  `json:"provider_id"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
}

// BulkUpdateProvidersResult records ordered outcomes and success/failure counts.
type BulkUpdateProvidersResult struct {
	Success    int                        `json:"success"`
	Failed     int                        `json:"failed"`
	SuccessIDs []int64                    `json:"success_ids"`
	FailedIDs  []int64                    `json:"failed_ids"`
	Results    []BulkUpdateProviderResult `json:"results"`
}

// ProviderBulkUpdate is a configuration patch; nil pointers leave their fields unchanged.
type ProviderBulkUpdate struct {
	// ProtocolUpdates contains validated, non-secret protocol patches merged atomically in one SQL statement.
	ProtocolUpdates map[int64]map[string]any

	Name           *string
	ProxyID        *int64
	Concurrency    *int
	Priority       *int
	RateMultiplier *float64
	LoadFactor     *int
	Status         *string
	Schedulable    *bool
	Credentials    map[string]any
	Extra          map[string]any
	// EnsureCodexFingerprintSeed asks storage to atomically retain or create a seed when fingerprint convergence is enabled.
	EnsureCodexFingerprintSeed bool
}
