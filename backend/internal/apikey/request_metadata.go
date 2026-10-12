package apikey

import (
	"context"
)

// Gateway entry readers record platform and endpoint values before services
// inspect the request. Each presence flag distinguishes an omitted input from a
// supplied empty string, and the setters retain the other metadata fields when
// replacing one value.

// RequestMetadata records gateway entry values and their independent presence
// flags.
type RequestMetadata struct {
	// ForcePlatform and ForcePlatformSet describe platform selection; InboundEndpoint
	// and InboundEndpointSet describe the gateway endpoint. Each Set flag records
	// presence independently of whether its paired string contains any text.
	ForcePlatform      string // required upstream platform
	ForcePlatformSet   bool   // platform input presence
	InboundEndpoint    string // normalized gateway endpoint
	InboundEndpointSet bool   // endpoint input presence
}

// requestMetadataKey identifies entry metadata stored in a standard request
// context.
type requestMetadataKey struct{}

// WithRequestMetadata stores value as a replacement for the existing request
// metadata. The value is copied into the context, and callers supply a nonnil
// context and set each presence flag themselves.
func WithRequestMetadata(ctx context.Context, value RequestMetadata) context.Context {
	return context.WithValue(ctx, requestMetadataKey{}, value)
}

// WithForcePlatform records platform and marks that input present for service
// readers. Empty platform text is a present value; existing endpoint metadata
// is retained, and callers supply a nonnil context.
func WithForcePlatform(ctx context.Context, platform string) context.Context {
	value := RequestMetadataFromContext(ctx)
	value.ForcePlatform, value.ForcePlatformSet = platform, true
	return WithRequestMetadata(ctx, value)
}

// WithInboundEndpoint records the endpoint and marks that input present for
// service readers. The caller supplies normalized text and a nonnil context; an
// empty endpoint remains distinguishable from an omitted input.
func WithInboundEndpoint(ctx context.Context, endpoint string) context.Context {
	value := RequestMetadataFromContext(ctx)
	value.InboundEndpoint, value.InboundEndpointSet = endpoint, true
	return WithRequestMetadata(ctx, value)
}

// RequestMetadataFromContext returns entry metadata from ctx, or its zero value
// when unavailable. Nil contexts, absent values, and values of another type all
// report both entry fields as absent.
func RequestMetadataFromContext(ctx context.Context) RequestMetadata {
	if ctx == nil {
		return RequestMetadata{}
	}
	v, _ := ctx.Value(requestMetadataKey{}).(RequestMetadata)
	return v
}

// ForcePlatformFromContext returns the recorded platform and its independent
// presence flag. An empty platform can still be present; nil contexts and
// missing metadata return empty text with false for the flag.
func ForcePlatformFromContext(ctx context.Context) (string, bool) {
	v := RequestMetadataFromContext(ctx)
	return v.ForcePlatform, v.ForcePlatformSet
}
