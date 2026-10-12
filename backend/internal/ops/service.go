package ops

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
)

const (
	opsMaxStoredErrorBodyBytes = 20 * 1024
	// OpsErrorLogQueueBodyMaxBytes bounds untrusted response data held in the asynchronous error queue.
	OpsErrorLogQueueBodyMaxBytes = 8 * 1024

	opsRuntimeSettingsRefreshInterval = 30 * time.Second
	opsRuntimeSettingsRefreshJitter   = 20
	opsRuntimeSettingsRefreshTimeout  = 3 * time.Second
	opsRuntimeSettingsFailureLogEvery = time.Minute
)

var ErrOpsDisabled = infraerrors.NotFound("OPS_DISABLED", "Ops monitoring is disabled")

type opsRuntimeSettingsSnapshot struct {
	monitoringEnabled bool
	advanced          OpsAdvancedSettings
}

// OpsService provides ingestion and query APIs for the Ops monitoring module.
type OpsService struct {
	snapshotOnce           sync.Once
	snapshotCache          *querycache.Cache
	realtimeOnce           sync.Once
	realtime               *RealtimeRuntime
	logging                LogControl
	opsRepo                OpsRepository
	settingRepo            Settings
	cfg                    *Options
	preAggregationSettings PreAggregationReader

	providerRepo ProviderReader
	userRepo     UserReader

	// getProviderAvailability is a unit-test hook for overriding provider availability lookup.
	getProviderAvailability func(ctx context.Context, platformFilter string, groupIDFilter *int64) (*OpsProviderAvailability, error)

	concurrencyService ConcurrencyReader

	systemLogSink               *OpsSystemLogSink
	ingressRejectAggregator     *OpsIngressRejectAggregator
	authCacheInvalidationWorker AuthHealthReader
	apiKeyService               KeyHealthReader

	// cleanupReloader is bound through SetCleanupReloader after OpsCleanupService construction.
	// Late binding breaks the construction cycle because cleanup reads Ops settings.
	cleanupReloader CleanupReloader

	// quotaAutoPauseSink publishes quota thresholds to the scheduler's settings cache.
	// UpdateOpsAdvancedSettings calls it after persistence so subsequent requests
	// read the updated defaults.
	quotaAutoPauseSink func(OpsOpenAIProviderQuotaAutoPauseSettings)

	// Published snapshots are immutable; readers use atomic loads and writers serialize updates with the mutex.
	runtimeSettings   atomic.Pointer[opsRuntimeSettingsSnapshot]
	runtimeSettingsMu sync.Mutex

	runtimeRefreshMu             sync.Mutex
	runtimeRefreshCancel         context.CancelFunc
	runtimeRefreshDone           chan struct{}
	runtimeRefreshRunning        atomic.Bool
	runtimeRefreshSuccess        atomic.Uint64
	runtimeRefreshFailure        atomic.Uint64
	runtimeRefreshLastFailureLog atomic.Int64
}

// CleanupReloader reloads cleanup schedules and enabled state.
// UpdateOpsAdvancedSettings calls it after persisting settings.
type CleanupReloader interface {
	Reload(ctx context.Context) error
}

func NewOpsService(
	opsRepo OpsRepository,
	settingRepo Settings,
	cfg *Options,
	providerRepo ProviderReader,
	userRepo UserReader,
	concurrencyService ConcurrencyReader,

	systemLogSink *OpsSystemLogSink,
	logging LogControl,
) *OpsService {
	svc := BuildOpsService(opsRepo, settingRepo, cfg, providerRepo, userRepo, concurrencyService, systemLogSink, logging)
	svc.initRuntimeSettings(context.Background())
	svc.ApplyRuntimeLogConfigOnStartup(context.Background())
	return svc
}

// SetPreAggregationSettings supplies the shared runtime switch for Ops aggregation queries.
func (s *OpsService) SetPreAggregationSettings(settings PreAggregationReader) {
	if s == nil {
		return
	}
	s.preAggregationSettings = settings
}

// SetCleanupReloader binds cleanup after construction to break the dependency cycle.
func (s *OpsService) SetCleanupReloader(r CleanupReloader) {
	if s == nil {
		return
	}
	s.cleanupReloader = r
}

// SetOpenAIQuotaAutoPauseSettingsSink binds the callback that publishes quota
// thresholds to the scheduler's cache after settings updates.
func (s *OpsService) SetOpenAIQuotaAutoPauseSettingsSink(sink func(OpsOpenAIProviderQuotaAutoPauseSettings)) {
	if s == nil {
		return
	}
	s.quotaAutoPauseSink = sink
}

// BuildOpsService constructs the Ops service with its dependencies.
func BuildOpsService(
	opsRepo OpsRepository,
	settingRepo Settings,
	cfg *Options,
	providerRepo ProviderReader,
	userRepo UserReader,
	concurrencyService ConcurrencyReader,

	systemLogSink *OpsSystemLogSink,
	logging LogControl,
) *OpsService {
	svc := &OpsService{
		opsRepo:     opsRepo,
		settingRepo: settingRepo,
		cfg:         cfg,

		providerRepo: providerRepo,
		userRepo:     userRepo,

		concurrencyService: concurrencyService,

		systemLogSink: systemLogSink,
		logging:       logging,
	}
	return svc
}

func (s *OpsService) RequireMonitoringEnabled(ctx context.Context) error {
	if s.IsMonitoringEnabled(ctx) {
		return nil
	}
	return ErrOpsDisabled
}

func (s *OpsService) IsMonitoringEnabled(ctx context.Context) bool {
	_ = ctx
	// Hard switch: disable ops entirely.
	if s.cfg != nil && !s.cfg.Ops.Enabled {
		return false
	}
	if snapshot := s.runtimeSettings.Load(); snapshot != nil {
		return snapshot.monitoringEnabled
	}
	// A missing snapshot defaults to enabled; request handling does not query settings synchronously.
	return true
}

func parseOpsMonitoringEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "false", "0", "off", "disabled":
		return false
	default:
		return true
	}
}

func (s *OpsService) initRuntimeSettings(ctx context.Context) {
	if s == nil {
		return
	}
	defaults := defaultOpsAdvancedSettings()
	s.runtimeSettings.Store(&opsRuntimeSettingsSnapshot{monitoringEnabled: true, advanced: *defaults})
	_ = s.RefreshRuntimeSettings(ctx)
}

// RefreshRuntimeSettings loads a database snapshot for startup or settings updates.
func (s *OpsService) RefreshRuntimeSettings(ctx context.Context) error {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.runtimeSettingsMu.Lock()
	defer s.runtimeSettingsMu.Unlock()

	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyOpsMonitoringEnabled,
		SettingKeyOpsAdvancedSettings,
	})
	if err != nil {
		return err
	}

	monitoringEnabled := true
	if raw, ok := values[SettingKeyOpsMonitoringEnabled]; ok {
		monitoringEnabled = parseOpsMonitoringEnabled(raw)
	}
	advanced := defaultOpsAdvancedSettings()
	if raw, ok := values[SettingKeyOpsAdvancedSettings]; ok {
		if err := json.Unmarshal([]byte(raw), advanced); err != nil {
			advanced = defaultOpsAdvancedSettings()
		}
	}
	normalizeOpsAdvancedSettings(advanced)

	s.runtimeSettings.Store(&opsRuntimeSettingsSnapshot{monitoringEnabled: monitoringEnabled, advanced: *advanced})
	return nil
}

// StartRuntimeSettingsRefresh polls database settings so application instances observe updates.
func (s *OpsService) StartRuntimeSettingsRefresh(ctx context.Context) {
	s.startRuntimeSettingsRefresh(ctx, opsRuntimeSettingsRefreshInterval, opsRuntimeSettingsRefreshJitter, opsRuntimeSettingsRefreshTimeout)
}

func (s *OpsService) startRuntimeSettingsRefresh(ctx context.Context, interval time.Duration, jitterPercent int, timeout time.Duration) {
	if s == nil || s.settingRepo == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = opsRuntimeSettingsRefreshInterval
	}
	if timeout <= 0 {
		timeout = opsRuntimeSettingsRefreshTimeout
	}
	if jitterPercent < 0 {
		jitterPercent = 0
	}
	if jitterPercent > 100 {
		jitterPercent = 100
	}

	s.runtimeRefreshMu.Lock()
	if s.runtimeRefreshCancel != nil {
		s.runtimeRefreshMu.Unlock()
		return
	}
	refreshCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.runtimeRefreshCancel = cancel
	s.runtimeRefreshDone = done
	s.runtimeRefreshRunning.Store(true)
	s.runtimeRefreshMu.Unlock()

	go func() {
		defer close(done)
		defer s.runtimeRefreshRunning.Store(false)
		for {
			delay := jitterDuration(interval, jitterPercent)
			timer := time.NewTimer(delay)
			select {
			case <-refreshCtx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
			}

			attemptCtx, attemptCancel := context.WithTimeout(refreshCtx, timeout)
			err := s.RefreshRuntimeSettings(attemptCtx)
			attemptCancel()
			if err != nil {
				s.runtimeRefreshFailure.Add(1)
				s.logRuntimeSettingsRefreshFailure(err)
				continue
			}
			s.runtimeRefreshSuccess.Add(1)
		}
	}()
}

func jitterDuration(base time.Duration, percent int) time.Duration {
	if base <= 0 || percent <= 0 {
		return base
	}
	delta := float64(percent) / 100
	factor := 1 - delta + rand.Float64()*(2*delta)
	if factor <= 0 {
		return base
	}
	return time.Duration(float64(base) * factor)
}

func (s *OpsService) logRuntimeSettingsRefreshFailure(err error) {
	if s == nil || err == nil {
		return
	}
	now := time.Now().Unix()
	for {
		last := s.runtimeRefreshLastFailureLog.Load()
		if last != 0 && now-last < int64(opsRuntimeSettingsFailureLogEvery/time.Second) {
			return
		}
		if s.runtimeRefreshLastFailureLog.CompareAndSwap(last, now) {
			log.Printf("[Ops] runtime settings refresh failed: %v", err)
			return
		}
	}
}

// StopRuntimeSettingsRefresh cancels and waits for the refresh loop; repeated calls are safe.
func (s *OpsService) StopRuntimeSettingsRefresh() {
	if s == nil {
		return
	}
	s.runtimeRefreshMu.Lock()
	cancel := s.runtimeRefreshCancel
	done := s.runtimeRefreshDone
	s.runtimeRefreshMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	s.runtimeRefreshMu.Lock()
	if s.runtimeRefreshDone == done {
		s.runtimeRefreshCancel = nil
		s.runtimeRefreshDone = nil
	}
	s.runtimeRefreshMu.Unlock()
}

// SetMonitoringEnabled publishes an already-persisted monitoring switch in memory.
func (s *OpsService) SetMonitoringEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.runtimeSettingsMu.Lock()
	current := s.runtimeSettings.Load()
	next := &opsRuntimeSettingsSnapshot{monitoringEnabled: enabled, advanced: *defaultOpsAdvancedSettings()}
	if current != nil {
		next.advanced = current.advanced
	}
	s.runtimeSettings.Store(next)
	s.runtimeSettingsMu.Unlock()
}

func (s *OpsService) storeAdvancedSettingsSnapshot(cfg *OpsAdvancedSettings) {
	if s == nil || cfg == nil {
		return
	}
	s.runtimeSettingsMu.Lock()
	current := s.runtimeSettings.Load()
	next := &opsRuntimeSettingsSnapshot{monitoringEnabled: true, advanced: *cfg}
	if current != nil {
		next.monitoringEnabled = current.monitoringEnabled
	}
	s.runtimeSettings.Store(next)
	s.runtimeSettingsMu.Unlock()
}

// SanitizeOpsErrorBodyForQueue sanitizes JSON credentials and bounds the body before enqueueing.
func SanitizeOpsErrorBodyForQueue(raw string) (string, bool) {
	return sanitizeErrorBodyForStorage(raw, OpsErrorLogQueueBodyMaxBytes)
}

// SanitizeOpsUpstreamErrorsForQueue bounds and serializes attempt data before enqueueing.
func SanitizeOpsUpstreamErrorsForQueue(entry *OpsInsertErrorLogInput) error {
	return sanitizeOpsUpstreamErrors(entry)
}

func (s *OpsService) RecordError(ctx context.Context, entry *OpsInsertErrorLogInput) error {
	prepared, ok, err := s.prepareErrorLogInput(ctx, entry)
	if err != nil {
		log.Printf("[Ops] RecordError prepare failed: %v", err)
		return err
	}
	if !ok {
		return nil
	}

	if _, err := s.opsRepo.InsertErrorLog(ctx, prepared); err != nil {
		// Report the persistence failure to the caller and process log.
		log.Printf("[Ops] RecordError failed: %v", err)
		return err
	}
	return nil
}

func (s *OpsService) RecordErrorBatch(ctx context.Context, entries []*OpsInsertErrorLogInput) error {
	if len(entries) == 0 {
		return nil
	}
	prepared := make([]*OpsInsertErrorLogInput, 0, len(entries))
	for _, entry := range entries {
		item, ok, err := s.prepareErrorLogInput(ctx, entry)
		if err != nil {
			log.Printf("[Ops] RecordErrorBatch prepare failed: %v", err)
			continue
		}
		if ok {
			prepared = append(prepared, item)
		}
	}
	if len(prepared) == 0 {
		return nil
	}
	if len(prepared) == 1 {
		_, err := s.opsRepo.InsertErrorLog(ctx, prepared[0])
		if err != nil {
			logOpsErrorBatchWriteFailure(ctx, "[Ops] RecordErrorBatch single insert failed: %v", err)
		}
		return err
	}

	if _, err := s.opsRepo.BatchInsertErrorLogs(ctx, prepared); err != nil {
		logOpsErrorBatchWriteFailure(ctx, "[Ops] RecordErrorBatch failed: %v", err)
		return err
	}
	return nil
}

func logOpsErrorBatchWriteFailure(ctx context.Context, format string, err error) {
	if isContextDoneError(ctx, err) {
		return
	}
	log.Printf(format, err)
}

func (s *OpsService) prepareErrorLogInput(ctx context.Context, entry *OpsInsertErrorLogInput) (*OpsInsertErrorLogInput, bool, error) {
	if entry == nil {
		return nil, false, nil
	}
	if !s.IsMonitoringEnabled(ctx) {
		return nil, false, nil
	}
	if s.opsRepo == nil {
		return nil, false, nil
	}

	// Ensure timestamps are always populated.
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}

	// Ensure required fields exist (DB has NOT NULL constraints).
	entry.ErrorPhase = strings.TrimSpace(entry.ErrorPhase)
	entry.ErrorType = strings.TrimSpace(entry.ErrorType)
	if entry.ErrorPhase == "" {
		entry.ErrorPhase = "internal"
	}
	if entry.ErrorType == "" {
		entry.ErrorType = "api_error"
	}

	// Credential acquisition failures use the provider_auth phase.
	// The latest credential event overrides earlier inference status and text before persistence.
	for _, last := range slices.Backward(entry.UpstreamErrors) {
		if last == nil {
			continue
		}
		if last.Stage == string("provider_auth") {
			entry.ErrorPhase = string("provider_auth")
			entry.ErrorOwner = "provider"
			entry.ErrorSource = "gateway"
			code := 0
			entry.UpstreamStatusCode = &code
			entry.UpstreamErrorMessage = nil
			if message := strings.TrimSpace(last.Message); message != "" {
				entry.UpstreamErrorMessage = &message
			}
			entry.UpstreamErrorDetail = nil
			if detail := strings.TrimSpace(last.Detail); detail != "" {
				entry.UpstreamErrorDetail = &detail
			}
		}
		break
	}

	// Sanitize + truncate error_body to avoid storing sensitive data.
	if strings.TrimSpace(entry.ErrorBody) != "" {
		sanitized, _ := sanitizeErrorBodyForStorage(entry.ErrorBody, opsMaxStoredErrorBodyBytes)
		entry.ErrorBody = sanitized
	}

	// Sanitize upstream error context if provided by gateway services.
	if entry.UpstreamStatusCode != nil && *entry.UpstreamStatusCode <= 0 && entry.ErrorPhase != string("provider_auth") {
		entry.UpstreamStatusCode = nil
	}
	if entry.UpstreamErrorMessage != nil {
		msg := strings.TrimSpace(*entry.UpstreamErrorMessage)
		msg = logredact.SanitizeUpstreamQueries(msg)
		msg = truncateString(msg, 2048)
		if strings.TrimSpace(msg) == "" {
			entry.UpstreamErrorMessage = nil
		} else {
			entry.UpstreamErrorMessage = &msg
		}
	}
	if entry.UpstreamErrorDetail != nil {
		detail := strings.TrimSpace(*entry.UpstreamErrorDetail)
		if detail == "" {
			entry.UpstreamErrorDetail = nil
		} else {
			sanitized, _ := sanitizeErrorBodyForStorage(detail, opsMaxStoredErrorBodyBytes)
			if strings.TrimSpace(sanitized) == "" {
				entry.UpstreamErrorDetail = nil
			} else {
				entry.UpstreamErrorDetail = &sanitized
			}
		}
	}

	if err := sanitizeOpsUpstreamErrors(entry); err != nil {
		return nil, false, err
	}

	return entry, true, nil
}

func sanitizeOpsUpstreamErrors(entry *OpsInsertErrorLogInput) error {
	if entry == nil || len(entry.UpstreamErrors) == 0 {
		return nil
	}

	const maxEvents = 16
	events := entry.UpstreamErrors
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}

	sanitized := make([]*OpsUpstreamErrorEvent, 0, len(events))
	for _, ev := range events {
		if ev == nil {
			continue
		}
		out := *ev

		out.Platform = truncateString(strings.TrimSpace(out.Platform), 32)
		out.ProviderName = truncateString(strings.TrimSpace(out.ProviderName), 128)
		out.UpstreamRequestID = truncateString(strings.TrimSpace(out.UpstreamRequestID), 128)
		out.UpstreamURL = truncateString(strings.TrimSpace(out.UpstreamURL), 2048)
		if body := strings.TrimSpace(out.UpstreamResponseBody); body != "" {
			out.UpstreamResponseBody, _ = sanitizeErrorBodyForStorage(body, OpsErrorLogQueueBodyMaxBytes)
		} else {
			out.UpstreamResponseBody = ""
		}
		out.Kind = truncateString(strings.TrimSpace(out.Kind), 64)
		out.Stage = truncateString(strings.TrimSpace(out.Stage), 64)
		out.Scope = truncateString(strings.TrimSpace(out.Scope), 64)
		out.Reason = truncateString(strings.TrimSpace(out.Reason), 128)

		if out.ProviderID < 0 {
			out.ProviderID = 0
		}
		if out.UpstreamStatusCode < 0 {
			out.UpstreamStatusCode = 0
		}
		if out.AtUnixMs < 0 {
			out.AtUnixMs = 0
		}

		msg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(out.Message))
		msg = truncateString(msg, 2048)
		out.Message = msg

		detail := strings.TrimSpace(out.Detail)
		if detail != "" {
			// Bound upstream details while events wait in the queue.
			sanitizedDetail, _ := sanitizeErrorBodyForStorage(detail, OpsErrorLogQueueBodyMaxBytes)
			out.Detail = sanitizedDetail
		} else {
			out.Detail = ""
		}

		// Drop fully-empty events (can happen if only status code was known).
		if out.UpstreamStatusCode == 0 && out.Message == "" && out.Detail == "" {
			continue
		}

		evCopy := out
		sanitized = append(sanitized, &evCopy)
	}

	entry.UpstreamErrorsJSON = marshalOpsUpstreamErrors(sanitized)
	entry.UpstreamErrors = nil
	return nil
}

func (s *OpsService) GetErrorLogs(ctx context.Context, filter *OpsErrorLogFilter) (*OpsErrorLogList, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return &OpsErrorLogList{Errors: []*OpsErrorLog{}, Total: 0, Page: 1, PageSize: 20}, nil
	}
	if filter == nil {
		filter = &OpsErrorLogFilter{}
	} else {
		filterCopy := *filter
		filter = &filterCopy
	}
	if filter.IgnoredStatusCodes == nil {
		filter.IgnoredStatusCodes = s.resolveOpsIgnoredStatusCodes(ctx)
	} else {
		filter.IgnoredStatusCodes = NormalizeOpsIgnoredStatusCodes(filter.IgnoredStatusCodes)
	}
	result, err := s.opsRepo.ListErrorLogs(ctx, filter)
	if err != nil {
		log.Printf("[Ops] GetErrorLogs failed: %v", err)
		return nil, err
	}

	return result, nil
}

// ListUserErrorRequests returns a sanitized list of the user's own error requests.
// It includes business limits and balance errors, and excludes count_tokens probes.
func (s *OpsService) ListUserErrorRequests(ctx context.Context, userID int64, filter *OpsErrorLogFilter) (*UserErrorRequestList, error) {
	if filter == nil {
		filter = &OpsErrorLogFilter{}
	}
	f := *filter // Copy scalar filters; query code treats shared slices as read-only.
	filter = &f
	uid := userID
	filter.UserID = &uid
	// Keep APIKeyID supplied by the handler. The repository combines it with UserID,
	// so a key owned by another user yields no rows.
	filter.View = "all"
	filter.ExcludeCountTokens = true
	filter.ModelFuzzy = true // User queries use ILIKE; admin filters default to exact matching.
	// Clear admin-only dimensions before querying.
	filter.UserQuery = ""
	filter.Owner = ""
	filter.Source = ""
	// User categories use ErrorPhasesAny and ErrorTypesAny; clear the independent Phase condition.
	// Disable recovered attempts; the repository retains the cyber_policy status exemption.
	filter.Phase = ""
	filter.IncludeRecoveredUpstream = false

	list, err := s.opsRepo.ListErrorLogs(ctx, filter)
	if err != nil {
		return nil, err
	}
	items := make([]*UserErrorRequest, 0, len(list.Errors))
	for _, e := range list.Errors {
		if r := ToUserErrorRequest(e); r != nil {
			items = append(items, r)
		}
	}
	return &UserErrorRequestList{
		Items:    items,
		Total:    list.Total,
		Page:     list.Page,
		PageSize: list.PageSize,
	}, nil
}

func (s *OpsService) GetErrorLogByID(ctx context.Context, id int64) (*OpsErrorLogDetail, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.NotFound("OPS_ERROR_NOT_FOUND", "ops error log not found")
	}
	detail, err := s.opsRepo.GetErrorLogByID(ctx, id)
	if err != nil {
		if s.isNotFound(err) {
			return nil, infraerrors.NotFound("OPS_ERROR_NOT_FOUND", "ops error log not found")
		}
		return nil, infraerrors.InternalServer("OPS_ERROR_LOAD_FAILED", "Failed to load ops error log").WithCause(err)
	}
	return detail, nil
}

// GetUserErrorRequestDetail returns sanitized details, including error_body, for the owner's request.
// It returns NotFound for another user's record.
func (s *OpsService) GetUserErrorRequestDetail(ctx context.Context, userID, id int64) (*UserErrorRequestDetail, error) {
	if s.opsRepo == nil {
		return nil, infraerrors.NotFound("OPS_ERROR_NOT_FOUND", "ops error log not found")
	}
	if id <= 0 {
		return nil, infraerrors.BadRequest("OPS_ERROR_INVALID_ID", "invalid error id")
	}
	detail, err := s.opsRepo.GetErrorLogByID(ctx, id)
	if err != nil {
		if s.isNotFound(err) {
			return nil, infraerrors.NotFound("OPS_ERROR_NOT_FOUND", "ops error log not found")
		}
		return nil, infraerrors.InternalServer("OPS_ERROR_LOAD_FAILED", "Failed to load ops error log").WithCause(err)
	}
	// Ownership comes from user_id recorded after authentication.
	ownedDirectly := detail.UserID != nil && *detail.UserID == userID
	if !ownedDirectly {
		return nil, infraerrors.NotFound("OPS_ERROR_NOT_FOUND", "ops error log not found")
	}
	return ToUserErrorRequestDetail(detail), nil
}

func (s *OpsService) UpdateErrorResolution(ctx context.Context, errorID int64, resolved bool, resolvedByUserID *int64) error {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return err
	}
	if s.opsRepo == nil {
		return infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if errorID <= 0 {
		return infraerrors.BadRequest("OPS_ERROR_INVALID_ID", "invalid error id")
	}
	// Best-effort ensure the error exists
	if _, err := s.opsRepo.GetErrorLogByID(ctx, errorID); err != nil {
		if s.isNotFound(err) {
			return infraerrors.NotFound("OPS_ERROR_NOT_FOUND", "ops error log not found")
		}
		return infraerrors.InternalServer("OPS_ERROR_LOAD_FAILED", "Failed to load ops error log").WithCause(err)
	}
	return s.opsRepo.UpdateErrorResolution(ctx, errorID, resolved, resolvedByUserID, nil)
}

func sanitizeAndTrimJSONPayload(raw []byte, maxBytes int) (jsonString string, truncated bool, bytesLen int) {
	bytesLen = len(raw)
	if len(raw) == 0 {
		return "", false, 0
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		// Let the caller handle non-JSON input as plain text.
		return "", false, bytesLen
	}

	decoded = redactSensitiveJSON(decoded)

	encoded, err := json.Marshal(decoded)
	if err != nil {
		return "", false, bytesLen
	}
	if len(encoded) <= maxBytes {
		return string(encoded), false, bytesLen
	}

	// Trim conversation history to keep the most recent context.
	if root, ok := decoded.(map[string]any); ok {
		if trimmed, ok := trimConversationArrays(root, maxBytes); ok {
			encoded2, err2 := json.Marshal(trimmed)
			if err2 == nil && len(encoded2) <= maxBytes {
				return string(encoded2), true, bytesLen
			}
			// Fallthrough: keep shrinking.
			decoded = trimmed
		}

		essential := shrinkToEssentials(root)
		encoded3, err3 := json.Marshal(essential)
		if err3 == nil && len(encoded3) <= maxBytes {
			return string(encoded3), true, bytesLen
		}
	}

	// Last resort: keep JSON shape but drop big fields.
	// Keep the named top-level keys available to downstream readers.
	if root, ok := decoded.(map[string]any); ok {
		placeholder := querycache.ShallowMap(root)
		placeholder["payload_truncated"] = true

		// Replace potentially huge arrays/strings, but keep the keys present.
		for _, k := range []string{"messages", "contents", "input", "prompt"} {
			if _, exists := placeholder[k]; exists {
				placeholder[k] = []any{}
			}
		}
		for _, k := range []string{"text"} {
			if _, exists := placeholder[k]; exists {
				placeholder[k] = ""
			}
		}

		encoded4, err4 := json.Marshal(placeholder)
		if err4 == nil {
			if len(encoded4) <= maxBytes {
				return string(encoded4), true, bytesLen
			}
		}
	}

	// Final fallback: minimal valid JSON.
	encoded4, err4 := json.Marshal(map[string]any{"payload_truncated": true})
	if err4 != nil {
		return "", true, bytesLen
	}
	return string(encoded4), true, bytesLen
}

func redactSensitiveJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			if isSensitiveKey(k) {
				out[k] = "[REDACTED]"
				continue
			}
			out[k] = redactSensitiveJSON(vv)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, vv := range t {
			out = append(out, redactSensitiveJSON(vv))
		}
		return out
	default:
		return v
	}
}

func isSensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" {
		return false
	}

	// Retain allowlisted token counts and budgets for diagnostics.
	switch k {
	case "max_tokens",
		"max_output_tokens",
		"max_input_tokens",
		"max_completion_tokens",
		"max_tokens_to_sample",
		"budget_tokens",
		"prompt_tokens",
		"completion_tokens",
		"input_tokens",
		"output_tokens",
		"total_tokens",
		"token_count",
		"cache_creation_input_tokens",
		"cache_read_input_tokens":
		return false
	}

	// Exact matches (common credential fields).
	switch k {
	case "authorization",
		"proxy-authorization",
		"x-api-key",
		"api_key",
		"apikey",
		"access_token",
		"refresh_token",
		"id_token",
		"session_token",
		"token",
		"password",
		"passwd",
		"passphrase",
		"secret",
		"client_secret",
		"private_key",
		"jwt",
		"signature",
		"accesskeyid",
		"secretaccesskey":
		return true
	}

	// Suffix matches.
	for _, suffix := range []string{
		"_secret",
		"_token",
		"_id_token",
		"_session_token",
		"_password",
		"_passwd",
		"_passphrase",
		"_key",
		"secret_key",
		"private_key",
	} {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}

	// Substring matches (conservative, but errs on the side of privacy).
	for _, sub := range []string{
		"secret",
		"token",
		"password",
		"passwd",
		"passphrase",
		"privatekey",
		"private_key",
		"apikey",
		"api_key",
		"accesskeyid",
		"secretaccesskey",
		"bearer",
		"cookie",
		"credential",
		"session",
		"jwt",
		"signature",
	} {
		if strings.Contains(k, sub) {
			return true
		}
	}

	return false
}

func trimConversationArrays(root map[string]any, maxBytes int) (map[string]any, bool) {
	// Supported: anthropic/openai: messages; gemini: contents.
	if out, ok := trimArrayField(root, "messages", maxBytes); ok {
		return out, true
	}
	if out, ok := trimArrayField(root, "contents", maxBytes); ok {
		return out, true
	}
	return root, false
}

func trimArrayField(root map[string]any, field string, maxBytes int) (map[string]any, bool) {
	raw, ok := root[field]
	if !ok {
		return nil, false
	}
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil, false
	}

	// Keep at least the last message/content. Use binary search so we don't marshal O(n) times.
	// We are dropping from the *front* of the array (oldest context first).
	lo := 0
	hi := len(arr) - 1 // inclusive; hi ensures at least one item remains

	var best map[string]any
	found := false

	for lo <= hi {
		mid := (lo + hi) / 2
		candidateArr := arr[mid:]
		if len(candidateArr) == 0 {
			lo = mid + 1
			continue
		}

		next := querycache.ShallowMap(root)
		next[field] = candidateArr
		encoded, err := json.Marshal(next)
		if err != nil {
			// If marshal fails, try dropping more.
			lo = mid + 1
			continue
		}

		if len(encoded) <= maxBytes {
			best = next
			found = true
			// Try to keep more context by dropping fewer items.
			hi = mid - 1
			continue
		}

		// Need to drop more.
		lo = mid + 1
	}

	if found {
		return best, true
	}

	// Nothing fit (even with only one element); return the smallest slice and let the
	// caller fall back to shrinkToEssentials().
	next := querycache.ShallowMap(root)
	next[field] = arr[len(arr)-1:]
	return next, true
}

func shrinkToEssentials(root map[string]any) map[string]any {
	out := make(map[string]any)
	for _, key := range []string{
		"model",
		"stream",
		"max_tokens",
		"max_output_tokens",
		"max_input_tokens",
		"max_completion_tokens",
		"thinking",
		"temperature",
		"top_p",
		"top_k",
	} {
		if v, ok := root[key]; ok {
			out[key] = v
		}
	}

	// Keep only the last element of the conversation array.
	if v, ok := root["messages"]; ok {
		if arr, ok := v.([]any); ok && len(arr) > 0 {
			out["messages"] = []any{arr[len(arr)-1]}
		}
	}
	if v, ok := root["contents"]; ok {
		if arr, ok := v.([]any); ok && len(arr) > 0 {
			out["contents"] = []any{arr[len(arr)-1]}
		}
	}
	return out
}

func sanitizeErrorBodyForStorage(raw string, maxBytes int) (sanitized string, truncated bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}

	// Prefer JSON-safe sanitization when possible.
	if out, trunc, _ := sanitizeAndTrimJSONPayload([]byte(raw), maxBytes); out != "" {
		return out, trunc
	}

	// Non-JSON: best-effort truncate.
	if maxBytes > 0 && len(raw) > maxBytes {
		return truncateString(raw, maxBytes), true
	}
	return raw, false
}

// isContextDoneError reports cancellation or deadline expiry from the error or context.
func isContextDoneError(ctx context.Context, err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	if ctx == nil {
		return false
	}
	ctxErr := ctx.Err()
	// Drivers can return their own error text; check context state for cancellation or expiry.
	return errors.Is(ctxErr, context.Canceled) || errors.Is(ctxErr, context.DeadlineExceeded)
}

// truncateString truncates to a byte limit at a UTF-8 character boundary.
func truncateString(s string, max int) string { return logredact.TruncateUTF8(s, max) }
