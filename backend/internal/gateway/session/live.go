package session

import (
	"context"
	"errors"
	"time"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

const (
	// LiveControllerPending means neither the observer nor the client controls the session yet.
	LiveControllerPending  = "pending"
	LiveControllerObserver = "observer"
	LiveControllerProxy    = "proxy"
	LiveControllerClosed   = "closed"
)

var (
	// ErrLiveUnavailable indicates that the runtime or infrastructure cannot provide Live service.
	ErrLiveUnavailable        = errors.New("live is unavailable")
	ErrLiveConcurrencyFull    = errors.New("live concurrency is full")
	ErrLiveCallNotFound       = errors.New("live call not found")
	ErrLiveIdentityMismatch   = errors.New("live call identity mismatch")
	ErrLiveControllerChanged  = errors.New("live controller changed")
	ErrLiveClientPolicyDenied = errors.New("live client is denied by provider policy")
)

// LiveAttestationUnavailableError explains why device attestation could not be generated or reused.
type LiveAttestationUnavailableError struct {
	Reason string
}

type LiveCallRequest = wire.LiveCallRequest

// LiveCallIdentity holds the creator identity and inbound metadata needed for fork routing.
type LiveCallIdentity struct {
	APIKeyID        int64
	ActorUserID     int64
	UserID          int64
	TeamID          *int64
	GroupID         *int64
	SubscriptionID  *int64
	UserAgent       string
	Originator      string
	IPAddress       string
	InboundEndpoint string
	ModelMapping    map[string]string
}

// LiveCallRecord stores session state needed to take over Live control on another instance.
type LiveCallRecord struct {
	CallID             string
	CallHash           string
	ProviderID         int64
	APIKeyID           int64
	ActorUserID        int64
	UserID             int64
	TeamID             int64
	GroupID            int64
	SubscriptionID     int64
	LeaseID            string
	Model              string
	RequestedModel     string
	UpstreamModel      string
	ModelMappingChain  string
	APIKeyModelMapping map[string]string
	CreatedAt          time.Time
	ExpiresAt          time.Time
	Controller         string
	ControllerOwner    string
	UserAgent          string
	IPAddress          string
	InboundEndpoint    string
	// AttestationCiphertext lets Sideband reuse the attestation issued when this session was created.
	AttestationCiphertext string
}

// LiveCallStore is the Live session storage capability supplied by the Redis gateway cache.
type LiveCallStore interface {
	SaveLiveCall(ctx context.Context, record *LiveCallRecord, ttl time.Duration) error
	GetLiveCall(ctx context.Context, callHash string) (*LiveCallRecord, error)
	ClaimLiveController(ctx context.Context, callHash, controller, owner string) (bool, error)
	ReleaseLiveController(ctx context.Context, callHash, owner string) (bool, error)
	GetLiveController(ctx context.Context, callHash string) (string, error)
	MarkLiveCallClosed(ctx context.Context, callHash string, ttl time.Duration) (bool, error)
}

func (e *LiveAttestationUnavailableError) Error() string {
	if e == nil || e.Reason == "" {
		return "Live attestation is unavailable"
	}
	return "Live attestation is unavailable: " + e.Reason
}
