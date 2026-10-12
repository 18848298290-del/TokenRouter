package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/batchimage"
)

// newBatchProviderRegistryForTest registers Gemini API and Vertex batch providers.
func newBatchProviderRegistryForTest() *batchimage.Registry[BatchImageProvider] {
	return batchimage.NewRegistry[BatchImageProvider](NewGeminiAPIBatchImageProvider(nil), NewVertexBatchImageProvider(VertexBatchImageProviderOptions{}, nil, nil, nil))
}
