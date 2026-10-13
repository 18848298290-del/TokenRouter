package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// TestGroupMapperExposesOpenAIFastOnlyToAdmins serializes a group with forced
// Fast enabled through user and administrator mappers. User JSON omits
// force/free billing policy fields, and administrator JSON exposes force
// while still omitting free.
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

// GroupFromService clones the routing fixture before producing its user DTO.
// Nil inputs remain nil through the delegated helpers, so tests can use the
// same entry for absent groups.
func GroupFromService(v *routing.Group) *Group {
	return GroupFromRouting(routing.CloneGroup(v))
}

// GroupFromServiceAdmin clones the routing fixture before producing its
// administrator DTO. It uses json.RawMessage for provider details and
// preserves nil input through the delegated cloning and mapping helpers.
func GroupFromServiceAdmin(v *routing.Group) *AdminGroup[json.RawMessage] {
	return AdminGroupFromRouting[json.RawMessage](routing.CloneGroup(v))
}
