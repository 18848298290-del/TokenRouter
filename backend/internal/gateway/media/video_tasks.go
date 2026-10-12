package media

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

const (
	VideoPendingTTL                  = 24 * time.Hour
	VideoClaimTTL                    = 48 * time.Hour
	grokMediaVideoRequestOwnerSource = "grok_video_request"
)

// VideoOptions controls video task binding lifetime.
type VideoOptions struct{ StickyTTL time.Duration }

// VideoTasks reads and writes task ownership and billing state through session caches.
type VideoTasks struct {
	owners  session.GatewayCache
	billing session.GrokVideoBillingCache
	options VideoOptions
}

// GrokVideoPendingBilling is the creation snapshot used when polling first discovers completed video output.
// It fills missing status model or duration before billing defaults are applied.
type GrokVideoPendingBilling struct {
	Model                string `json:"model"`
	BillingModel         string `json:"billing_model,omitempty"`
	UpstreamModel        string `json:"upstream_model,omitempty"`
	VideoResolution      string `json:"video_resolution,omitempty"`
	VideoDurationSeconds int    `json:"video_duration_seconds,omitempty"`
	OriginalModel        string `json:"original_model,omitempty"`
	// CreatedAt is the UTC acceptance time in RFC3339Nano format.
	// duration_ms ends when polling or content download first observes done with video.url.
	CreatedAt string `json:"created_at,omitempty"`
}

// VideoBinding holds a group binding from the authentication snapshot.
type VideoBinding struct {
	GroupID int64
	Present bool
}
type VideoOwner struct {
	GroupID, ProviderID int64
	BindingIndex        int
}

func NewVideoTasks(owners session.GatewayCache, billing session.GrokVideoBillingCache, options VideoOptions) *VideoTasks {
	return &VideoTasks{owners: owners, billing: billing, options: options}
}

func videoSessionHash(seed string) string {
	current, _ := scheduler.DeriveSessionHashes(seed)
	return current
}

func videoSessionCacheKey(hash string) string {
	if strings.TrimSpace(hash) == "" {
		return ""
	}
	return "openai:" + strings.TrimSpace(hash)
}

func derefGroupID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

func GrokMediaVideoRequestSessionHash(requestID string, userID, apiKeyID int64) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || userID <= 0 || apiKeyID <= 0 {
		return ""
	}
	ownerSeed := fmt.Sprintf("%d:%d:%s", userID, apiKeyID, requestID)
	return "grok-video:" + videoSessionHash(ownerSeed)
}

func (s *VideoTasks) BindGrokMediaVideoRequestProvider(
	ctx context.Context,
	groupID *int64,
	requestID string,
	userID, apiKeyID, providerID int64,
) error {
	if s == nil || s.owners == nil {
		return fmt.Errorf("grok video request binding cache is unavailable")
	}
	sessionHash := GrokMediaVideoRequestSessionHash(requestID, userID, apiKeyID)
	cacheKey := videoSessionCacheKey(sessionHash)
	if cacheKey == "" || providerID <= 0 {
		return fmt.Errorf("grok video request binding is invalid")
	}
	// Video tasks can outlive WebSocket stickiness, so bindings last at least as long as pending billing.
	// Status and content polls use this binding to find the provider.
	ttl := VideoPendingTTL
	if s.options.StickyTTL > 0 {
		if sticky := s.options.StickyTTL; sticky > ttl {
			ttl = sticky
		}
	}
	if err := s.owners.SetSessionProviderID(ctx, derefGroupID(groupID), cacheKey, providerID, ttl); err != nil {
		return err
	}
	if groupID == nil || *groupID <= 0 {
		return nil
	}
	written, err := s.owners.SetSessionOwnerGroupID(ctx, userID, grokMediaVideoRequestOwnerSource, sessionHash, *groupID, ttl)
	if err != nil {
		return err
	}
	if !written {
		ownerGroupID, getErr := s.owners.GetSessionOwnerGroupID(ctx, userID, grokMediaVideoRequestOwnerSource, sessionHash)
		if getErr != nil {
			return getErr
		}
		if ownerGroupID != *groupID {
			return fmt.Errorf("grok video request binding belongs to another group")
		}
		return s.owners.RefreshSessionOwnerTTL(ctx, userID, grokMediaVideoRequestOwnerSource, sessionHash, ttl)
	}
	return nil
}

// ResolveGrokMediaVideoRequestGroup returns the group saved when the video task was created.
func (s *VideoTasks) ResolveGrokMediaVideoRequestGroup(
	ctx context.Context,
	requestID string,
	userID, apiKeyID int64,
) (int64, error) {
	if s == nil || s.owners == nil {
		return 0, fmt.Errorf("grok video request binding cache is unavailable")
	}
	sessionHash := GrokMediaVideoRequestSessionHash(requestID, userID, apiKeyID)
	if sessionHash == "" {
		return 0, fmt.Errorf("grok video request binding is invalid")
	}
	return s.owners.GetSessionOwnerGroupID(ctx, userID, grokMediaVideoRequestOwnerSource, sessionHash)
}

func (s *VideoTasks) ResolveGrokMediaVideoRequestProvider(
	ctx context.Context,
	groupID *int64,
	requestID string,
	userID, apiKeyID int64,
) (int64, error) {
	if s == nil || s.owners == nil {
		return 0, fmt.Errorf("grok video request binding cache is unavailable")
	}
	cacheKey := videoSessionCacheKey(GrokMediaVideoRequestSessionHash(requestID, userID, apiKeyID))
	if cacheKey == "" {
		return 0, fmt.Errorf("grok video request binding is invalid")
	}
	return s.owners.GetSessionProviderID(ctx, derefGroupID(groupID), cacheKey)
}

// GrokVideoPendingCreatedAtNow returns a UTC acceptance timestamp for pending billing.
func GrokVideoPendingCreatedAtNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// GrokVideoE2EDuration returns elapsed time from task acceptance to observed completion.
// Missing or invalid CreatedAt returns zero so the caller can retain its polling duration.
func GrokVideoE2EDuration(createdAt string, discoveredAt time.Time) time.Duration {
	createdAt = strings.TrimSpace(createdAt)
	if createdAt == "" {
		return 0
	}
	if discoveredAt.IsZero() {
		discoveredAt = time.Now()
	}
	var created time.Time
	var err error
	if created, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		if created, err = time.Parse(time.RFC3339, createdAt); err != nil {
			return 0
		}
	}
	if created.IsZero() {
		return 0
	}
	d := discoveredAt.Sub(created)
	if d < 0 {
		return 0
	}
	return d
}

func VideoPendingKey(requestID string, userID, apiKeyID int64) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || userID <= 0 || apiKeyID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d:%s", userID, apiKeyID, requestID)
}

// StoreGrokVideoPendingBilling saves creation parameters for billing after completion is observed.
func (s *VideoTasks) StoreGrokVideoPendingBilling(
	ctx context.Context,
	requestID string,
	userID, apiKeyID int64,
	pending GrokVideoPendingBilling,
) error {
	if s == nil || s.owners == nil {
		return fmt.Errorf("grok video pending billing cache is unavailable")
	}
	key := VideoPendingKey(requestID, userID, apiKeyID)
	if key == "" {
		return fmt.Errorf("grok video pending billing key is invalid")
	}
	pending.Model = strings.TrimSpace(pending.Model)
	pending.BillingModel = strings.TrimSpace(pending.BillingModel)
	pending.UpstreamModel = strings.TrimSpace(pending.UpstreamModel)
	pending.OriginalModel = strings.TrimSpace(pending.OriginalModel)
	if pending.VideoResolution != "" {
		pending.VideoResolution = pricing.NormalizeVideoBillingResolutionOrDefault(pending.VideoResolution)
	}
	if pending.VideoDurationSeconds > 0 {
		pending.VideoDurationSeconds = pricing.NormalizeVideoBillingDurationSecondsOrDefault(pending.VideoDurationSeconds)
	}
	// Supply a missing acceptance time for end-to-end duration calculation during delayed billing.
	if strings.TrimSpace(pending.CreatedAt) == "" {
		pending.CreatedAt = GrokVideoPendingCreatedAtNow()
	} else {
		pending.CreatedAt = strings.TrimSpace(pending.CreatedAt)
	}
	payload, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	cache := s.billing
	if cache == nil {
		return fmt.Errorf("grok video pending billing cache is unavailable")
	}
	return cache.SetGrokVideoPendingBilling(ctx, key, payload, VideoPendingTTL)
}

// LoadGrokVideoPendingBilling returns the creation snapshot, or nil on a cache miss.
func (s *VideoTasks) LoadGrokVideoPendingBilling(
	ctx context.Context,
	requestID string,
	userID, apiKeyID int64,
) (*GrokVideoPendingBilling, error) {
	if s == nil || s.owners == nil {
		return nil, fmt.Errorf("grok video pending billing cache is unavailable")
	}
	key := VideoPendingKey(requestID, userID, apiKeyID)
	if key == "" {
		return nil, fmt.Errorf("grok video pending billing key is invalid")
	}
	cache := s.billing
	if cache == nil {
		return nil, fmt.Errorf("grok video pending billing cache is unavailable")
	}
	payload, err := cache.GetGrokVideoPendingBilling(ctx, key)
	if err != nil || len(payload) == 0 {
		return nil, err
	}
	var pending GrokVideoPendingBilling
	if err := json.Unmarshal(payload, &pending); err != nil {
		return nil, err
	}
	return &pending, nil
}

// ClaimGrokVideoBilling claims billing for the cache TTL; callers skip billing on a false result or error.
func (s *VideoTasks) ClaimGrokVideoBilling(
	ctx context.Context,
	requestID string,
	userID, apiKeyID int64,
) (bool, error) {
	if s == nil || s.owners == nil {
		return false, fmt.Errorf("grok video billing claim cache is unavailable")
	}
	key := VideoPendingKey(requestID, userID, apiKeyID)
	if key == "" {
		return false, fmt.Errorf("grok video billing claim key is invalid")
	}
	cache := s.billing
	if cache == nil {
		return false, fmt.Errorf("grok video billing claim cache is unavailable")
	}
	return cache.ClaimGrokVideoBilled(ctx, key, VideoClaimTTL)
}

// ReleaseGrokVideoBilling releases the claim after RecordUsage persistence fails,
// allowing a later status or content poll to retry billing.
func (s *VideoTasks) ReleaseGrokVideoBilling(
	ctx context.Context,
	requestID string,
	userID, apiKeyID int64,
) error {
	if s == nil || s.owners == nil {
		return fmt.Errorf("grok video billing claim cache is unavailable")
	}
	key := VideoPendingKey(requestID, userID, apiKeyID)
	if key == "" {
		return fmt.Errorf("grok video billing claim key is invalid")
	}
	cache := s.billing
	if cache == nil {
		return fmt.Errorf("grok video billing claim cache is unavailable")
	}
	return cache.ReleaseGrokVideoBilled(ctx, key)
}

// StableGrokVideoBillingRequestID returns a task-stable usage_logs deduplication ID.
// Use it across polls instead of each poll's gateway request ID.
func StableGrokVideoBillingRequestID(taskRequestID string) string {
	taskRequestID = strings.TrimSpace(taskRequestID)
	if taskRequestID == "" {
		return ""
	}
	if strings.HasPrefix(taskRequestID, "grok-video:") {
		return taskRequestID
	}
	return "grok-video:" + taskRequestID
}

// TrackCreated binds ownership best-effort and retries snapshot storage once, reporting failures through the observer.
func (s *VideoTasks) TrackCreated(ctx context.Context, groupID *int64, taskID string, userID, keyID, providerID int64, pending GrokVideoPendingBilling, observer VideoObserver) {
	billingKey := StableGrokVideoBillingRequestID(taskID)
	createdAt, _ := time.Parse(time.RFC3339Nano, pending.CreatedAt)
	telemetry.RecordRelatedRequest(ctx, telemetry.RequestRecord{RequestID: telemetry.StableRequestID(fmt.Sprintf("%d/%s", keyID, billingKey)), StartedAt: createdAt, State: "running", UserID: userID, APIKeyID: keyID, ProviderID: providerID, Model: pending.Model, Aliases: []telemetry.RequestAlias{{Kind: "task", Value: taskID}, {Kind: "billing", Value: billingKey}}})

	if err := s.BindGrokMediaVideoRequestProvider(ctx, groupID, taskID, userID, keyID, providerID); err != nil {
		videoNotice(observer, VideoNotice{Kind: "bind_failed", TaskID: taskID, ProviderID: providerID, Err: err})
	}
	if err := s.StoreGrokVideoPendingBilling(ctx, taskID, userID, keyID, pending); err != nil {
		videoNotice(observer, VideoNotice{Kind: "store_retry", TaskID: taskID, ProviderID: providerID, Err: err})
		if retryErr := s.StoreGrokVideoPendingBilling(ctx, taskID, userID, keyID, pending); retryErr != nil {
			videoNotice(observer, VideoNotice{Kind: "store_failed", TaskID: taskID, ProviderID: providerID, Err: retryErr})
		}
	}
}

// ResolveCompositeVideo tries saved ownership first, then scans current bindings for legacy tasks.
func (s *VideoTasks) ResolveCompositeVideo(ctx context.Context, taskID string, userID, keyID int64, bindings []VideoBinding) (VideoOwner, error) {
	var lookupErr error
	ownerID, err := s.ResolveGrokMediaVideoRequestGroup(ctx, taskID, userID, keyID)
	if err == nil && ownerID > 0 {
		providerID, providerErr := s.ResolveGrokMediaVideoRequestProvider(ctx, &ownerID, taskID, userID, keyID)
		if providerErr == nil && providerID > 0 {
			index := -1
			for i, binding := range bindings {
				if binding.GroupID == ownerID && binding.Present {
					index = i
					break
				}
			}
			return VideoOwner{GroupID: ownerID, ProviderID: providerID, BindingIndex: index}, nil
		}
		lookupErr = providerErr
	} else if err != nil {
		lookupErr = err
	}
	for i, binding := range bindings {
		if !binding.Present {
			continue
		}
		id := binding.GroupID
		providerID, err := s.ResolveGrokMediaVideoRequestProvider(ctx, &id, taskID, userID, keyID)
		if err != nil {
			lookupErr = err
			continue
		}
		if providerID <= 0 {
			continue
		}
		return VideoOwner{GroupID: id, ProviderID: providerID, BindingIndex: i}, nil
	}
	if lookupErr == nil {
		lookupErr = fmt.Errorf("grok video request binding not found")
	}
	return VideoOwner{}, lookupErr
}
