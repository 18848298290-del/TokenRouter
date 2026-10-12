package apikey

import (
	"context"
)

// RequestMetadata stores gateway entry data; Set flags distinguish absent fields from empty values.
type RequestMetadata struct {
	ForcePlatform      string
	ForcePlatformSet   bool
	InboundEndpoint    string
	InboundEndpointSet bool
}

type requestMetadataKey struct{}

func WithRequestMetadata(ctx context.Context, value RequestMetadata) context.Context {
	return context.WithValue(ctx, requestMetadataKey{}, value)
}

// WithForcePlatform stores the required platform and marks it present.
func WithForcePlatform(ctx context.Context, platform string) context.Context {
	value := RequestMetadataFromContext(ctx)
	value.ForcePlatform, value.ForcePlatformSet = platform, true
	return WithRequestMetadata(ctx, value)
}

// WithInboundEndpoint stores an endpoint already normalized by the caller.
func WithInboundEndpoint(ctx context.Context, endpoint string) context.Context {
	value := RequestMetadataFromContext(ctx)
	value.InboundEndpoint, value.InboundEndpointSet = endpoint, true
	return WithRequestMetadata(ctx, value)
}

func RequestMetadataFromContext(ctx context.Context) RequestMetadata {
	if ctx == nil {
		return RequestMetadata{}
	}
	v, _ := ctx.Value(requestMetadataKey{}).(RequestMetadata)
	return v
}

func ForcePlatformFromContext(ctx context.Context) (string, bool) {
	v := RequestMetadataFromContext(ctx)
	return v.ForcePlatform, v.ForcePlatformSet
}
