package ws

import "context"

// ImagePolicy 保存当前报文适用的图片资格规则。
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

// RequestPort 每次只调用一个既有解析/策略能力，完整处理顺序由 Normalize 拥有。
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

// RequestNormalizer 保存会话模型状态，按收到的请求帧执行规范化。
type RequestNormalizer struct {
	State   *IngressState
	Options NormalizeOptions
	Port    RequestPort
}
