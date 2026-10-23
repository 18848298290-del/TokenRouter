package protocol

// image_mode.go defines the transport modes accepted by an Images upstream.
// A provider declares these capabilities separately for generation and editing.
// The client response mode remains independent so adapters can convert a single
// upstream execution into the representation requested by the client.

const (
	// ImageModeBoth follows the client, while the single-mode values select the
	// only request representation accepted by the configured Images endpoint.
	ImageModeBoth         ImageStreamMode = "both"
	ImageModeNonStreaming ImageStreamMode = "non_streaming"
	ImageModeStreaming    ImageStreamMode = "streaming"
)

// ImageStreamMode identifies the accepted request modes of one Images endpoint.
type ImageStreamMode string

// Valid reports whether the mode is a supported stored capability value.
func (m ImageStreamMode) Valid() bool {
	return m == ImageModeBoth || m == ImageModeNonStreaming || m == ImageModeStreaming
}

// UpstreamStream selects an upstream mode from the client request and capability.
// Empty mode values follow the client before request construction, matching the
// default used when older providers have no endpoint mode configuration.
func (m ImageStreamMode) UpstreamStream(clientStream bool) bool {
	switch m {
	case ImageModeStreaming:
		return true
	case ImageModeNonStreaming:
		return false
	default:
		return clientStream
	}
}
