package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// A hydrated group must carry the saved image-generation permission into
// routing reads. The fixture sets that permission on an Ent entity and checks
// its converted value, protecting the administrator-controlled flag during
// group mapping.

// TestGroupEntityToService_PreservesImageGenerationControls maps a stored group
// with image generation enabled. The result must be nonnil and retain that
// permission so routing can read the saved flag.
func TestGroupEntityToService_PreservesImageGenerationControls(t *testing.T) {
	group := &dbent.Group{
		ID:   1,
		Name: "openai-images",

		Status:               routing.StatusActive,
		RateMultiplier:       1,
		AllowImageGeneration: true,
	}

	got := GroupFromEnt(group)
	require.NotNil(t, got)
	require.True(t, got.AllowImageGeneration)
}
