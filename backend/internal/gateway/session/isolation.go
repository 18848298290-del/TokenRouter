package session

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	SessionIsolationSourceOpenAI                 = "openai"
	SessionIsolationSourceOpenAIPreviousResponse = "openai_previous_response"
	SessionIsolationSourceGateway                = "gateway"
	SessionIsolationSourceGemini                 = "gemini"

	SessionIsolationConflictMessage = "This session already belongs to another group and cannot switch to the current session-isolated group"
)

var ErrSessionIsolationConflict = infraerrors.Forbidden("SESSION_ISOLATION_CONFLICT", SessionIsolationConflictMessage)

// OwnerStore supports atomic first-owner binding and expiry refresh.
type OwnerStore interface {
	SetSessionOwnerGroupID(context.Context, int64, string, string, int64, time.Duration) (bool, error)
	GetSessionOwnerGroupID(context.Context, int64, string, string) (int64, error)
	RefreshSessionOwnerTTL(context.Context, int64, string, string, time.Duration) error
}

// IsolationInput identifies the user, target group, and session to check.
type IsolationInput struct {
	UserID, GroupID int64
	Source, Hash    string
	Enabled         bool
	TTL             time.Duration
}

// EnsureIsolation records the first group to use a session.
// It rejects a different target group when that target has isolation enabled.
func EnsureIsolation(ctx context.Context, cache OwnerStore, input IsolationInput) error {
	userID, source, sessionHash, ttl := input.UserID, input.Source, input.Hash, input.TTL
	source = strings.TrimSpace(source)
	sessionHash = strings.TrimSpace(sessionHash)
	if cache == nil || userID <= 0 || source == "" || sessionHash == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = time.Hour
	}

	targetGroupID := input.GroupID
	targetIsolationEnabled := input.Enabled

	// Record the first group even when it has isolation disabled.
	written, err := cache.SetSessionOwnerGroupID(ctx, userID, source, sessionHash, targetGroupID, ttl)
	if err != nil {
		return err
	}
	if written {
		return nil
	}

	ownerGroupID, err := cache.GetSessionOwnerGroupID(ctx, userID, source, sessionHash)
	if err != nil {
		// Retry first-owner binding once in case the record expired between SetNX and Get.
		written, setErr := cache.SetSessionOwnerGroupID(ctx, userID, source, sessionHash, targetGroupID, ttl)
		if setErr != nil {
			return setErr
		}
		if written {
			return nil
		}
		ownerGroupID, err = cache.GetSessionOwnerGroupID(ctx, userID, source, sessionHash)
		if err != nil {
			return err
		}
	}

	if ownerGroupID == targetGroupID {
		return cache.RefreshSessionOwnerTTL(ctx, userID, source, sessionHash, ttl)
	}
	if !targetIsolationEnabled {
		return nil
	}
	return ErrSessionIsolationConflict
}
