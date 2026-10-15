package ws

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// entry_contract.go defines the identities, operations, and per-turn callbacks
// used by WebSocket entry execution. Entry data separates validated Key and
// subject facts from provider selection, turn callbacks, and completion inputs,
// letting RunEntry retain the current logical request across attempts.

// EntryKey supplies validated Key access data retained by upgraded connection
// orchestration.
type EntryKey struct {
	ID      int64  // authenticated API Key identifier
	UserID  int64  // API Key owner identifier
	GroupID *int64 // selected request group identifier
	// ModelMapping is read while rewriting tool model fields. RunEntry retains
	// this map by reference, so the connection caller keeps its entries stable
	// during execution.
	ModelMapping map[string]string // API Key additional-model redirects
	// FastModePolicy initializes the attempt's policy value. RefreshFastPolicy
	// enables per-turn reads; failed reads retain the most recently accepted
	// policy in that attempt.
	FastModePolicy    string // initial authenticated Fast policy
	RefreshFastPolicy bool   // per-turn Fast refresh enabled
	// Group supplies reasoning caps and mappings for this connection's hooks. The
	// input is shared, so callers keep it stable while execution reads those
	// fields.
	Group *EntryGroup // authenticated group policy view
}

// EntryGroup supplies the group policy values inherited by per-turn execution
// hooks.
type EntryGroup struct {
	Platform string // group compatible request platform
	// MaxReasoningEffort, MaxReasoningEffortOverLimit, and ReasoningEffortMappings
	// define inherited reasoning policy. These values are passed together to each
	// selected attempt's normalization hooks.
	MaxReasoningEffort          string                           // group requested effort ceiling
	MaxReasoningEffortOverLimit string                           // group excessive effort action
	ReasoningEffortMappings     []routing.ReasoningEffortMapping // group effort rewrite rules
	ImagesAllowed               bool                             // group Responses image permission
}

// EntrySubject identifies the admission user and configured user concurrency
// limit.
type EntrySubject struct {
	UserID      int64 // user admission subject identifier
	Concurrency int   // user concurrent request limit
}

// EntryInput contains connection access data and first-turn execution context
// for RunEntry.
type EntryInput struct {
	// Key is required and shared for the connection lifetime. The caller completes
	// authentication and group selection before RunEntry receives this access
	// view.
	Key     *EntryKey    // validated connection access view
	Subject EntrySubject // user slot admission subject
	// ClientLifecycleContext captures the client lifetime before ingress lease
	// control is added. Execution adapters use it when separating downstream
	// writes from lease-loss closure.
	ClientLifecycleContext context.Context // client lifetime before lease
	FirstTurnStartedAt     time.Time       // first frame acceptance time
	ClientIP               string          // resolved client IP address
	UserAgent              string          // client User-Agent header value
	MaxProviderSwitches    int             // allowed provider replacement count
}

// EntryProvider supplies selected provider eligibility, display name, and
// shadow status.
type EntryProvider struct {
	provider.ProviderSnapshot        // selected provider eligibility snapshot
	Name                      string // selected provider display name
	Shadow                    bool   // OAuth shadow provider marker
}

// EntrySelection carries the selected provider target and its admission
// resource outcome.
type EntrySelection struct {
	Provider *EntryProvider // selected provider eligibility view
	// Acquired and ReleaseFunc describe a slot already reserved during selection.
	// Otherwise WaitPlan permits RunEntry to try provider admission before
	// credential and forwarding work.
	Acquired    bool                        // provider slot already acquired
	ReleaseFunc func()                      // selected slot release callback
	WaitPlan    *scheduler.ProviderWaitPlan // optional provider admission plan
	// Target is required when Provider is present. The adapter binds that target
	// to the same selected provider and attempt feedback data.
	Target EntryTarget // selected provider execution operations
}

// EntryDecision records scheduler observations used by entry logging and
// continuation preparation.
type EntryDecision struct {
	Layer          string // scheduler selection layer label
	CandidateCount int    // reported scheduler candidate count
	// StickyPreviousHit controls first-frame continuation retention. RunEntry
	// removes a movable previous_response_id when selection has no matching
	// prior-response affinity hit.
	StickyPreviousHit bool // continuation provider affinity found
}

// EntryFailure carries provider-failover presentation and feedback choices
// extracted from an error.
type EntryFailure struct {
	Err        error // underlying provider failure marker
	StatusCode int   // classified upstream HTTP status
	// ReportScheduleFailure selects negative feedback before release. RetryNext
	// separately permits replacement; RunEntry still checks cancellation, the
	// switch budget, and OAuth 429 stopping policy.
	ReportScheduleFailure bool // provider health failure feedback
	RetryNext             bool // replacement provider attempt permitted
}

// EntryClose records a recognized client close instruction for entry error
// presentation.
type EntryClose struct {
	Status int    // WebSocket close status code
	Reason string // client close reason text
	// Present reports recognition of the gateway close instruction. Status and
	// Reason are forwarded on recognized exits; clean untyped client exits use
	// normal closure.
	Present bool // typed close instruction recognized
}

// EntryCyberSnapshot retains prompt excerpt and moderation input for a turn
// warning.
type EntryCyberSnapshot struct {
	Excerpt string                            // captured client prompt excerpt
	Input   moderation.ContentModerationInput // captured content moderation input
}

// EntryHooks supplies the callback sequence used by one selected provider
// attempt.
type EntryHooks struct {
	// ContextForTurn shares one logical request's telemetry capture across
	// retries. The turn number is local to this selected provider attempt.
	ContextForTurn func(int) context.Context // logical turn context resolver
	// ClientLifecycleContext retains the client lifetime captured before ingress
	// lease control. Execution adapters can use it to separate response writes
	// from lease-loss cancellation while entry keeps its control context.
	ClientLifecycleContext context.Context // client lifetime before lease
	// InitialRequestModel retains the first entry model after Key redirection.
	// Adapters can use it for omitted first-frame models before resolving group
	// and provider mappings.
	InitialRequestModel string // initial redirected request model
	// InitialTurnStartedAt carries first-frame acceptance into dedicated
	// passthrough. A zero value lets that session record its own initial time;
	// sequential ingress chooses each loop's start time separately.
	InitialTurnStartedAt time.Time // first frame acceptance time
	// MaxReasoningEffort, MaxReasoningEffortOverLimit, and ReasoningEffortMappings
	// supply the inherited group reasoning rules. Normalization evaluates them
	// against the client model for each frame.
	MaxReasoningEffort          string                           // connection group effort ceiling
	MaxReasoningEffortOverLimit string                           // connection excessive effort action
	ReasoningEffortMappings     []routing.ReasoningEffortMapping // connection effort rewrite rules
	// ResolveFastModePolicy supplies the latest usable attempt policy for each
	// turn. Entry's callback keeps its accepted value when RefreshFast cannot
	// produce a usable replacement.
	ResolveFastModePolicy func(int) string // current attempt Fast policy reader
	// ResolveRoutingModel rechecks this turn's redirected model, image intent,
	// and selected-provider eligibility. A failure returns a local close
	// instruction before that frame is forwarded.
	ResolveRoutingModel func(int, string, []byte) (string, error) // per-turn eligible model resolver
	// TurnStarted creates observation state for the logical request, including
	// turns later rejected at admission. Sequential local retries can report
	// another start for the same turn; this callback does not confirm forwarding.
	TurnStarted func(int, time.Time) // accepted turn notification callback
	// BeforeTurn acquires later-turn resources and reauthorizes after user
	// admission. BeforeRequest performs later payload checks; execution adapters
	// choose their relative timing and skip repeated admission during local
	// retries.
	BeforeTurn    func(int) error                                   // per-turn slot admission callback
	BeforeRequest func(int, []byte, string, string) ([]byte, error) // later request validation callback
	// OnUpstreamError handles observed risk evidence from this turn before
	// client response restoration. Dedicated relay can invoke it independently
	// of client admission, so entry protects its per-turn evidence maps.
	OnUpstreamError func(int, string, int, []byte, string) // per-turn upstream warning observer
	// AfterTurn releases turn resources and records terminal outcomes or final
	// failures. Executing targets report each current turn, including local
	// warmups; entry retains Key admission across classified provider retries.
	AfterTurn func(TurnCapture) // turn outcome completion callback
}

// EntryTarget performs execution and feedback operations for one selected
// provider.
type EntryTarget interface {
	// MappedModel resolves the provider model used by scheduling and usage
	// feedback.
	MappedModel(string) string
	// Report records the outcome for the supplied scheduling model and optional
	// first-token milliseconds. Nil denotes unobserved TTFT; RunEntry forwards
	// the selected turn's result pointer after excluding local warmup completion.
	Report(string, bool, *int)
	// Switched records replacement of this selection in scheduler attempt
	// statistics.
	Switched()
	// Stop429 evaluates whether OAuth 429 state ends the replacement-provider
	// loop. The caller supplies the incremented switch count and shared request
	// state after excluding the failed provider from further selection.
	Stop429(int, int, *failover.OAuth429State) bool
	// Credential obtains and retains credentials for this selected execution
	// target. RunEntry calls it after provider admission and before client policy
	// checks; failures can enter the configured provider failover path.
	Credential(context.Context) error
	// EnforceClient checks the selected provider's allowed client and fingerprint
	// policy against the first request. A failure prevents running that provider
	// and becomes a local client close response.
	EnforceClient(context.Context, []byte) error
	// ResolveRouting validates model eligibility and pricing for this provider and
	// turn. The context carries the current route plan and image intent;
	// successful output supplies the group/provider routing model used by
	// execution.
	ResolveRouting(context.Context, string, bool) (string, error)
	// Warning records upstream risk evidence with the captured prompt and
	// moderation input for its turn.
	Warning(context.Context, string, int, []byte, string, EntryCyberSnapshot)
	// RecordMarked processes the saved risk marker for this turn and reports
	// whether it was handled. RunEntry uses true to block later turns; handling
	// does not establish successful warning persistence or background submission.
	RecordMarked(context.Context, string, bool, []byte, routing.PricingUsageFields, string) bool
	// UpdateUsage publishes OAuth usage observations from the completed response
	// headers.
	UpdateUsage(context.Context, map[string][]string)
	// PrepareCompletion captures provider, access, result, mapping, request, and
	// pricing-time data for background settlement. It returns a prepared
	// completion input whose referenced source values must remain valid until
	// capture is complete.
	PrepareCompletion(context.Context, *ForwardResult, TurnCapture, string, routing.GroupMappingResult, []byte, bool) *completion.Input
	// BeginPreemption optionally registers ownership for the selected provider and
	// request session. When armed is true, RunEntry adopts the returned context
	// and invokes cleanup on connection exit, retaining that registration through
	// nested forwarding.
	BeginPreemption(context.Context, []byte) (context.Context, func(), bool)
	// Run executes the selected provider using its credentials, upgraded socket,
	// first payload, and turn hooks. The target reports turn outcomes through
	// AfterTurn and returns a final execution or connection error.
	//
	// Per-turn hooks keep admission and completion attached to the logical request
	// even when this execution spans multiple frames. A later-turn provider
	// failover needs CurrentTurnFailoverError with a self-contained current
	// payload; otherwise entry would only have the initial attempt bytes. A nil
	// error ends provider iteration for this connection.
	Run(context.Context, ClientSocket, []byte, *EntryHooks) error
	// LogFailure records selected-provider diagnostics for a final unhandled
	// execution error.
	LogFailure(error)
}

// EntryCompletion records captured WebSocket usage through the shared
// completion service.
type EntryCompletion interface {
	// Record settles and records prepared turn input with the supplied background
	// context. The boolean selects OpenAI mode; the shared recorder can route
	// captured native usage through its alternate pricing path.
	Record(context.Context, *completion.Input, bool) error
}

// EntryPorts supplies HTTP and application operations required by upgraded
// connection orchestration.
type EntryPorts interface {
	// Logger returns the structured logger currently bound to this connection
	// entry.
	Logger() EntryLogger
	// SetLogger binds the derived logger used after first-frame observation adds
	// request fields.
	SetLogger(EntryLogger)
	// ClassifyPrevious identifies the supplied continuation spelling for
	// validation and diagnostics.
	ClassifyPrevious(string) string
	// Platform returns the request platform used for compatible provider
	// selection.
	Platform() string
	// Prompt applies user prompt replacement before first-frame model parsing and
	// moderation.
	Prompt(context.Context, []byte) []byte
	// Redirect applies API Key model redirection and records its trace in the
	// returned context. RunEntry keeps the client identifier separately, then uses
	// the redirected model for moderation and group planning.
	Redirect(context.Context, string) (context.Context, string)
	// BindContext attaches the derived entry context to the owning HTTP request.
	BindContext(context.Context)
	// ObserveFirst records the client model and WS request type for Ops
	// observation.
	ObserveFirst(string)
	// CaptureCyber captures prompt and moderation evidence from the supplied
	// request body.
	CaptureCyber([]byte) EntryCyberSnapshot
	// Moderate evaluates the request model and payload for local content
	// rejection. A nil or unblocked decision allows entry to continue; a blocked
	// decision is presented and closed before upstream provider acquisition.
	Moderate(context.Context, string, []byte) *moderation.Decision
	// ModerationError writes the WS presentation of a local content moderation
	// rejection.
	ModerationError(context.Context, *moderation.Decision)
	// BlockedSession returns the matched explicit or derived blocked-session
	// identity, or empty when none matches. Entry presents and closes a nonempty
	// match before provider selection, preserving the identity for its Ops record.
	BlockedSession(context.Context, []byte) string
	// BlockedError attempts the client error event for an already-blocked risk
	// session.
	BlockedError(context.Context)
	// BlockedMessage returns the configured client reason used to close a blocked
	// session.
	BlockedMessage() string
	// BlockedOps submits observation of a blocked request with model and session
	// identity.
	BlockedOps(string, string)
	// Plan attaches the current group route plan and returns its model mapping.
	// Entry calls it independently for first admission, later-turn routing, and
	// completion so each phase receives the applicable model source rules.
	Plan(context.Context, string) (context.Context, routing.GroupMappingResult)
	// ImageIntent applies group-model intent mapping and returns mapped body,
	// routing model, and intent result. Entry tests explicit image capability
	// against that group model while retaining upstream model resolution for the
	// selected target.
	ImageIntent(string, []byte, routing.GroupMappingResult) ([]byte, string, bool)
	// ImageContext adds the model-level image admission scope to the supplied
	// context.
	ImageContext(context.Context) context.Context
	// ExplicitImage reports whether the supplied model and body request explicit
	// image generation.
	ExplicitImage(string, []byte) bool
	// ImagesAllowed reports the current group's Responses image feature
	// permission.
	ImagesAllowed() bool
	// ImageDeniedMessage returns the client reason for a denied Responses image
	// request.
	ImageDeniedMessage() string
	// PolicyDenied marks local policy rejection in this request's Ops observation.
	PolicyDenied()
	// FeatureDenied marks local feature rejection in this request's Ops
	// observation.
	FeatureDenied()
	// AcquireKey reserves this turn's API Key request allowance and returns its
	// lease context and release callback. Success supplies a usable release;
	// errors carry a CloseInfo-readable instruction. Entry retains the reservation
	// through provider retries and releases it after terminal completion.
	AcquireKey(context.Context) (context.Context, func(), error)
	// AcquireUser tries the user concurrency slot and returns its release
	// callback, acquisition flag, or failure. Entry owns successful release
	// between turns and on exit; false acquisition closes the client as busy.
	AcquireUser(context.Context) (func(), bool, error)
	// AcquireProvider tries the selected provider's slot with the supplied limit.
	// Entry owns successful release before provider replacement and between turns;
	// false acquisition is a busy result, separate from an operation error.
	AcquireProvider(context.Context, int64, int) (func(), bool, error)
	// WrapRelease adds context-triggered cleanup to an acquired slot callback.
	// Entry also invokes the returned release on normal completion; the adapter
	// supplies safe repeated-release behavior for those competing cleanup paths.
	WrapRelease(context.Context, func()) func()
	// Eligibility checks initial funding after user-slot acquisition and
	// subscription loading. A returned error rejects the connection before Key
	// request admission and provider selection; later turns use AuthorizeTurn to
	// refresh identity and funding.
	Eligibility(context.Context) error
	// AuthorizeTurn reauthenticates current identity, protocol permission, and
	// funding after later user admission. It refreshes the request Key used for
	// admission while already admitted turn completion retains its captured
	// connection attribution.
	AuthorizeTurn(context.Context) error
	// LoadSubscription retains the authenticated subscription used by initial
	// funding and completion capture.
	LoadSubscription()
	// SessionHash derives the scheduler session identity from headers, payload,
	// and fallback seed. The supplied seed distinguishes group, user, and Key when
	// an explicit session identity is unavailable.
	SessionHash([]byte, string) string
	// ExplicitHash returns the hash derived from explicit client session identity
	// when available. Empty means no explicit session was supplied, so entry skips
	// that isolation check while retaining its separately derived scheduler hash.
	ExplicitHash([]byte) string
	// Isolate verifies that the supplied explicit or continuation session can be
	// used by this connection.
	Isolate(context.Context, string, string) error
	// IsolationError attempts the client event presenting a rejected
	// session-isolation check.
	IsolationError(context.Context, error)
	// IsolationReason selects the close reason for a rejected session-isolation
	// check.
	IsolationReason(error) string
	// Guardian adds parent-affinity hints derived from the current request payload
	// and model.
	Guardian(context.Context, []byte, string) context.Context
	// Select chooses an eligible provider using continuation, session, model, and
	// excluded-provider state. Capability and move flags constrain transport
	// routing; the result distinguishes a reserved slot from a wait plan and
	// supplies scheduler observations.
	Select(context.Context, string, string, string, map[int64]struct{}, bool, bool, string) (*EntrySelection, EntryDecision, error)
	// SelectionLogError converts selection failure details for the compatible
	// request platform's diagnostics.
	SelectionLogError(error, string) error
	// BindSticky records session-to-provider affinity after provider admission
	// succeeds. A failed write is logged while entry continues using that selected
	// provider, because affinity storage is advisory for subsequent requests.
	BindSticky(context.Context, string, int64) error
	// RefreshFast reads and normalizes current API Key Fast policy for a turn. Its
	// boolean reports a usable refreshed value; false leaves the attempt's last
	// accepted policy available to ResolveFastModePolicy.
	RefreshFast(context.Context) (string, bool)
	// Failover extracts the underlying provider failure marker and its replacement
	// and feedback choices. The boolean reports recognized failover state; entry
	// separately checks cancellation, output-safe replay, and the configured
	// switch budget.
	Failover(error) (*EntryFailure, bool)
	// CloseFailover presents the exhausted provider failure and closes the client
	// socket.
	CloseFailover(*EntryFailure)
	// Close sends the selected WebSocket status and reason to the upgraded client.
	Close(int, string)
	// CloseError constructs the adapter's typed client close instruction around
	// its cause.
	CloseError(int, string, error) error
	// CloseInfo extracts a recognized gateway close instruction for entry
	// presentation. Present distinguishes typed instructions from ordinary errors;
	// callers use that distinction when choosing clean-client or proxy-failure
	// close handling.
	CloseInfo(error) EntryClose
	// SessionPreempted reports whether the error represents replacement by a newer
	// scoped session.
	SessionPreempted(error) bool
	// RemovePrevious removes continuation metadata from a movable first request
	// for another provider. The returned payload follows the adapter's best-effort
	// protocol mutation rule; entry retains the initial bytes used for its attempt
	// history.
	RemovePrevious([]byte) []byte
	// LocalPolicyError recognizes an error requiring local business-limit
	// observation before close.
	LocalPolicyError(error) bool
	// ReportFailure reports whether the final error permits negative provider
	// scheduling feedback.
	ReportFailure(error) bool
	// EndedByClient recognizes a clean client exit using the adapter's close
	// attribution rules.
	EndedByClient(error) bool
	// ClearCyber clears the risk marker and Ops state after processing one turn.
	ClearCyber()
	// CompletionRecorder returns the shared recorder used by captured background
	// turn tasks.
	CompletionRecorder() EntryCompletion
	// CompletionObserver captures a completion-failure reporting function
	// independent of the HTTP request lifetime. Entry invokes it with prepared
	// provider and request IDs when the shared recorder returns an error.
	CompletionObserver() func(int64, string, error)
	// SubmitCompletion schedules the captured turn task using the result's image
	// count to select submission policy. The task already holds its logical
	// request context and completion input; submission may choose an inline path
	// under its configured pool policy.
	SubmitCompletion(*ForwardResult, func(context.Context))
}

// EntryField identifies a log key and one typed value for the entry logger.
type EntryField struct {
	Key string // structured log field name
	// Text, Integer, Flag, and Err supply alternatives selected by Kind. The HTTP
	// logger reads string 1, integer 2, boolean 3, or error 4 and ignores other
	// kinds.
	Text    string // string log value storage
	Integer int64  // integer log value storage
	Flag    bool   // boolean log value storage
	Err     error  // error log value storage
	Kind    uint8  // log value selector code
}

// EntryLogger records structured diagnostics for connection orchestration and
// completion failure observation.
type EntryLogger interface {
	// With returns a derived entry logger carrying the supplied structured fields.
	With(...EntryField) EntryLogger
	// Info records an informational message with the supplied structured log
	// fields.
	Info(string, ...EntryField)
	// Warn records a warning message with the supplied structured log fields.
	Warn(string, ...EntryField)
	// Debug records a debug message with the supplied structured log fields.
	Debug(string, ...EntryField)
	// Error records an error message with the supplied structured log fields.
	Error(string, ...EntryField)
}

// EntryString builds a string field for the structured entry logger adapter.
func EntryString(key, value string) EntryField { return EntryField{Key: key, Text: value, Kind: 1} }

// EntryInt widens an int value into the entry logger integer field.
func EntryInt(key string, value int) EntryField {
	return EntryField{Key: key, Integer: int64(value), Kind: 2}
}

// EntryInt64 builds an integer field for the structured entry logger adapter.
func EntryInt64(key string, value int64) EntryField {
	return EntryField{Key: key, Integer: value, Kind: 2}
}

// EntryBool builds a boolean field for the structured entry logger adapter.
func EntryBool(key string, value bool) EntryField { return EntryField{Key: key, Flag: value, Kind: 3} }

// EntryError builds the error-kind field using the adapter's fixed error key.
func EntryError(err error) EntryField { return EntryField{Key: "error", Err: err, Kind: 4} }
