package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider/transfer"
)

// TestArchiveIdentityHintsDoNotReplaceExplicitValues protects the platform, type, and nonempty credentials during hint filling.
func TestArchiveIdentityHintsDoNotReplaceExplicitValues(t *testing.T) {
	item := transfer.DataProvider{Platform: " OpenAI ", Type: " OAUTH ", Credentials: map[string]any{"id_token": " fixture-token ", "email": "admin@example.test", "plan_type": nil}}
	require.Equal(t, " fixture-token ", ArchiveIDToken(&item))
	FillArchiveIdentity(&item, &ArchiveIdentityHints{Email: "token@example.test", PlanType: "pro", ChatGPTAccountID: "workspace"})
	require.Equal(t, "admin@example.test", item.Credentials["email"])
	require.Equal(t, "pro", item.Credentials["plan_type"])
	require.Equal(t, "workspace", item.Credentials["chatgpt_account_id"])
	item.Type = ProviderTypeAPIKey
	require.Empty(t, ArchiveIDToken(&item))
	require.Empty(t, ArchiveIDToken(nil))
}
