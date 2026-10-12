package compact

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStreamPayloadInjectsIDOnlyWhenRequired supplies a compact response with
// and without an existing ID. The generator stays unused for a supplied ID and
// is called once to populate a missing one.
func TestStreamPayloadInjectsIDOnlyWhenRequired(t *testing.T) {
	calls := 0
	id := func() string { calls++; return "resp_generated" }
	_, ok := StreamPayload([]byte(`{"id":"resp_existing","output":[]}`), id)
	require.True(t, ok)
	require.Zero(t, calls)
	result, ok := StreamPayload([]byte(`{"output":[]}`), id)
	require.True(t, ok)
	require.Equal(t, 1, calls)
	require.Contains(t, string(result), `"id":"resp_generated"`)
}
