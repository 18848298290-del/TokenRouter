package rediscache

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/mcp"
)

// state.go stores browser confirmations and encrypted result files in Redis.
// All instances compare the same serialized confirmation before approving or
// consuming it. A concurrent change causes rejection, so two accepted tool
// retries cannot both consume one approval and dispatch the protected action.
// Records keep their original expiry throughout these transitions.
//
// Confirmation previews contain redacted values and a request digest. Temporary
// file records can contain credential-bearing output, so both their metadata
// and bytes are encrypted with the deployment cipher before storage. Expiry
// bounds retention independently of process shutdown; Redis failures stop the
// workflow rather than converting missing state into permission to proceed.

const (
	// Key prefixes isolate workflow state; CAS preserves the original TTL while
	// changing the serialized state exactly once across competing server instances.
	approvalPrefix = "mcp:approval:"
	artifactPrefix = "mcp:artifact:"
	compareReplace = `if redis.call('GET',KEYS[1])~=ARGV[1] then return 0 end; redis.call('SET',KEYS[1],ARGV[2],'KEEPTTL'); return 1`
)

// State implements shared temporary MCP storage and atomic approval transitions.
type State struct {
	client *redis.Client // shared Redis connection
	cipher mcp.Cipher    // authenticated file encryption
}

// NewState binds shared Redis and encryption resources for temporary MCP workflows.
func NewState(client *redis.Client, cipher mcp.Cipher) *State {
	return &State{client: client, cipher: cipher}
}

// CreateApproval stores a new record with bounded retention and collision protection.
// Expired inputs and existing identifiers are rejected; callers return the URL
// only after storage confirms that the pending record exists.
func (s *State) CreateApproval(ctx context.Context, approval *mcp.Approval) error {
	data, err := json.Marshal(approval)
	if err != nil {
		return err
	}
	ttl := time.Until(approval.ExpiresAt)
	if ttl <= 0 {
		return errors.New("confirmation has expired")
	}
	ok, err := s.client.SetNX(ctx, approvalPrefix+approval.ID, data, ttl).Result()
	if err == nil && !ok {
		return errors.New("confirmation identifier already exists")
	}
	return err
}

// GetApproval reads a fresh confirmation without consuming or extending its lifetime.
func (s *State) GetApproval(ctx context.Context, id string) (*mcp.Approval, error) {
	data, err := s.client.Get(ctx, approvalPrefix+id).Bytes()
	if err != nil {
		return nil, err
	}
	var approval mcp.Approval
	if err := json.Unmarshal(data, &approval); err != nil {
		return nil, err
	}
	return &approval, nil
}

// Approve changes the observed pending record into a browser-session approval.
// The caller has verified owner, live permissions, and any required TOTP grant;
// concurrent confirmation or consumption fails the conditional replacement.
func (s *State) Approve(ctx context.Context, observed *mcp.Approval, sessionID string) error {
	if observed.State != "pending" || sessionID == "" {
		return errors.New("confirmation is not pending or browser session is missing")
	}
	next := *observed
	next.State = "approved"
	next.SessionID = sessionID
	return s.replace(ctx, observed, &next)
}

// Consume claims one observed approved record before its protected action executes.
// The consumed record remains until expiry so a repeated request is rejected
// even after dispatch loses its response or the handling process exits.
func (s *State) Consume(ctx context.Context, observed *mcp.Approval) error {
	if observed.State != "approved" {
		return errors.New("confirmation is not approved")
	}
	next := *observed
	next.State = "consumed"
	return s.replace(ctx, observed, &next)
}

// replace atomically compares and replaces the complete serialized
// confirmation record. Callers supply the exact record whose owner, token
// version, digest, and session they validated, then provide its permitted next
// state. A concurrent approval, consumption, or expiry causes a conflict
// instead of replacing newer state.
//
// The Redis script preserves the remaining TTL during replacement. Successful
// consumption therefore remains observable until the original expiry, even
// when the process later loses the business response or another instance
// receives a repeated request.
func (s *State) replace(ctx context.Context, previous, next *mcp.Approval) error {
	before, err := json.Marshal(previous)
	if err != nil {
		return err
	}
	after, err := json.Marshal(next)
	if err != nil {
		return err
	}
	// Comparing the complete record binds the state transition to the same owner,
	// token version, request digest, and browser session that the caller validated.
	n, err := s.client.Eval(ctx, compareReplace, []string{approvalPrefix + previous.ID}, string(before), string(after)).Int()
	if err == nil && n != 1 {
		return errors.New("confirmation changed or expired")
	}
	return err
}

// SaveArtifact encrypts a file record before storing it with its remaining lifetime.
// A failed encryption or storage write leaves the caller without a downloadable
// result, which must be reported separately from an already executed business action.
func (s *State) SaveArtifact(ctx context.Context, artifact *mcp.Artifact) error {
	data, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	encrypted, err := s.cipher.Encrypt(string(data))
	if err != nil {
		return err
	}
	ttl := time.Until(artifact.ExpiresAt)
	if ttl <= 0 {
		return errors.New("file has expired")
	}
	return s.client.Set(ctx, artifactPrefix+artifact.ID, encrypted, ttl).Err()
}

// GetArtifact authenticates and decodes a shared temporary file record.
// The returned bytes belong to this read. Callers verify current token grants
// and owner identity before displaying or streaming any portion of the result.
func (s *State) GetArtifact(ctx context.Context, id string) (*mcp.Artifact, error) {
	data, err := s.client.Get(ctx, artifactPrefix+id).Result()
	if err != nil {
		return nil, err
	}
	plaintext, err := s.cipher.Decrypt(data)
	if err != nil {
		return nil, err
	}
	var artifact mcp.Artifact
	if err := json.Unmarshal([]byte(plaintext), &artifact); err != nil {
		return nil, err
	}
	return &artifact, nil
}
