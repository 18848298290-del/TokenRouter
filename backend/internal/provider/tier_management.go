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

// tier_management.go refreshes Google One subscription and storage
// observations through conditional provider updates.
// Google One tier refresh observes a provider's upstream subscription and Drive
// storage while administrators can change the same credentials. Saving a full
// pre-query snapshot would let the delayed observation replace a newer token or
// unrelated Extra fields.
//
// Refresh captures the credential identity before observation, then writes a
// tier_id credential patch and the Drive fields reported by that observation
// through conditional management updates. Cancellation is checked before the
// query and again before its write. Batch refresh selects eligible Google One
// OAuth records, limits concurrent observations to ten, and records each
// failure while allowing other records to finish. The shared activity tracker
// rejects new maintenance after stop, cancels registered work, and retains the
// first shutdown result.

// GoogleOneTierObservation holds the tier and storage data from one upstream
// query.
type GoogleOneTierObservation struct {
	TierID string // observed Google One tier identifier
	// Storage and ObservedAt describe the Drive query together. A nil Storage
	// causes GoogleOneTierExtraPatch to omit every storage field, retaining the
	// stored observation in the configuration merge.
	Storage    *GoogleOneStorage // optional Drive storage observation
	ObservedAt time.Time         // Drive storage observation time
}

type (
	// GoogleOneStorage contains Drive capacity and consumption measured in bytes.
	GoogleOneStorage struct {
		Limit, Usage int64 // bytes
	}
	// TierManagementStore supplies provider selection and conditional management
	// writes.
	TierManagementStore interface {
		// GetProvider reads the persisted provider selected by its numeric ID.
		GetProvider(context.Context, int64) (*Record, error)
		// GetProvidersByIDs reads provider records for the selected batch identities.
		GetProvidersByIDs(context.Context, []int64) ([]*Record, error)
		// ListProviders returns one filtered and sorted administrator provider page.
		ListProviders(context.Context, int, int, string, string, string, string, int64, string, string, string) ([]Record, int64, error)
		// UpdateProvider applies management intent to a provider's current record.
		// Tier refresh supplies ExpectedCredentials and patch flags so a changed
		// identity is rejected and unrelated saved fields retain their latest values.
		UpdateProvider(context.Context, int64, *UpdateProviderInput) (*Record, error)
	}
)

// TierManagementOptions supplies the upstream query used by registered tier
// maintenance.
type TierManagementOptions struct {
	// Observe is required before Refresh or Batch runs. It receives a copied
	// eligible provider and a cancelable context, returning tier data and any
	// available Drive observation for that operation.
	Observe func(context.Context, *Record) (GoogleOneTierObservation, error)
}

// TierRefreshResult reports a saved tier and merged administrator display data.
type TierRefreshResult struct {
	TierID string // saved Google One tier identifier
	// StorageInfo contains copied Extra from the queried record plus reported
	// Drive fields. It is display data rather than a reread of the persisted row.
	StorageInfo map[string]any
}

// TierRefreshFailure associates one failed batch refresh with its provider
// identity.
type TierRefreshFailure struct {
	ProviderID int64  // failed provider record identity
	Error      string // refresh failure display message
}

// TierBatchResult summarizes the selected providers and independently completed
// refresh outcomes.
type TierBatchResult struct {
	// Total, Success, and Failed count eligible records and their completed
	// outcomes. Batch's mutex protects outcome updates so each selected callback
	// contributes exactly one success or failure.
	Total, Success, Failed int // selected, successful, and failed counts
	// Errors follows the order callbacks append under the result mutex.
	// Concurrent query completion and goroutine scheduling can produce a
	// different order from the supplied provider IDs.
	Errors []TierRefreshFailure
}

// TierManagement validates eligibility, queries tiers, and writes updates
// conditional on credential identity.
type TierManagement struct {
	store   TierManagementStore   // provider query and update adapter
	options TierManagementOptions // upstream tier observation callback
	// activity registers single and batch operations until their deferred finish.
	// Stopping cancels them and fixes the result of the first shutdown attempt.
	activity operationActivity
}

// ProviderTierManagementOptions adapts Gemini authorization into Google One
// tier observation. The callback requires google_one OAuth metadata and a
// stored access token, using the resolved proxy only when both proxy fields
// exist.
//
// Drive storage is copied into the provider observation with its observation
// timestamp when returned. The source error is retained alongside that
// observation, so refresh can reject unsuccessful queries before attempting any
// conditional update.
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

// NewTierManagement constructs tier maintenance around the supplied store and
// observer.
func NewTierManagement(store TierManagementStore, options TierManagementOptions) *TierManagement {
	return &TierManagement{store: store, options: options}
}

// StopContext rejects subsequent tier maintenance and cancels registered single
// or batch refresh work. It waits within ctx's budget and retains the first
// shutdown result, including unfinished work on timeout.
func (s *TierManagement) StopContext(ctx context.Context) error {
	return s.activity.stop(ctx, "provider tier maintenance")
}

// Refresh registers one tier operation and delegates eligibility, observation,
// and conditional writing to refresh. Callers pass a stable nonnil provider;
// stop or request cancellation reaches its upstream query through the
// registered context.
func (s *TierManagement) Refresh(ctx context.Context, v *Record) (*TierRefreshResult, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrRefreshStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	return s.refresh(ctx, v)
}

// refresh observes an eligible Gemini Google One OAuth record and submits its
// tier and reported Drive fields for update. A canceled context stops before
// observation, and the record's credential identity is captured before upstream
// work begins.
//
// The observer receives a copied record. Cancellation is checked again after it
// returns, preventing a late response from being saved after shutdown.
// UpdateProvider receives ExpectedCredentials plus credential and Extra patch
// flags, so its locked merge can reject an identity change and retain fields
// outside this observation. The result uses an independent display merge of the
// queried snapshot, copying CloneValues' supported containers while other
// dynamic values retain their input references.
func (s *TierManagement) refresh(ctx context.Context, v *Record) (*TierRefreshResult, error) {
	// Batch callbacks may wait behind the concurrency limit while shutdown
	// cancels their shared operation context. Check that context before calling
	// the observer so queued records do not start another upstream request.
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
	expected := ObserveCredentialVersion(v)
	observation, err := s.options.Observe(ctx, CloneRecord(v))
	if err != nil {
		return nil, err
	}
	// An observer can return after ignoring cancellation. Recheck before the
	// conditional management write so its late response cannot save a tier
	// after the registered operation was stopped.
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	extra, credentials := ProjectGoogleOneTier(v, observation)
	patch := GoogleOneTierExtraPatch(observation)
	// Send just the tier and reported Drive fields with the pre-query identity.
	// The management writer compares that identity under its row lock and
	// retains unrelated values from the latest record.
	_, err = s.store.UpdateProvider(ctx, v.ID, &UpdateProviderInput{ExpectedCredentials: &expected, Credentials: map[string]any{"tier_id": credentials["tier_id"]}, PatchCredentials: true, Extra: patch, PatchExtra: true})
	if err != nil {
		return nil, err
	}
	return &TierRefreshResult{TierID: observation.TierID, StorageInfo: extra}, nil
}

// GoogleOneTierExtraPatch builds the Extra fields supplied by one storage
// observation. Nil Storage returns an empty map, so a tier-only result leaves
// previously stored Drive data to the configuration merge.
func GoogleOneTierExtraPatch(observation GoogleOneTierObservation) map[string]any {
	out := map[string]any{}
	if observation.Storage != nil {
		out["drive_storage_limit"] = observation.Storage.Limit
		out["drive_storage_usage"] = observation.Storage.Usage
		out["drive_tier_updated_at"] = observation.ObservedAt.Format(time.RFC3339)
	}
	return out
}

// ProjectGoogleOneTier merges the observation into copied Extra and credential
// maps for display. Missing maps become empty objects, existing Drive values
// remain when storage is absent, and tier_id is set from the observation.
// CloneValues separates its supported containers; other dynamic values remain
// shared, so callers keep those retained references immutable.
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

// Batch refreshes eligible Gemini Google One OAuth providers and returns
// success and failure counts. Empty IDs select the first name-sorted page of up
// to ten thousand Gemini OAuth records, then filter that page to google_one;
// supplied IDs use the bulk reader.
//
// At most ten refresh callbacks run together. Each callback records its own
// failure and returns nil to the errgroup, so one upstream error does not
// cancel other selected providers. Result mutations share a mutex, and error
// order follows callback publication under that mutex. Stop cancellation
// reaches the whole batch, and queued callbacks check that cancellation before
// beginning another observation.
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
	// Bound upstream observations across the selected batch. Returning nil from
	// each callback keeps an individual failure in the result and allows the
	// other providers to continue under the same cancellation context.
	g.SetLimit(10)
	for _, v := range providers {
		g.Go(func() error {
			_, err := s.refresh(gctx, v)
			// A callback contributes one completed outcome. Count and append together
			// under this mutex so concurrent providers cannot lose result updates.
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
