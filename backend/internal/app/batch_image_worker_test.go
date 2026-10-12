package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
)

// Absent application configuration still produces a positive queue-reservation
// timeout because batchWorkerOptions normalizes zero options. The check compares
// that field with the module's default and verifies its sign, concentrating on
// the adapter's returned settings.

// TestNewBatchImageWorkerOptionsFromConfig_UsesFiniteReserveTimeout passes nil
// configuration to the batch worker adapter. It requires ReserveBlockTimeout
// to equal the module's default and remain a positive duration.
func TestNewBatchImageWorkerOptionsFromConfig_UsesFiniteReserveTimeout(t *testing.T) {
	opts := batchWorkerOptions(nil)
	require.Equal(t, batchimage.DefaultBatchImageWorkerReserveBlockTimeout, opts.ReserveBlockTimeout)
	require.Positive(t, opts.ReserveBlockTimeout)
}
