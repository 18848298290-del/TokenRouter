package accessview

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// group_config.go defines the group configuration shared by routing,
// authentication snapshots, and console queries.
// A group carries request policy separately from pricing and provider
// credentials. Storage hydration supplies trusted routing fields, while console
// queries can add localized names and a requestable model catalogue to the same
// value. Consumers therefore choose the policy or display fields appropriate to
// their operation.

// GroupConfig contains persisted group policy and query-specific display
// information. CloneGroupConfig detaches its declared collections and optional
// scalar pointers for retained snapshots; nested feature values follow
// GroupRoutingPolicy.Clone's supported copying cases.
type GroupConfig struct {
	Localization GroupLocalization // stored translatable group copy
	// DisplayName is resolved for a display query's language. Name remains the
	// persisted business identifier used by management and routing consumers.
	DisplayName   string
	RoutingPolicy GroupRoutingPolicy // group model and feature rules
	ID            int64              // persisted group row identifier
	Name          string             // persisted group business name
	Description   string             // persisted group description text
	// Models and ModelProtocols are populated for console catalogue queries. They
	// describe locally eligible request names and their entry protocols; requests
	// still perform current authorization and provider checks.
	Models         []string                         // resolved client request identifiers
	ModelProtocols map[string][]protocol.ProtocolID // protocols per displayed model

	SchedulerType GroupSchedulerType // basic or advanced selection
	// AdvancedSchedulerOverrides applies when SchedulerType selects advanced
	// scheduling. Unset pointer fields inherit gateway settings; validated
	// supplied values, including false and permitted zeros, take precedence during
	// parameter merging.
	AdvancedSchedulerOverrides GroupAdvancedSchedulerOverrides
	DisplayBrand               string  // administrator-selected display brand
	RateMultiplier             float64 // group user-price multiplier
	IsExclusive                bool    // restricted group access flag
	Status                     string  // persisted group availability status
	// Hydrated marks a value loaded with routing fields from trusted storage.
	// IsGroupContextValid requires this marker before accepting a group context.
	Hydrated bool
	// DuplicateOperationID identifies a persisted duplication attempt. The
	// duplication store uses it to recover the committed target group when
	// completion is retried.
	DuplicateOperationID string

	// SessionIsolationEnabled enables session.EnsureIsolation to reject a
	// user/source/hash session whose recorded first group differs from this
	// target. Gateway callers supply that identity and the target group's flag.
	SessionIsolationEnabled bool

	AllowImageGeneration      bool // legacy image-generation permission
	AllowBatchImageGeneration bool // legacy batch-generation permission

	// ClaudeCodeOnly triggers client checks before selection; FallbackGroupID
	// supplies the next group when the client is rejected. The entry reauthorizes
	// a selected target group before execution.
	ClaudeCodeOnly  bool   // Claude Code client restriction
	FallbackGroupID *int64 // client-restriction fallback group
	// FallbackGroupIDOnInvalidRequest supplies the configured target for invalid
	// Anthropic requests. Its entry consumer determines whether the specific
	// failure permits this fallback.
	FallbackGroupIDOnInvalidRequest *int64
	// UnavailableFallbackGroupID supplies the administrator's preferred target
	// when this group is unavailable. API-key admission also checks the key's
	// permission to use that fallback.
	UnavailableFallbackGroupID *int64

	// ModelRouting and ModelRoutingEnabled select preferred providers for matching
	// request models. Exact populated entries precede trailing-wildcard entries,
	// and each value retains its configured provider order. Overlapping populated
	// patterns use the first map-iteration match, so their selection has no saved
	// wildcard priority.
	ModelRouting        map[string][]int64 // preferred provider identifier lists
	ModelRoutingEnabled bool               // preferred-provider selection switch

	MCPXMLInject bool // retained legacy injection setting

	SupportedModelScopes []string // retained legacy model-family selections

	SortOrder int // administrator-selected display order

	// AllowedProtocols admits new client operations; an empty selection closes
	// those entries. ProtocolFallbacks independently selects their conversions, so
	// a conversion target need not be admitted as a client entry.
	AllowedProtocols []protocol.ProtocolID // admitted client protocol identifiers
	// ProtocolFallbacks uses a missing key for automatic conversion matching. A
	// present empty list requires native execution, and a nonempty list fixes
	// permitted targets and order.
	ProtocolFallbacks    map[protocol.ProtocolID][]protocol.ProtocolID
	ResponsesImagePolicy string // Responses image-tool policy selection
	// AllowMessagesDispatch is retained for persisted compatibility. Group
	// administration derives it from AllowedProtocols so complete protocol
	// selections govern Messages admission.
	AllowMessagesDispatch bool
	AllowLive             bool // retained legacy Live permission
	// ForceOpenAIFast represents the legacy priority request. Group administration
	// derives it from OpenAIFastPolicy when saving the mutually exclusive
	// acceleration policy.
	ForceOpenAIFast  bool
	OpenAIFastPolicy string // selected OpenAI acceleration policy

	// RequireOAuthOnly rejects runtime candidates outside the provider's OAuth or
	// Setup Token classification. Association validation and catalogue filtering
	// apply their own authentication checks around this saved requirement.
	RequireOAuthOnly bool // provider authentication restriction
	// RequirePrivacySet requires the candidate's saved privacy result to pass
	// provider.IsPrivacySet. OpenAI and Antigravity use their successful platform
	// markers; other platforms pass that check.
	RequirePrivacySet       bool                         // verified privacy setting requirement
	DefaultMappedModel      string                       // retained default model setting
	ModelsListConfig        GroupModelsListConfig        // custom displayed model selections
	AvailabilityProbeConfig GroupAvailabilityProbeConfig // active group probe settings

	// RPMLimit supplies the per-user allowance within this group when no
	// user-group override is present. Nonpositive values disable that default
	// group counter; admission still checks a positive total-user RPM limit
	// separately.
	RPMLimit int

	// MaxReasoningEffort caps ranked effort after request mappings. Empty means no
	// ceiling; the execution consumer checks the selected provider's supported
	// effort values.
	MaxReasoningEffort string
	// MaxReasoningEffortOverLimit chooses how the reasoning policy handles a
	// ranked request above MaxReasoningEffort. Empty configuration resolves to
	// downgrade during policy normalization.
	MaxReasoningEffortOverLimit string
	// ReasoningEffortMappings rewrites client effort before the group ceiling is
	// compared. Model scopes choose exact matches before prefix or suffix matches
	// and global rules.
	ReasoningEffortMappings []ReasoningEffortMapping

	CreatedAt                time.Time // group row creation time
	UpdatedAt                time.Time // last persisted group update
	ProviderCount            int64     // total associated provider count
	ActiveProviderCount      int64     // active associated provider count
	RateLimitedProviderCount int64     // rate-limited associated provider count
}

// GroupAdvancedSchedulerOverrides aliases the scheduler policy's optional group
// parameter overrides.
type GroupAdvancedSchedulerOverrides = policy.GroupAdvancedSchedulerOverrides

// GroupAvailabilityProbeConfig records the model, schedule, and attempt budget
// for probes.
type GroupAvailabilityProbeConfig struct {
	// Protocol selects a group entry for every attempt. Empty retains automatic
	// provider selection and test inference for configurations saved without it.
	Protocol        protocol.ProtocolID `json:"protocol,omitempty"`
	Enabled         bool                `json:"enabled"`                    // active probe scheduling switch
	IntervalMinutes int                 `json:"interval_minutes,omitempty"` // minutes
	ModelID         string              `json:"model_id,omitempty"`         // probe request model identifier
	Prompt          string              `json:"prompt,omitempty"`           // probe request prompt text
	TimeoutSeconds  int                 `json:"timeout_seconds,omitempty"`  // seconds
	// MaxRetries counts attempts after the first failed probe. Nil selects the
	// server default, while a supplied zero executes only the first attempt.
	MaxRetries *int   `json:"max_retries,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"` // probe request User-Agent override
}

// GroupCopy contains the group text resolved for a requested language.
type GroupCopy struct {
	DisplayName string `json:"display_name"` // localized group display name
	Description string `json:"description"`  // localized group description text
}

// GroupLocalization stores group translations independently from its persisted
// business name.
type GroupLocalization locale.Content[GroupCopy]

// GroupModelsListConfig selects the ordered custom model catalogue exposed to
// clients.
type GroupModelsListConfig struct {
	// Enabled applies the custom Models list when it contains entries; an empty
	// list leaves the catalogue unfiltered. RequestableResolver still validates
	// those names against group and provider eligibility.
	Enabled bool     `json:"enabled"`          // custom catalogue selection switch
	Models  []string `json:"models,omitempty"` // ordered custom request names
}

// ReasoningEffortMapping describes one effort rewrite and its optional
// request-model scope.
type ReasoningEffortMapping struct {
	From string `json:"from"` // requested effort to match
	To   string `json:"to"`   // mapped effort to apply
	// MatchType scopes Model matching; normalization makes a populated model
	// default to exact. An empty Model selects a global mapping regardless of the
	// supplied match type.
	MatchType string `json:"match_type,omitempty"`
	Model     string `json:"model,omitempty"` // optional request-model match pattern
}

// GroupSchedulerType names the selection strategy applied after provider
// eligibility checks.
type GroupSchedulerType string
