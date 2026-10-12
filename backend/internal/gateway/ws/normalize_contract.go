package ws

import "context"

// ImagePolicy contains the image eligibility rules for the current payload.
type ImagePolicy struct {
	Allowed  bool
	Explicit string
	Bridge   bool
}
type ImageBilling struct {
	Model     string
	SizeTier  string
	InputSize string
}

// RequestPort supplies parsing and policy operations in the order chosen by Normalize.
type RequestPort interface {
	ModelAttributes(context.Context, int, string, []byte) ([]byte, error)
	PromptReplace(context.Context, []byte) []byte
	Mutate([]byte, string, string) ([]byte, error)
	RequestedEffort([]byte, string) *string
	Reasoning([]byte, string) ([]byte, error)
	IsLite([]byte) bool
	Compatibility([]byte, bool) ([]byte, bool, error)
	AliasTools([]byte) ([]byte, error)
	Models(int, string, []byte) (string, string, error)
	ClassifyPrevious(string) string
	TurnMetadata() string
	ScopeIdentity([]byte) ([]byte, bool, error)
	NormalizeLite([]byte) ([]byte, error)
	ImagePolicy(context.Context, []byte) ImagePolicy
	BridgeImages([]byte) ([]byte, error)
	StripImages([]byte) ([]byte, bool, error)
	StripSparkImages([]byte, string) ([]byte, bool, error)
	ImageIntent(string, string, []byte) ([]byte, bool, bool)
	FeatureDenied()
	ImageDeniedMessage() string
	ImageBilling([]byte, string) (ImageBilling, error)
	FastPolicy(context.Context, int, string, []byte, bool) ([]byte, *PolicyBlocked, error)
	PolicyDenied()
	BlockedEvent(*PolicyBlocked) []byte
	WriteBlocked(context.Context, []byte)
	CloseError(int, string, error) error
	Log(string)
}
type NormalizeOptions struct {
	ProviderID      int64
	OAuth           bool
	ForceHTTPBridge bool
}

// RequestNormalizer keeps session model state and normalizes each received request frame.
type RequestNormalizer struct {
	State   *IngressState
	Options NormalizeOptions
	Port    RequestPort
}
