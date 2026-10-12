package ws

import "context"

// A later response.create can omit its model while retaining the session's last
// accepted client model. Normalization resolves that client choice through
// group and provider rules before model attributes, image eligibility, and Fast
// policy use their respective identifiers; provider-scoped identity is captured
// separately for current-turn replay.

// ImagePolicy combines current group permission with explicit-tool and bridge
// policy choices.
type ImagePolicy struct {
	// Allowed and Explicit constrain Bridge at the HTTP adapter: bridging requires
	// permitted images and an explicit policy other than strip. Normalize also
	// rejects forbidden explicit image intent.
	Allowed  bool   // group Responses image permission
	Explicit string // explicit image tool action
	Bridge   bool   // Codex image bridge enabled
}

// ImageBilling records the pricing model and size labels derived for an
// image-intent turn.
type ImageBilling struct {
	Model     string // resolved image billing model
	SizeTier  string // normalized image pricing tier
	InputSize string // request image size label
}

// RequestPort supplies policy and payload operations applied in
// RequestNormalizer order.
type RequestPort interface {
	// ModelAttributes applies request parameters for the resolved upstream model
	// and logical turn context. It runs after Models determines eligibility;
	// failures stop normalization before the accepted session model is updated.
	ModelAttributes(context.Context, int, string, []byte) ([]byte, error)
	// PromptReplace applies user prompt replacement before parsing and later
	// policy steps.
	PromptReplace(context.Context, []byte) []byte
	// Mutate writes the supplied string at a supported protocol field path and
	// returns updated request bytes. Its errors become invalid-payload closes; the
	// HTTP adapter can rebuild supported fields through decoded-map compatibility
	// handling.
	Mutate([]byte, string, string) ([]byte, error)
	// RequestedEffort captures canonical client effort before reasoning policy
	// rewrites the payload. Nil represents omitted or unrecognized effort, and a
	// supplied none remains available separately from the accepted reporting
	// label.
	RequestedEffort([]byte, string) *string
	// Reasoning applies configured group effort rules using the client model,
	// including the retained model for omitted-model frames. It returns updated
	// bytes or a policy error that prevents this frame from being accepted.
	Reasoning([]byte, string) ([]byte, error)
	// IsLite reports whether client metadata requests Responses Lite handling for
	// this frame.
	IsLite([]byte) bool
	// Compatibility normalizes fields required by the selected provider's
	// Responses protocol and Lite mode. Its changed flag controls replacing the
	// supplied bytes; an error stops this turn before any upstream forwarding.
	Compatibility([]byte, bool) ([]byte, bool, error)
	// AliasTools rewrites OAuth-reserved tool names and retains reverse mappings
	// for response restoration. Normalize invokes it for OAuth WS forwarding when
	// HTTP bridge is not forced; failures close the local turn.
	AliasTools([]byte) ([]byte, error)
	// Models resolves the eligible routing model and actual provider model for the
	// supplied client model. The turn number and payload carry per-turn capability
	// intent; errors prevent model-dependent attributes and forwarding.
	Models(int, string, []byte) (string, string, error)
	// ClassifyPrevious labels response versus message ID spellings for
	// continuation validation.
	ClassifyPrevious(string) string
	// TurnMetadata returns the inbound turn metadata header for this connection's
	// request normalization. Empty leaves payload metadata untouched; a nonempty
	// result is written to client_metadata after model attributes are applied.
	TurnMetadata() string
	// ScopeIdentity rewrites identity metadata into the selected provider
	// namespace and reports whether bytes changed. Normalize saves pre-scope
	// request bytes separately so replacement-provider replay can rebuild identity
	// for its new target.
	ScopeIdentity([]byte) ([]byte, bool, error)
	// NormalizeLite applies provider-specific Lite fields after identity scoping.
	// It returns accepted bytes or an error; required Lite parameters are restored
	// after earlier compatibility processing may have removed them.
	NormalizeLite([]byte) ([]byte, error)
	// ImagePolicy returns current group permission, explicit image-tool action,
	// and bridge eligibility.
	ImagePolicy(context.Context, []byte) ImagePolicy
	// BridgeImages injects and normalizes the permitted Codex image tool, choice,
	// and instructions in request bytes. Normalize invokes it only when
	// ImagePolicy enables bridging; failures prevent forwarding that payload.
	BridgeImages([]byte) ([]byte, error)
	// StripImages removes explicit image tool declarations selected for strip
	// policy. Its changed flag controls payload replacement; normalization
	// continues image-intent checks on the stripped body while errors close the
	// turn.
	StripImages([]byte) ([]byte, bool, error)
	// StripSparkImages removes image declarations incompatible with the resolved
	// Spark model. Its changed flag controls replacing the payload; the policy
	// check uses upstreamModel after client and group mapping have been resolved.
	StripSparkImages([]byte, string) ([]byte, bool, error)
	// ImageIntent inspects image eligibility using the routing model while
	// preserving the actual upstream model in returned request bytes. It
	// separately reports image intent and explicit intent so group denial can
	// distinguish their cases.
	ImageIntent(string, string, []byte) ([]byte, bool, bool)
	// FeatureDenied marks a local group feature rejection for request observation.
	FeatureDenied()
	// ImageDeniedMessage returns the client explanation for forbidden Responses
	// image intent.
	ImageDeniedMessage() string
	// ImageBilling resolves image pricing and size labels from the intent body and
	// routing model. It is called only when image intent is detected, and an error
	// rejects the turn before Fast policy and forwarding.
	ImageBilling([]byte, string) (ImageBilling, error)
	// FastPolicy applies current turn tier rules against the resolved upstream
	// model. Its separate blocked result lets Normalize mark policy denial and
	// attempt an error event before returning a close instruction; encoding
	// failures remain ordinary errors.
	FastPolicy(context.Context, int, string, []byte, bool) ([]byte, *PolicyBlocked, error)
	// PolicyDenied marks a local Fast policy rejection in request observation.
	PolicyDenied()
	// BlockedEvent renders the selected policy rejection as client event bytes.
	// Nil bytes mean there is no event to write; Normalize still returns the
	// configured close instruction for the blocked request.
	BlockedEvent(*PolicyBlocked) []byte
	// WriteBlocked attempts a synchronous policy error write before the caller
	// returns a close instruction. The adapter owns its downstream timeout and
	// ignores write failure, so event delivery is best effort.
	WriteBlocked(context.Context, []byte)
	// CloseError constructs the adapter's typed WebSocket close instruction and
	// retained cause.
	CloseError(int, string, error) error
	// Log records the provider-specific normalization decisions supplied by the
	// caller.
	Log(string)
}

// NormalizeOptions retains selected provider flags governing OAuth and
// HTTP-bridge normalization.
type NormalizeOptions struct {
	ProviderID int64 // selected upstream provider identifier
	// OAuth and ForceHTTPBridge jointly govern reserved tool aliasing: Normalize
	// invokes AliasTools for OAuth only when HTTP bridge mode is not forced.
	OAuth           bool // OAuth tool aliasing enabled
	ForceHTTPBridge bool // HTTP bridge mode forced
}

// RequestNormalizer validates one turn using the retained session model and
// supplied policy adapter.
type RequestNormalizer struct {
	// State and Port are required. Normalize reads the prior client model for
	// missing-model frames and publishes a new model only after all policy
	// processing succeeds.
	State   *IngressState    // mutable session client-model state
	Options NormalizeOptions // provider normalization flags snapshot
	Port    RequestPort      // required normalization policy dependency
}
