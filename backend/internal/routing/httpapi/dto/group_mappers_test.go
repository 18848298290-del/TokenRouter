package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// TestGroupMapperExposesOpenAIFastOnlyToAdmins checks that public group responses omit Fast policy.
func TestGroupMapperExposesOpenAIFastOnlyToAdmins(t *testing.T) {
	group := &routing.Group{
		ID: 7, Name: "fast", Status: billing.StatusActive,
		ForceOpenAIFast: true,
	}

	userJSON, err := json.Marshal(GroupFromService(group))
	require.NoError(t, err)
	require.NotContains(t, string(userJSON), "force_openai_fast")
	require.NotContains(t, string(userJSON), "free_openai_fast")

	adminJSON, err := json.Marshal(GroupFromServiceAdmin(group))
	require.NoError(t, err)
	require.Contains(t, string(adminJSON), `"force_openai_fast":true`)
	require.NotContains(t, string(adminJSON), `"free_openai_fast"`)
}

func GroupFromService(v *routing.Group) *Group {
	return GroupFromRouting(routing.CloneGroup(v))
}

func GroupFromServiceAdmin(v *routing.Group) *AdminGroup[json.RawMessage] {
	return AdminGroupFromRouting[json.RawMessage](routing.CloneGroup(v))
}
