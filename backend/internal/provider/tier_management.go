package provider

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// GoogleOneTierObservation holds the tier and storage data from one upstream query.
type GoogleOneTierObservation struct {
	TierID     string
	Storage    *GoogleOneStorage
	ObservedAt time.Time
}

type (
	GoogleOneStorage    struct{ Limit, Usage int64 }
	TierManagementStore interface {
		GetProvider(context.Context, int64) (*Record, error)
		GetProvidersByIDs(context.Context, []int64) ([]*Record, error)
		ListProviders(context.Context, int, int, string, string, string, string, int64, string, string, string) ([]Record, int64, error)
		UpdateProvider(context.Context, int64, *UpdateProviderInput) (*Record, error)
	}
)

type TierManagementOptions struct {
	Observe func(context.Context, *Record) (GoogleOneTierObservation, error)
}

type TierRefreshResult struct {
	TierID      string
	StorageInfo map[string]any
}

type TierRefreshFailure struct {
	ProviderID int64
	Error      string
}

type TierBatchResult struct {
	Total, Success, Failed int
	Errors                 []TierRefreshFailure
}

// TierManagement validates eligibility, queries tiers, and writes updates conditional on credential identity.
type TierManagement struct {
	store    TierManagementStore
	options  TierManagementOptions
	activity operationActivity
}

// ProviderTierManagementOptions adapts Drive queries to tier observations.
func ProviderTierManagementOptions(source *GeminiAuthorization) TierManagementOptions {
	return TierManagementOptions{Observe: func(ctx context.Context, v *Record) (GoogleOneTierObservation, error) {
		kind, ok := v.Credentials["oauth_type"].(string)
		if !ok || kind != "google_one" {
			return GoogleOneTierObservation{}, fmt.Errorf("not a google_one OAuth provider")
		}
		token, ok := v.Credentials["access_token"].(string)
		if !ok || token == "" {
			return GoogleOneTierObservation{}, fmt.Errorf("missing access_token")
		}
		var proxy string
		if v.ProxyID != nil && v.Proxy != nil {
			proxy = v.Proxy.URL()
		}
		tier, storage, err := source.FetchGoogleOneTier(ctx, token, proxy)
		out := GoogleOneTierObservation{TierID: tier}
		if storage != nil {
			out.Storage = &GoogleOneStorage{Limit: storage.Limit, Usage: storage.Usage}
			out.ObservedAt = time.Now()
		}
		return out, err
	}}
}

func NewTierManagement(store TierManagementStore, options TierManagementOptions) *TierManagement {
	return &TierManagement{store: store, options: options}
}

func (s *TierManagement) StopContext(ctx context.Context) error {
	return s.activity.stop(ctx, "provider tier maintenance")
}

func (s *TierManagement) Refresh(ctx context.Context, v *Record) (*TierRefreshResult, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrRefreshStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	return s.refresh(ctx, v)
}

func (s *TierManagement) refresh(ctx context.Context, v *Record) (*TierRefreshResult, error) {
	// Queued work must check cancellation before starting an upstream observation.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if v.Platform != PlatformGemini || v.Type != ProviderTypeOAuth {
		return nil, apperror.BadRequest("", "Only Gemini OAuth providers support tier refresh")
	}
	kind, _ := v.Credentials["oauth_type"].(string)
	if kind != "google_one" {
		return nil, apperror.BadRequest("", "Only google_one OAuth providers support tier refresh")
	}
	expected := FailureVersion(v).CredentialVersion
	observation, err := s.options.Observe(ctx, CloneRecord(v))
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	extra, credentials := ProjectGoogleOneTier(v, observation)
	patch := GoogleOneTierExtraPatch(observation)
	_, err = s.store.UpdateProvider(ctx, v.ID, &UpdateProviderInput{ExpectedCredentials: &expected, Credentials: map[string]any{"tier_id": credentials["tier_id"]}, PatchCredentials: true, Extra: patch, PatchExtra: true})
	if err != nil {
		return nil, err
	}
	return &TierRefreshResult{TierID: observation.TierID, StorageInfo: extra}, nil
}

// GoogleOneTierExtraPatch returns fields reported by this Drive observation.
func GoogleOneTierExtraPatch(observation GoogleOneTierObservation) map[string]any {
	out := map[string]any{}
	if observation.Storage != nil {
		out["drive_storage_limit"] = observation.Storage.Limit
		out["drive_storage_usage"] = observation.Storage.Usage
		out["drive_tier_updated_at"] = observation.ObservedAt.Format(time.RFC3339)
	}
	return out
}

// ProjectGoogleOneTier merges an observation into independent extra and credential maps for display.
func ProjectGoogleOneTier(v *Record, observation GoogleOneTierObservation) (map[string]any, map[string]any) {
	extra := CloneValues(v.Extra)
	if extra == nil {
		extra = map[string]any{}
	}
	maps.Copy(extra, GoogleOneTierExtraPatch(observation))
	credentials := CloneValues(v.Credentials)
	if credentials == nil {
		credentials = map[string]any{}
	}
	credentials["tier_id"] = observation.TierID
	return extra, credentials
}

func (s *TierManagement) Batch(ctx context.Context, ids []int64) (*TierBatchResult, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrRefreshStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	providers := make([]*Record, 0)
	if len(ids) == 0 {
		all, _, err := s.store.ListProviders(ctx, 1, 10000, "gemini", "oauth", "", "", 0, "", "name", "asc")
		if err != nil {
			return nil, err
		}
		for i := range all {
			v := &all[i]
			kind, _ := v.Credentials["oauth_type"].(string)
			if kind == "google_one" {
				providers = append(providers, v)
			}
		}
	} else {
		fetched, err := s.store.GetProvidersByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, v := range fetched {
			if v == nil || v.Platform != PlatformGemini || v.Type != ProviderTypeOAuth {
				continue
			}
			kind, _ := v.Credentials["oauth_type"].(string)
			if kind == "google_one" {
				providers = append(providers, v)
			}
		}
	}
	result := &TierBatchResult{Total: len(providers)}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(10)
	for _, v := range providers {
		g.Go(func() error {
			_, err := s.refresh(gctx, v)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				result.Failed++
				result.Errors = append(result.Errors, TierRefreshFailure{ProviderID: v.ID, Error: err.Error()})
			} else {
				result.Success++
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return result, nil
}
