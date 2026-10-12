package session

import (
	"context"
	"errors"
	"time"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Live calls outlast their creation request and can be controlled by an observer
// or a sideband proxy on another instance. Records carry the creator's routing,
// lease, and identity evidence, while storage methods expose controller claims
// and closure for the Live runtime's coordination.

// Controller labels describe which Live participant holds the call; Redis transitions
// use these stored values when claiming, releasing, or closing controller ownership.
const (
	// LiveControllerPending awaits an observer or proxy controller claim.
	LiveControllerPending  = "pending"
	LiveControllerObserver = "observer" // observer holds call control
	LiveControllerProxy    = "proxy"    // sideband proxy holds control
	LiveControllerClosed   = "closed"   // call has reached closure
)

// These sentinels let Live entrypoints select responses for unavailable service,
// missing calls, identity or controller conflicts, and exhausted or denied admission.
var (
	// ErrLiveUnavailable reports missing runtime or infrastructure for Live service.
	ErrLiveUnavailable = errors.New("live is unavailable")
	// ErrLiveConcurrencyFull reports that live lease capacity is exhausted.
	ErrLiveConcurrencyFull = errors.New("live concurrency is full")
	// ErrLiveCallNotFound reports that live call lookup has no usable record.
	ErrLiveCallNotFound = errors.New("live call not found")
	// ErrLiveIdentityMismatch reports that requester differs from stored call identity.
	ErrLiveIdentityMismatch = errors.New("live call identity mismatch")
	// ErrLiveControllerChanged reports that another participant changed call control.
	ErrLiveControllerChanged = errors.New("live controller changed")
	// ErrLiveClientPolicyDenied reports that provider policy rejects the Live client.
	ErrLiveClientPolicyDenied = errors.New("live client is denied by provider policy")
)

// LiveAttestationUnavailableError carries the diagnostic reason attestation is unavailable.
type LiveAttestationUnavailableError struct {
	Reason string // optional attestation failure detail
}

// LiveCallRequest uses the shared OpenAI Live creation request representation.
type LiveCallRequest = wire.LiveCallRequest

// LiveCallIdentity carries authenticated creator identity and inbound routing evidence.
// Pointer IDs and ModelMapping retain the supplied references; HTTP creation builds
// a map snapshot, and Creator copies that map into its call record.
type LiveCallIdentity struct {
	// APIKeyID identifies the credential, ActorUserID identifies its user owner,
	// and UserID identifies the authenticated funding subject used for Live lease and lookup checks.
	APIKeyID    int64 // authenticated API Key identifier
	ActorUserID int64 // API Key owner user identifier
	UserID      int64 // payer or authenticated subject identifier
	// TeamID, GroupID, and SubscriptionID retain optional identity pointers here;
	// Creator converts each absent pointer to zero when building the stored call record.
	TeamID          *int64 // optional creator team identifier
	GroupID         *int64 // optional selected group identifier
	SubscriptionID  *int64 // optional selected subscription identifier
	UserAgent       string // inbound client User-Agent value
	Originator      string // inbound Codex originator value
	IPAddress       string // resolved inbound client address
	InboundEndpoint string // inbound protocol endpoint name
	// ModelMapping retains its supplied map; Creator clones these rules into
	// the record so later Live rewrites use the creation-time redirect snapshot.
	ModelMapping map[string]string // API Key model redirect rules
}

// LiveCallRecord carries creation evidence and the Live call's controller state.
// Creator fills it before SaveLiveCall; Redis restores the fields it persists for
// later lookup and controller handling, while ActorUserID and TeamID remain zero
// on that adapter's read path.
type LiveCallRecord struct {
	CallID     string // upstream returned call identifier
	CallHash   string // SHA-256 call identifier lookup key
	ProviderID int64  // selected upstream provider identifier
	APIKeyID   int64  // authenticated API Key identifier
	// ActorUserID comes from creation identity; the Redis call adapter omits
	// this field from persistence, so its reconstructed record has zero here.
	ActorUserID int64 // API Key owner user identifier
	UserID      int64 // payer or authenticated subject identifier
	// TeamID holds creation team identity; the Redis call adapter omits it
	// from persistence, so a record loaded through that adapter has zero here.
	TeamID            int64  // optional creator team identifier
	GroupID           int64  // optional selected group identifier
	SubscriptionID    int64  // optional selected subscription identifier
	LeaseID           string // Live runtime concurrency lease identifier
	Model             string // creation session model or default
	RequestedModel    string // trace-derived original client model
	UpstreamModel     string // resolved upstream model identifier
	ModelMappingChain string // serialized model trace chain
	// APIKeyModelMapping is cloned during creation and decoded on Redis reads;
	// live.rewrite uses these rules for later model declarations from the caller.
	APIKeyModelMapping map[string]string // stored API Key redirect snapshot
	// CreatedAt and ExpiresAt record the logical call lifetime and are serialized
	// as Unix milliseconds by Redis; SaveLiveCall receives a separate physical record TTL.
	CreatedAt time.Time // call creation wall-clock time
	ExpiresAt time.Time // logical call lifetime deadline
	// Controller and ControllerOwner identify the current participant and its claim.
	// Redis claim, release, and close operations compare or update the pair atomically.
	Controller      string // current controller state label
	ControllerOwner string // controller claim owner token
	UserAgent       string // inbound client User-Agent value
	IPAddress       string // resolved inbound client address
	InboundEndpoint string // inbound protocol endpoint name
	// AttestationCiphertext stores the encrypted creation-time attestation so
	// Sideband can reuse that issued value when reconnecting to the Live call.
	AttestationCiphertext string
}

// LiveCallStore persists call records and arbitrates controller changes for Live handling.
type LiveCallStore interface {
	// SaveLiveCall stores the call record for the supplied physical storage lifetime.
	// CallID and CallHash identify a usable record; the Redis adapter serializes the
	// routing map and stores fields and expiry together, returning persistence failures.
	SaveLiveCall(ctx context.Context, record *LiveCallRecord, ttl time.Duration) error
	// GetLiveCall returns the record indexed by callHash for Live lookup.
	// The production Redis adapter returns ErrLiveCallNotFound on absence and errors
	// on failed reads or invalid stored model-map JSON.
	GetLiveCall(ctx context.Context, callHash string) (*LiveCallRecord, error)
	// ClaimLiveController attempts to install controller and owner on an existing
	// live call. Redis accepts observer from pending, and proxy from pending,
	// observer, or the same proxy owner. Closed or absent records reject claims,
	// while storage errors propagate.
	//
	// Callers use the known controller constants; Redis applies those named
	// transition checks rather than validating arbitrary target labels. A successful
	// proxy claim can replace the observer, and subsequent matching-owner release
	// decides whether that proxy may return control to pending.
	ClaimLiveController(ctx context.Context, callHash, controller, owner string) (bool, error)
	// ReleaseLiveController returns a proxy-controlled call to pending when owner
	// matches. False means no matching proxy owner was released, so a stale relay's
	// cleanup cannot clear another relay's controller claim.
	ReleaseLiveController(ctx context.Context, callHash, owner string) (bool, error)
	// GetLiveController reads the stored controller label for callHash.
	// The production Redis adapter reports ErrLiveCallNotFound when the label is
	// absent, and callers use the result to detect controller changes.
	GetLiveController(ctx context.Context, callHash string) (string, error)
	// MarkLiveCallClosed marks an existing nonclosed call closed and clears its owner.
	// True means this call performed that transition; false means already closed or
	// missing. The supplied ttl controls retention of the closed record.
	MarkLiveCallClosed(ctx context.Context, callHash string, ttl time.Duration) (bool, error)
}

// Error returns the Live attestation diagnostic with an optional reason suffix.
// A nil receiver or empty reason returns the generic unavailable message so
// callers can report failure without a detailed attestation cause.
func (e *LiveAttestationUnavailableError) Error() string {
	if e == nil || e.Reason == "" {
		return "Live attestation is unavailable"
	}
	return "Live attestation is unavailable: " + e.Reason
}
