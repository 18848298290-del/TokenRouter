package apikey

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type ManagedKeyStore interface {
	GetManagedKeyByUserAndGroup(context.Context, int64, int64, string) (*APIKey, error)
	CreateManagedKey(context.Context, *APIKey) error
}

type ManagedKeys struct {
	Store      ManagedKeyStore
	Prefix     string
	ManagedBy  string
	NamePrefix string
}

func (s ManagedKeys) Ensure(ctx context.Context, userID, groupID int64) (*APIKey, error) {
	if s.Store == nil {
		return nil, errors.New("creative managed key repository is not configured")
	}
	existing, err := s.Store.GetManagedKeyByUserAndGroup(ctx, userID, groupID, s.ManagedBy)
	if err == nil && existing != nil {
		return existing, nil
	}
	if err != nil && !errors.Is(err, ErrAPIKeyNotFound) {
		return nil, err
	}
	prefix := strings.TrimSpace(s.Prefix)
	if prefix == "" {
		prefix = "sk-"
	}
	keyString, err := GenerateAPIKeyString(prefix)
	if err != nil {
		return nil, err
	}
	managedBy := s.ManagedBy
	key := &APIKey{
		UserID:                       userID,
		Key:                          keyString,
		Name:                         fmt.Sprintf("%s:%d", s.NamePrefix, groupID),
		GroupID:                      &groupID,
		Status:                       StatusActive,
		BillingMode:                  APIKeyBillingModeAuto,
		ManagedBy:                    &managedBy,
		FastModePolicy:               "follow_request",
		FallbackWhenGroupUnavailable: false,
	}
	if err := s.Store.CreateManagedKey(ctx, key); err != nil {
		// A concurrent creator may have won; reload the managed key before returning the creation error.
		if existing, retryErr := s.Store.GetManagedKeyByUserAndGroup(ctx, userID, groupID, s.ManagedBy); retryErr == nil && existing != nil {
			return existing, nil
		}
		return nil, err
	}
	return key, nil
}
