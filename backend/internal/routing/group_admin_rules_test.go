package routing

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Messages admission mirrors the authoritative protocol selection while a
// stored default mapped model remains a separate setting. The sanitizer case
// enables Messages and supplies an explicit model name so both outputs are
// checked after compatibility-field normalization.

// TestSanitizeGroupMessagesDispatchFieldsPreservesExplicitConfig applies a
// Messages-enabled selection with a stored default model. It checks that the
// admission mirror stays true and the configured name remains intact.
func TestSanitizeGroupMessagesDispatchFieldsPreservesExplicitConfig(t *testing.T) {
	t.Parallel()

	group := &Group{
		AllowedProtocols:      []capability.ProtocolID{capability.ProtocolAnthropicMessages},
		AllowMessagesDispatch: true,
		DefaultMappedModel:    "gpt-5.6-sol",
	}

	SanitizeGroupMessagesDispatchFields(group)

	require.True(t, group.AllowMessagesDispatch)
	require.Equal(t, "gpt-5.6-sol", group.DefaultMappedModel)
}
