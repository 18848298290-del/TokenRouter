package live

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// create.go creates upstream Live calls and saves their ownership records
// under persistent concurrency leases.
// Creating a Live call transfers concurrency accounting from ordinary request
// slots to a persistent session lease. The caller holds its user slot while
// selection supplies an acquired provider slot; model and client qualification
// precede lease acquisition, and upstream creation runs while that lease
// reserves the call.
//
// The ordinary provider slot is released when creation returns. Failed
// creation releases the Live lease before any eligible reselection, and
// failure to save a successful call also releases that lease without closing
// the already created upstream call here. Saving precedes request-alias
// recording and observer startup. The record retains model and identity
// evidence for later control, but the Redis adapter persists a subset that
// omits ActorUserID and TeamID. Logical expiry and physical record retention
// are separate durations.

// Created holds the public response and selected provider ID.
type Created struct {
	SDP        []byte // upstream session answer SDP bytes
	CallID     string // upstream created call identifier
	Location   string // public call location response value
	ProviderID int64  // selected creation provider identifier
}

// CreateTarget supplies model resolution, client eligibility, and upstream
// creation for a selected provider.
type CreateTarget interface {
	// ResolveModel returns current routing and upstream names for a requested
	// Live model. Errors let Creator release the ordinary slot and try another
	// candidate; a blank upstream name can use the routing name.
	ResolveModel(context.Context, string) (string, string, error)
	// AllowsClient checks the supplied authenticated creation identity against
	// provider client policy. It runs after model rewriting and before session
	// lease acquisition, so a denied candidate releases its ordinary slot for
	// reselection.
	AllowsClient(context.Context, session.LiveCallIdentity) bool
	// Create opens the upstream call using the rewritten request and plaintext
	// attestation header. A successful result supplies a usable Created response;
	// Creator owns concurrency release and subsequent record persistence.
	Create(context.Context, *session.LiveCallRequest, string) (*Created, error)
}

// Candidate owns the ordinary provider slot released after the Live lease is
// acquired and creation returns.
type Candidate struct {
	ID          int64 // selected creation provider identifier
	Concurrency int   // provider concurrency allowance for lease
	Acquired    bool  // ordinary provider slot is held
	// ReleaseFunc is required for a valid acquired candidate. Creator releases
	// the ordinary slot when qualification or creation finishes, and the
	// persistent Live lease has separate cleanup.
	ReleaseFunc func()       // owned ordinary provider slot cleanup
	Target      CreateTarget // qualified Live creation provider target
}

// CreatePorts supplies attestation, routing, model tracing, and observer
// startup for Live creation.
type CreatePorts interface {
	// PrepareAttestation returns plaintext for upstream creation and the
	// protected ciphertext saved for later control. Errors stop creation before
	// provider selection, and the paired strings remain associated with this
	// call.
	PrepareAttestation(context.Context) (string, string, error)
	// Select returns an acquired candidate outside excluded for the supplied
	// group and model. Successful candidates provide a target and release
	// callback; unusable admission ends with concurrency-full classification
	// rather than a wait.
	Select(context.Context, *int64, string, map[int64]struct{}) (*Candidate, error)
	// TraceModels registers routing and upstream names for this creation
	// request's model trace.
	TraceModels(context.Context, string, string)
	// ModelTrace returns the original requested label and serialized mapping
	// chain for call metadata. It uses this request's current trace and group
	// route while retaining the separately supplied upstream name.
	ModelTrace(context.Context, *int64, string, string) (string, string)
	// NewLeaseID returns a new ownership identifier for persistent Live
	// concurrency accounting.
	NewLeaseID() string
	// ShouldFailover reports whether an upstream creation error permits another
	// provider attempt.
	ShouldFailover(error) bool
	// Observe requests background observation after the call record has been
	// saved. The creation response is already available, and task acceptance or
	// shutdown handling belongs to the application observer adapter.
	Observe(*session.LiveCallRecord)
}

// Creator orders Live creation, lease acquisition, session storage, and
// observer startup.
type Creator struct {
	runtime     *Service      // shared Live controller and lease service
	ports       CreatePorts   // bound creation and observation adapters
	maxDuration time.Duration // logical Live session lifetime duration
}

// NewCreator binds a usable Live service, creation adapters, and the supplied
// logical session duration. The duration is stored unchanged and later
// determines both ExpiresAt and the extra record-retention window.
func NewCreator(runtime *Service, ports CreatePorts, maxDuration time.Duration) *Creator {
	return &Creator{runtime: runtime, ports: ports, maxDuration: maxDuration}
}

// HashCallID returns the lowercase hexadecimal SHA-256 digest of the supplied
// call identifier.
func HashCallID(callID string) string {
	sum := sha256.Sum256([]byte(callID))
	return hex.EncodeToString(sum[:])
}

// liveGroupID converts an optional identity pointer into its stored numeric
// value. Nil selects zero for absent group, team, or subscription identity; a
// supplied value is retained without positivity validation.
func liveGroupID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

// Create opens a Live call while the caller holds its ordinary user slot. A
// missing session model selects gpt-live, and usable selected candidates
// provide an acquired ordinary provider slot with cleanup.
//
// The persistent lease is acquired with ordinary-slot replacement allowance
// before upstream creation. The ordinary provider slot then releases; failed
// creation releases the Live lease before eligible reselection, with at most
// four selection iterations. Successful creation saves its mapping before
// alias recording and observer startup. Save failure releases the lease but
// does not close the created upstream call here, and an observer adapter can
// reject startup during shutdown.
func (s *Creator) Create(
	ctx context.Context,
	request *session.LiveCallRequest,
	identity session.LiveCallIdentity,
	userMaxConcurrency int,
) (*Created, error) {
	if err := wire.ValidateLiveCallRequest(request); err != nil {
		return nil, err
	}
	store, err := s.runtime.ports.Store()
	if err != nil {
		return nil, err
	}
	liveCache, err := s.runtime.ports.Leases()
	if err != nil {
		return nil, err
	}
	attestation, attestationCiphertext, err := s.ports.PrepareAttestation(ctx)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(gjson.GetBytes(request.Session, "model").String())
	if model == "" {
		model = "gpt-live"
	}

	excluded := make(map[int64]struct{})
	var lastErr error
	// Bound provider reselection while retaining the most recent qualification or
	// creation error, so a later selection miss returns the failure already
	// observed for this call.
	for range 4 {
		selection, selectErr := s.ports.Select(ctx, identity.GroupID, model, excluded)

		if selectErr != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, selectErr
		}
		// Reject unusable admission before creating a persistent session; release
		// any supplied ordinary slot so the reported concurrency refusal cannot
		// retain that selection's reservation.
		if selection == nil || selection.Target == nil || !selection.Acquired {
			if selection != nil && selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			return nil, session.ErrLiveConcurrencyFull
		}

		provider := selection
		routingModel, upstreamModel, routingErr := provider.Target.ResolveModel(ctx, model)

		// Release the admitted provider slot before excluding a model-resolution
		// failure, so the next candidate cannot accumulate ordinary reservations
		// from unsuccessful Live qualification.
		if routingErr != nil {
			selection.ReleaseFunc()
			excluded[provider.ID] = struct{}{}
			lastErr = routingErr
			continue
		}
		// Use the resolved routing name when provider mapping supplies no upstream
		// name, retaining the qualified model rather than writing a blank creation
		// model.
		if strings.TrimSpace(upstreamModel) == "" {
			upstreamModel = routingModel
		}
		s.ports.TraceModels(ctx, routingModel, upstreamModel)
		upstreamSession, rewriteErr := sjson.SetBytes(request.Session, "model", upstreamModel)
		if rewriteErr != nil {
			selection.ReleaseFunc()
			return nil, rewriteErr
		}
		upstreamRequest := &session.LiveCallRequest{SDP: request.SDP, Session: upstreamSession}
		// Reject this client before acquiring its long-lived session lease; ordinary
		// slot cleanup permits trying another eligible provider under the same
		// bounded creation loop.
		if !provider.Target.AllowsClient(ctx, identity) {
			selection.ReleaseFunc()
			excluded[provider.ID] = struct{}{}
			lastErr = session.ErrLiveClientPolicyDenied
			continue
		}
		leaseID := s.ports.NewLeaseID()
		// Reserve the persistent call while both ordinary slots remain held. The
		// replacement allowance accounts for those slots, preventing admission from
		// counting this transfer as another independent request.
		acquired, acquireErr := liveCache.AcquireLiveLease(
			ctx,
			provider.ID,
			provider.Concurrency,
			identity.UserID,
			userMaxConcurrency,
			identity.APIKeyID,
			leaseID,
			true,
		)
		if acquireErr != nil || !acquired {
			selection.ReleaseFunc()
			if acquireErr != nil {
				return nil, acquireErr
			}
			return nil, session.ErrLiveConcurrencyFull
		}

		// Hold the Live lease through creation, then release the ordinary provider
		// slot. A failed exchange releases the Live reservation before any eligible
		// next-provider attempt.
		created, createErr := provider.Target.Create(ctx, upstreamRequest, attestation)
		selection.ReleaseFunc()
		if createErr != nil {
			s.runtime.ReleaseLease(provider.ID, identity.UserID, identity.APIKeyID, leaseID)
			if !s.ports.ShouldFailover(createErr) {
				return nil, createErr
			}
			excluded[provider.ID] = struct{}{}
			lastErr = createErr
			continue
		}

		now := time.Now()
		requestedModel, mappingChain := s.ports.ModelTrace(ctx, identity.GroupID, model, upstreamModel)
		record := &session.LiveCallRecord{
			CallID:                created.CallID,
			CallHash:              HashCallID(created.CallID),
			ProviderID:            provider.ID,
			APIKeyID:              identity.APIKeyID,
			ActorUserID:           identity.ActorUserID,
			UserID:                identity.UserID,
			TeamID:                liveGroupID(identity.TeamID),
			GroupID:               liveGroupID(identity.GroupID),
			SubscriptionID:        liveGroupID(identity.SubscriptionID),
			LeaseID:               leaseID,
			Model:                 model,
			RequestedModel:        requestedModel,
			UpstreamModel:         upstreamModel,
			ModelMappingChain:     mappingChain,
			APIKeyModelMapping:    cloneModelMapping(identity.ModelMapping),
			CreatedAt:             now,
			ExpiresAt:             now.Add(s.maxDuration),
			Controller:            session.LiveControllerPending,
			UserAgent:             identity.UserAgent,
			IPAddress:             identity.IPAddress,
			InboundEndpoint:       identity.InboundEndpoint,
			AttestationCiphertext: attestationCiphertext,
		}
		// Keep the mapping beyond logical expiry so a later finalizer can still
		// claim its stored closure; this retention grace does not extend the call's
		// allowed lifetime.
		mappingTTL := s.maxDuration + 5*time.Minute
		// Publish the call record before aliases and observation can reference it.
		// Save failure relinquishes lease accounting, while this path has no
		// upstream-close compensation for the created call.
		if saveErr := store.SaveLiveCall(ctx, record, mappingTTL); saveErr != nil {
			s.runtime.ReleaseLease(provider.ID, identity.UserID, identity.APIKeyID, leaseID)
			return nil, fmt.Errorf("save live call mapping: %w", saveErr)
		}
		telemetry.RecordRelatedRequest(ctx, telemetry.RequestRecord{RequestID: telemetry.StableRequestID("live/" + record.CallHash), State: "running", StartedAt: now, UserID: record.ActorUserID, APIKeyID: record.APIKeyID, ProviderID: record.ProviderID, Model: record.RequestedModel, Aliases: []telemetry.RequestAlias{{Kind: "billing", Value: record.CallHash}}})
		created.ProviderID = provider.ID
		s.ports.Observe(record)
		return created, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, session.ErrLiveUnavailable
}

// cloneModelMapping returns independently allocated string-map storage for
// creation-time Key rules. Nil input produces an allocated empty map; the
// saved call can retain these values after the identity input is released.
func cloneModelMapping(mapping map[string]string) map[string]string {
	result := make(map[string]string, len(mapping))
	maps.Copy(result, mapping)
	return result
}
