package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/batchimage"
)

// Default Gemini and Vertex clients can be constructed before any upstream
// request occurs. Registry tests use those adapters' platform identities, while
// operation tests construct separate providers with injected fakes and chosen
// managed-storage options.

// newBatchProviderRegistryForTest returns a registry containing Gemini API and
// Vertex adapters. Their default clients are constructed without contacting
// upstream services; tests use the registry to check platform lookup identities.
func newBatchProviderRegistryForTest() *batchimage.Registry[BatchImageProvider] {
	return batchimage.NewRegistry[BatchImageProvider](NewGeminiAPIBatchImageProvider(nil), NewVertexBatchImageProvider(VertexBatchImageProviderOptions{}, nil, nil, nil))
}
