package provider

import "github.com/TokenFlux/TokenRouter/internal/upstream/usageview"

// usage_catalog.go lists supported upstream usage adapters and filters
// administrator-selectable formats.
// Usage queries expose four administrator-selectable relay formats and four
// platform-selected CN formats. The catalog keeps those categories together so
// diagnostics can name every adapter while administrator choices remain limited
// to the manual formats.

// Adapter identifiers select fixed registered usage formats and identify
// normalized results returned by manual queries or CN monitoring.
const (
	UpstreamUsageAdapterSub2API         = usageview.UpstreamUsageAdapterSub2API         // Sub2API relay usage format
	UpstreamUsageAdapterNewAPI          = usageview.UpstreamUsageAdapterNewAPI          // New API usage format
	UpstreamUsageAdapterDerouter        = usageview.UpstreamUsageAdapterDerouter        // derouter.ai account balance format
	UpstreamUsageAdapterZivv            = usageview.UpstreamUsageAdapterZivv            // Zivv wallet usage format
	UpstreamUsageAdapterKimiCoding      = usageview.UpstreamUsageAdapterKimiCoding      // Kimi coding percentage format
	UpstreamUsageAdapterZhipuCoding     = usageview.UpstreamUsageAdapterZhipuCoding     // Zhipu coding percentage format
	UpstreamUsageAdapterKimiBalance     = usageview.UpstreamUsageAdapterKimiBalance     // Kimi wallet balance format
	UpstreamUsageAdapterDeepseekBalance = usageview.UpstreamUsageAdapterDeepseekBalance // DeepSeek multicurrency balance format
	// Wallet credential keys supply optional New API user authentication when
	// an API Key cannot read its wallet balance.
	NewAPIUserAccessTokenCredentialKey = "new_api_user_access_token" // wallet access token key
	NewAPIUserIDCredentialKey          = "new_api_user_id"           // wallet user ID key
	UpstreamUsageDefaultAdapter        = UpstreamUsageAdapterSub2API // default relay query format

	// DerouterUsageDefaultBaseURL supplies the account management root when an
	// administrator selects derouter without a separate query address override.
	DerouterUsageDefaultBaseURL = "https://cf-api.derouter.ai"
)

// usageAdapterCatalog associates each supported format with its display name and
// automatic selection rule for catalog and administrator-option consumers.
var usageAdapterCatalog = []UsageAdapterSpec{
	{Name: UpstreamUsageAdapterSub2API, Label: "Sub2API / TokenRouter", Automatic: false},
	{Name: UpstreamUsageAdapterNewAPI, Label: "New API", Automatic: false},
	{Name: UpstreamUsageAdapterZivv, Label: "Zivv", Automatic: false},
	{Name: UpstreamUsageAdapterDerouter, Label: "derouter.ai (Account Key)", Automatic: false},
	{Name: UpstreamUsageAdapterKimiCoding, Label: "Kimi Coding Plan", Automatic: true},
	{Name: UpstreamUsageAdapterZhipuCoding, Label: "Zhipu Coding Plan", Automatic: true},
	{Name: UpstreamUsageAdapterKimiBalance, Label: "Kimi Balance", Automatic: true},
	{Name: UpstreamUsageAdapterDeepseekBalance, Label: "DeepSeek Balance", Automatic: true},
}

// UsageAdapterSpec names a usage format and its selection category.
type UsageAdapterSpec struct {
	Name, Label string // registered adapter name and administrator display label
	// Automatic excludes the adapter from administrator choices because CN platform
	// and provider mode determine the matching query format.
	Automatic bool // platform selects adapter
}

// UpstreamUsageAdapterCatalog returns a new slice containing every catalog entry
// in registration order. Entries contain strings and a boolean, so callers may
// edit the returned values independently.
func UpstreamUsageAdapterCatalog() []UsageAdapterSpec {
	return append([]UsageAdapterSpec(nil), usageAdapterCatalog...)
}

// UpstreamUsageAdapterOptions returns administrator choices in catalog order,
// excluding adapters selected automatically by CN platform and mode. Each call
// builds a new slice of name and label values.
func UpstreamUsageAdapterOptions() []UpstreamUsageAdapterOption {
	options := make([]UpstreamUsageAdapterOption, 0, len(usageAdapterCatalog))
	for _, registration := range usageAdapterCatalog {
		if registration.Automatic {
			continue
		}
		options = append(options, UpstreamUsageAdapterOption{Name: registration.Name, Label: registration.Label})
	}
	return options
}
