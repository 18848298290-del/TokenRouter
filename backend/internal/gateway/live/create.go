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

// Created holds the public response and selected provider ID.
type Created struct {
	SDP        []byte
	CallID     string
	Location   string
	ProviderID int64
}

// CreateTarget supplies model resolution, client eligibility, and upstream creation for a selected provider.
type CreateTarget interface {
	ResolveModel(context.Context, string) (string, string, error)
	AllowsClient(context.Context, session.LiveCallIdentity) bool
	Create(context.Context, *session.LiveCallRequest, string) (*Created, error)
}

// Candidate owns the ordinary provider slot released after the Live lease is acquired and creation returns.
type Candidate struct {
	ID          int64
	Concurrency int
	Acquired    bool
	ReleaseFunc func()
	Target      CreateTarget
}

// CreatePorts supplies credentials, routing, and observation operations.
type CreatePorts interface {
	PrepareAttestation(context.Context) (string, string, error)
	Select(context.Context, *int64, string, map[int64]struct{}) (*Candidate, error)
	TraceModels(context.Context, string, string)
	ModelTrace(context.Context, *int64, string, string) (string, string)
	NewLeaseID() string
	ShouldFailover(error) bool
	Observe(*session.LiveCallRecord)
}

// Creator orders Live creation, lease acquisition, session storage, and observer startup.
type Creator struct {
	runtime     *Service
	ports       CreatePorts
	maxDuration time.Duration
}

func NewCreator(runtime *Service, ports CreatePorts, maxDuration time.Duration) *Creator {
	return &Creator{runtime: runtime, ports: ports, maxDuration: maxDuration}
}

func HashCallID(callID string) string {
	sum := sha256.Sum256([]byte(callID))
	return hex.EncodeToString(sum[:])
}

func liveGroupID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

// Create opens a Frameless session. The caller holds the ordinary user slot during the call.
// A Live lease accounts for the session before the ordinary provider slot is released.
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
	for range 4 {
		selection, selectErr := s.ports.Select(ctx, identity.GroupID, model, excluded)

		if selectErr != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, selectErr
		}
		if selection == nil || selection.Target == nil || !selection.Acquired {
			if selection != nil && selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			return nil, session.ErrLiveConcurrencyFull
		}

		provider := selection
		routingModel, upstreamModel, routingErr := provider.Target.ResolveModel(ctx, model)

		if routingErr != nil {
			selection.ReleaseFunc()
			excluded[provider.ID] = struct{}{}
			lastErr = routingErr
			continue
		}
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
		if !provider.Target.AllowsClient(ctx, identity) {
			selection.ReleaseFunc()
			excluded[provider.ID] = struct{}{}
			lastErr = session.ErrLiveClientPolicyDenied
			continue
		}
		leaseID := s.ports.NewLeaseID()
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
		mappingTTL := s.maxDuration + 5*time.Minute
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

// cloneModelMapping copies model mappings, returning an empty map for nil input.
func cloneModelMapping(mapping map[string]string) map[string]string {
	result := make(map[string]string, len(mapping))
	maps.Copy(result, mapping)
	return result
}
