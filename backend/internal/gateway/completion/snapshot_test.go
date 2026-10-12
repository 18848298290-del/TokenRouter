package completion

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResponseModelCompletionSnapshot checks that the copied response model survives later source changes.
func TestResponseModelCompletionSnapshot(t *testing.T) {
	source := &Input{Result: &Result{UpstreamModel: "sent", UpstreamResponseModel: "first"}}
	captured := Snapshot(source)
	source.Result.UpstreamResponseModel = "next-turn"
	require.Equal(t, "first", captured.Result.UpstreamResponseModel)
}
