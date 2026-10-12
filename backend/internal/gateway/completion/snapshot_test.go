package completion

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResponseModelCompletionSnapshot captures outbound and raw response model
// data before mutating the source result. It checks that the response declaration
// in the copied completion input retains its submitted value.
func TestResponseModelCompletionSnapshot(t *testing.T) {
	source := &Input{Result: &Result{UpstreamModel: "sent", UpstreamResponseModel: "first"}}
	captured := Snapshot(source)
	source.Result.UpstreamResponseModel = "next-turn"
	require.Equal(t, "first", captured.Result.UpstreamResponseModel)
}
