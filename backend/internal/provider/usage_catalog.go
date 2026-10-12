package provider

import "github.com/TokenFlux/TokenRouter/internal/upstream/usageview"

const (
	UpstreamUsageAdapterSub2API         = usageview.UpstreamUsageAdapterSub2API
	UpstreamUsageAdapterNewAPI          = usageview.UpstreamUsageAdapterNewAPI
	UpstreamUsageAdapterZivv            = usageview.UpstreamUsageAdapterZivv
	UpstreamUsageAdapterKimiCoding      = usageview.UpstreamUsageAdapterKimiCoding
	UpstreamUsageAdapterZhipuCoding     = usageview.UpstreamUsageAdapterZhipuCoding
	UpstreamUsageAdapterKimiBalance     = usageview.UpstreamUsageAdapterKimiBalance
	UpstreamUsageAdapterDeepseekBalance = usageview.UpstreamUsageAdapterDeepseekBalance
	NewAPIUserAccessTokenCredentialKey  = "new_api_user_access_token"
	NewAPIUserIDCredentialKey           = "new_api_user_id"
	UpstreamUsageDefaultAdapter         = UpstreamUsageAdapterSub2API
)

var usageAdapterCatalog = []UsageAdapterSpec{
	{Name: UpstreamUsageAdapterSub2API, Label: "Sub2API / TokenRouter", Automatic: false},
	{Name: UpstreamUsageAdapterNewAPI, Label: "New API", Automatic: false},
	{Name: UpstreamUsageAdapterZivv, Label: "Zivv", Automatic: false},
	{Name: UpstreamUsageAdapterKimiCoding, Label: "Kimi Coding Plan", Automatic: true},
	{Name: UpstreamUsageAdapterZhipuCoding, Label: "Zhipu Coding Plan", Automatic: true},
	{Name: UpstreamUsageAdapterKimiBalance, Label: "Kimi Balance", Automatic: true},
	{Name: UpstreamUsageAdapterDeepseekBalance, Label: "DeepSeek Balance", Automatic: true},
}

// UsageAdapterSpec describes an adapter in the usage catalog.
type UsageAdapterSpec struct {
	Name, Label string
	Automatic   bool
}

// UpstreamUsageAdapterCatalog returns a copy callers may modify independently.
func UpstreamUsageAdapterCatalog() []UsageAdapterSpec {
	return append([]UsageAdapterSpec(nil), usageAdapterCatalog...)
}

// UpstreamUsageAdapterOptions returns manually selectable adapters in catalog order.
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
