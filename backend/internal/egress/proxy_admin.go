package egress

import (
	"context"
	"fmt"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// proxy_admin.go administers proxy records and combines connectivity and
// target-quality probe results for display.
// Proxy administration writes connection records before scheduling optional
// background latency probes. Connectivity and target-quality observations share
// a cache value, letting later latency-only checks retain the earlier quality
// assessment while the administrative result reflects the current repository
// operation.
//
// Update treats ordinary nonzero connection fields as patches but replaces the
// expiration and fallback group even for nil or zero values. Deletion first
// checks bound providers, and batch deletion records each failure so later IDs
// can still proceed. Quality grading combines expected endpoint statuses,
// warnings, failures, and challenges after base reachability. Cache writes and
// task admission are best effort, and queued probes use independent contexts
// owned by the application task tracker.

type (
	// ProxyTasks registers named asynchronous proxy probes with an
	// application-owned background task tracker.
	ProxyTasks interface {
		// Go registers a named callback and reports whether admission succeeded.
		// The application owns waiting, while callers keep captured mutable inputs
		// stable and provide the callback's context and request bounds.
		Go(string, func()) bool
	}
	// ProxyQualityProbe returns sequential endpoint observations for proxy quality
	// aggregation after base reachability.
	ProxyQualityProbe interface {
		// ProbeTargets returns one observed assessment item per selected target
		// through the proxy. Endpoint failures remain items so later targets run; a
		// shared-client construction failure returns an aggregate error.
		ProbeTargets(context.Context, string, []ProxyQualityTarget) ([]ProxyQualityCheckItem, error)
	}
)

// ProxyAdminOptions supplies observation timing, asynchronous task ownership,
// and diagnostic logging for administration.
type ProxyAdminOptions struct {
	// Now supplies observation timestamps to request handlers and admitted probes.
	// The callback supports their overlapping calls; nil selects time.Now at construction.
	Now func() time.Time // observation timestamp clock callback
	// Tasks is optional and owns admitted background probe lifetimes. ProxyAdmin
	// discards rejected admission and keeps persistence results independent of
	// whether a probe was accepted.
	Tasks       ProxyTasks  // optional background task owner
	Diagnostics Diagnostics // cache failure diagnostic sink
}

// ProxyAdmin coordinates proxy persistence, probe assessment, optional cached
// observations, and background latency work.
type ProxyAdmin struct {
	proxyRepo ProxyRepository // proxy administration persistence dependency
	// proxyProber can be nil for administration that does not run probes.
	// CheckProxyQuality returns a base-failure item when absent, while TestProxy
	// requires a configured prober.
	proxyProber       ProxyExitInfoProber // optional exit-information probe dependency
	proxyLatencyCache ProxyLatencyCache   // optional probe observation cache
	// qualityProbe supplies target items after successful base reachability.
	// CheckProxyQuality requires it at that phase, while create, update, and
	// listing operations do not invoke it.
	qualityProbe ProxyQualityProbe // target quality assessment dependency
	// tasks owns admitted probes using independent background contexts.
	// CreateProxy can capture its returned proxy pointer, so callers keep its
	// ID and connection values stable until that queued probe finishes.
	tasks       ProxyTasks       // optional background probe task owner
	diagnostics Diagnostics      // cache failure diagnostic sink
	now         func() time.Time // observation timestamp clock callback
}

// ProxyAdministrator provides proxy management, connectivity tests, and quality checks.
type ProxyAdministrator interface {
	// ListProxies returns the matching proxy page and total count for supplied
	// filters and pagination settings.
	ListProxies(ctx context.Context, page, pageSize int, protocol, status, search string, sortBy, sortOrder string) ([]Proxy, int64, error)
	// ListProxiesWithProviderCount returns a matching proxy page, count, and
	// cached probe observations. Cache errors are diagnostic and leave stored
	// proxy/count results available to the caller rather than failing the listing.
	ListProxiesWithProviderCount(ctx context.Context, page, pageSize int, protocol, status, search string, sortBy, sortOrder string) ([]ProxyWithProviderCount, int64, error)
	// GetAllProxies returns repository records whose persisted status is active,
	// without pagination.
	GetAllProxies(ctx context.Context) ([]Proxy, error)
	// GetAllProxiesWithProviderCount returns active-status proxy records with
	// provider counts and cached probe details. Cache lookup failure leaves
	// repository values available, and time-based expiration is separate from the
	// active-status filter.
	GetAllProxiesWithProviderCount(ctx context.Context) ([]ProxyWithProviderCount, error)
	// GetProxy returns the persisted proxy selected by its database identity.
	GetProxy(ctx context.Context, id int64) (*Proxy, error)
	// GetProxiesByIDs returns selected stored proxies in repository query order.
	// Empty input returns an empty list, and input order or repeated IDs does not
	// require repeated output records.
	GetProxiesByIDs(ctx context.Context, ids []int64) ([]Proxy, error)
	// CreateProxy persists an active proxy and optionally registers a background
	// latency probe. Empty fallback mode becomes none, proxy fallback requires a
	// backup ID, and negative warning days reject the write.
	//
	// Probe admission is attempted after persistence and its boolean result is
	// discarded, so a stored proxy can be returned without any queued probe. The
	// callback uses a background context and retains the returned proxy pointer.
	// Callers keep ID and connection values stable until admitted work finishes,
	// and the application task owner coordinates final waiting.
	CreateProxy(ctx context.Context, input *CreateProxyInput) (*Proxy, error)
	// UpdateProxy patches nonempty connection/status values and replaces the
	// complete expiration/fallback group for the selected proxy. Empty fallback
	// mode becomes none, self-backup or missing required backup fails, and warning
	// days cannot be negative.
	//
	// Name, protocol, host, port, and credentials retain current values when their
	// inputs are empty or zero. ExpiresAt, BackupProxyID, and ExpiryWarnDays are
	// replaced even when nil or zero, so a partial connection update needs to
	// carry desired expiration settings. Repository Update owns
	// connection-dependent persistence and snapshot changes before the returned
	// record is published.
	UpdateProxy(ctx context.Context, id int64, input *UpdateProxyInput) (*Proxy, error)
	// DeleteProxy checks the nondeleted-provider count and rejects deletion when
	// that observation is positive. It then delegates repository deletion and
	// returns count or delete errors. The count check does not lock concurrent
	// provider binding, so another binding can appear between the two operations.
	DeleteProxy(ctx context.Context, id int64) error
	// BatchDeleteProxies attempts IDs in input order and reports successful
	// deletions plus skipped failures. Empty input returns an empty result. Each
	// count query and deletion is a separate operation, and count observation does
	// not lock concurrent provider binding.
	//
	// Per-item count, in-use, and deletion errors stay in Skipped so later IDs are
	// attempted. Successful earlier deletions remain stored when a later item
	// fails; the batch supplies no enclosing transaction. Repeated IDs are
	// attempted repeatedly, with missing-row behavior determined by the repository
	// adapter.
	BatchDeleteProxies(ctx context.Context, ids []int64) (*ProxyBatchDeleteResult, error)
	// GetProxyProviders returns administrator summaries for nondeleted providers
	// bound to the selected proxy.
	GetProxyProviders(ctx context.Context, proxyID int64) ([]ProxyProviderSummary, error)
	// CheckProxyExists delegates duplicate lookup for host, port, username, and
	// password. Protocol is excluded from this repository comparison, allowing
	// batch creation to treat matching connection credentials as an existing
	// record.
	CheckProxyExists(ctx context.Context, host string, port int, username, password string) (bool, error)
	// TestProxy probes one stored proxy and returns connectivity failure as an
	// unsuccessful result rather than a service error. Repository lookup failure
	// still returns an error; a configured prober is required before executing the
	// request.
	//
	// Successful observations include response-header latency and exit metadata,
	// and failed observations include the prober's message. Each observation is
	// best-effort cached, with an earlier quality group retained when the incoming
	// probe has no quality values. Cache failure is diagnostic and does not
	// replace the probe result returned to the administrator.
	TestProxy(ctx context.Context, id int64) (*ProxyTestResult, error)
	// CheckProxyQuality combines base reachability with sequential configured
	// target assessments and returns one weighted result. Missing base probing or
	// base failure ends target probing with a failure item; a successful base
	// probe requires qualityProbe for the next phase.
	//
	// Target-client construction failure becomes an item, while individual target
	// classifications update pass, warning, failure, or challenge counters. Final
	// scoring and cached status use those counters independently of base success.
	// A best-effort snapshot stores quality details and available exit metadata;
	// cache failure does not replace the returned assessment.
	CheckProxyQuality(ctx context.Context, id int64) (*ProxyQualityCheckResult, error)
}

// NewProxyAdmin constructs administration with the supplied repository, probe,
// cache, quality, and task dependencies. Nil Now selects time.Now; ordinary
// writes/listing can omit probes, while TestProxy and post-base quality
// execution need their respective probe dependencies.
func NewProxyAdmin(repo ProxyRepository, prober ProxyExitInfoProber, cache ProxyLatencyCache, quality ProxyQualityProbe, options ProxyAdminOptions) *ProxyAdmin {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &ProxyAdmin{proxyRepo: repo, proxyProber: prober, proxyLatencyCache: cache, qualityProbe: quality, tasks: options.Tasks, diagnostics: options.Diagnostics, now: now}
}

// ListProxies returns the matching proxy page and total count for supplied
// filters and pagination settings.
func (s *ProxyAdmin) ListProxies(ctx context.Context, page, pageSize int, protocol, status, search string, sortBy, sortOrder string) ([]Proxy, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}
	proxies, result, err := s.proxyRepo.ListWithFilters(ctx, params, protocol, status, search)
	if err != nil {
		return nil, 0, err
	}
	return proxies, result.Total, nil
}

// ListProxiesWithProviderCount returns a matching proxy page, count, and cached
// probe observations. Cache errors are diagnostic and leave stored proxy/count
// results available to the caller rather than failing the listing.
func (s *ProxyAdmin) ListProxiesWithProviderCount(ctx context.Context, page, pageSize int, protocol, status, search string, sortBy, sortOrder string) ([]ProxyWithProviderCount, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}
	proxies, result, err := s.proxyRepo.ListWithFiltersAndProviderCount(ctx, params, protocol, status, search)
	if err != nil {
		return nil, 0, err
	}
	s.attachProxyLatency(ctx, proxies)
	return proxies, result.Total, nil
}

// GetAllProxies returns repository records whose persisted status is active,
// without pagination.
func (s *ProxyAdmin) GetAllProxies(ctx context.Context) ([]Proxy, error) {
	return s.proxyRepo.ListActive(ctx)
}

// GetAllProxiesWithProviderCount returns active-status proxy records with
// provider counts and cached probe details. Cache lookup failure leaves
// repository values available, and time-based expiration is separate from the
// active-status filter.
func (s *ProxyAdmin) GetAllProxiesWithProviderCount(ctx context.Context) ([]ProxyWithProviderCount, error) {
	proxies, err := s.proxyRepo.ListActiveWithProviderCount(ctx)
	if err != nil {
		return nil, err
	}
	s.attachProxyLatency(ctx, proxies)
	return proxies, nil
}

// GetProxy returns the persisted proxy selected by its database identity.
func (s *ProxyAdmin) GetProxy(ctx context.Context, id int64) (*Proxy, error) {
	return s.proxyRepo.GetByID(ctx, id)
}

// GetProxiesByIDs returns selected stored proxies in repository query order.
// Empty input returns an empty list, and input order or repeated IDs does not
// require repeated output records.
func (s *ProxyAdmin) GetProxiesByIDs(ctx context.Context, ids []int64) ([]Proxy, error) {
	return s.proxyRepo.ListByIDs(ctx, ids)
}

// CreateProxy persists an active proxy and optionally registers a background
// latency probe. Empty fallback mode becomes none, proxy fallback requires a
// backup ID, and negative warning days reject the write.
//
// Probe admission is attempted after persistence and its boolean result is
// discarded, so a stored proxy can be returned without any queued probe. The
// callback uses a background context and retains the returned proxy pointer.
// Callers keep ID and connection values stable until admitted work finishes, and
// the application task owner coordinates final waiting.
func (s *ProxyAdmin) CreateProxy(ctx context.Context, input *CreateProxyInput) (*Proxy, error) {
	mode := input.FallbackMode
	if mode == "" {
		mode = FallbackModeNone
	}
	if mode == FallbackModeProxy && input.BackupProxyID == nil {
		return nil, infraerrors.BadRequest("PROXY_BACKUP_REQUIRED", "backup proxy required when fallback_mode=proxy")
	}
	if input.ExpiryWarnDays < 0 {
		return nil, infraerrors.BadRequest("PROXY_WARN_DAYS_INVALID", "expiry_warn_days must be >= 0")
	}

	proxy := &Proxy{
		Name:           input.Name,
		Protocol:       input.Protocol,
		Host:           input.Host,
		Port:           input.Port,
		Username:       input.Username,
		Password:       input.Password,
		Status:         StatusActive,
		ExpiresAt:      input.ExpiresAt,
		FallbackMode:   mode,
		BackupProxyID:  input.BackupProxyID,
		ExpiryWarnDays: input.ExpiryWarnDays,
	}
	if err := s.proxyRepo.Create(ctx, proxy); err != nil {
		return nil, err
	}
	// Queue probing only after the record exists. Rejected task admission does
	// not reverse persistence, and the callback uses its own background context.
	if s.tasks != nil {
		s.tasks.Go("service/admin_proxy.go:CreateProxy", func() { s.probeProxyLatency(context.Background(), proxy) })
	}
	return proxy, nil
}

// UpdateProxy patches nonempty connection/status values and replaces the
// complete expiration/fallback group for the selected proxy. Empty fallback
// mode becomes none, self-backup or missing required backup fails, and warning
// days cannot be negative.
//
// Name, protocol, host, port, and credentials retain current values when their
// inputs are empty or zero. ExpiresAt, BackupProxyID, and ExpiryWarnDays are
// replaced even when nil or zero, so a partial connection update needs to carry
// desired expiration settings. Repository Update owns connection-dependent
// persistence and snapshot changes before the returned record is published.
func (s *ProxyAdmin) UpdateProxy(ctx context.Context, id int64, input *UpdateProxyInput) (*Proxy, error) {
	if input.BackupProxyID != nil && *input.BackupProxyID == id {
		return nil, infraerrors.BadRequest("PROXY_BACKUP_SELF", "backup proxy cannot be itself")
	}
	mode := input.FallbackMode
	if mode == "" {
		mode = FallbackModeNone
	}
	if mode == FallbackModeProxy && input.BackupProxyID == nil {
		return nil, infraerrors.BadRequest("PROXY_BACKUP_REQUIRED", "backup proxy required when fallback_mode=proxy")
	}
	if input.ExpiryWarnDays < 0 {
		return nil, infraerrors.BadRequest("PROXY_WARN_DAYS_INVALID", "expiry_warn_days must be >= 0")
	}

	proxy, err := s.proxyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if input.Name != "" {
		proxy.Name = input.Name
	}
	if input.Protocol != "" {
		proxy.Protocol = input.Protocol
	}
	if input.Host != "" {
		proxy.Host = input.Host
	}
	if input.Port != 0 {
		proxy.Port = input.Port
	}
	if input.Username != "" {
		proxy.Username = input.Username
	}
	if input.Password != "" {
		proxy.Password = input.Password
	}
	if input.Status != "" {
		proxy.Status = input.Status
	}
	// This group is a complete replacement even during a connection-field patch.
	// Carry desired expiration policy in input so nil or zero does not clear it accidentally.
	proxy.ExpiresAt = input.ExpiresAt
	proxy.FallbackMode = mode
	proxy.BackupProxyID = input.BackupProxyID
	proxy.ExpiryWarnDays = input.ExpiryWarnDays

	if err := s.proxyRepo.Update(ctx, proxy); err != nil {
		return nil, err
	}
	return proxy, nil
}

// DeleteProxy checks the nondeleted-provider count and rejects deletion when
// that observation is positive. It then delegates repository deletion and
// returns count or delete errors. The count check does not lock concurrent
// provider binding, so another binding can appear between the two operations.
func (s *ProxyAdmin) DeleteProxy(ctx context.Context, id int64) error {
	count, err := s.proxyRepo.CountProvidersByProxyID(ctx, id)
	if err != nil {
		return err
	}
	if count > 0 {
		return ErrProxyInUse
	}
	return s.proxyRepo.Delete(ctx, id)
}

// BatchDeleteProxies attempts IDs in input order and reports successful
// deletions plus skipped failures. Empty input returns an empty result. Each
// count query and deletion is a separate operation, and count observation does
// not lock concurrent provider binding.
//
// Per-item count, in-use, and deletion errors stay in Skipped so later IDs are
// attempted. Successful earlier deletions remain stored when a later item fails;
// the batch supplies no enclosing transaction. Repeated IDs are attempted
// repeatedly, with missing-row behavior determined by the repository adapter.
func (s *ProxyAdmin) BatchDeleteProxies(ctx context.Context, ids []int64) (*ProxyBatchDeleteResult, error) {
	result := &ProxyBatchDeleteResult{}
	if len(ids) == 0 {
		return result, nil
	}

	// Each ID has an independent administrative outcome. Record skipped
	// failures and continue so earlier successes remain and later IDs are attempted.
	for _, id := range ids {
		count, err := s.proxyRepo.CountProvidersByProxyID(ctx, id)
		if err != nil {
			result.Skipped = append(result.Skipped, ProxyBatchDeleteSkipped{
				ID:     id,
				Reason: err.Error(),
			})
			continue
		}
		if count > 0 {
			result.Skipped = append(result.Skipped, ProxyBatchDeleteSkipped{
				ID:     id,
				Reason: ErrProxyInUse.Error(),
			})
			continue
		}
		if err := s.proxyRepo.Delete(ctx, id); err != nil {
			result.Skipped = append(result.Skipped, ProxyBatchDeleteSkipped{
				ID:     id,
				Reason: err.Error(),
			})
			continue
		}
		result.DeletedIDs = append(result.DeletedIDs, id)
	}

	return result, nil
}

// GetProxyProviders returns administrator summaries for nondeleted providers
// bound to the selected proxy.
func (s *ProxyAdmin) GetProxyProviders(ctx context.Context, proxyID int64) ([]ProxyProviderSummary, error) {
	return s.proxyRepo.ListProviderSummariesByProxyID(ctx, proxyID)
}

// CheckProxyExists delegates duplicate lookup for host, port, username, and
// password. Protocol is excluded from this repository comparison, allowing
// batch creation to treat matching connection credentials as an existing
// record.
func (s *ProxyAdmin) CheckProxyExists(ctx context.Context, host string, port int, username, password string) (bool, error) {
	return s.proxyRepo.ExistsByHostPortAuth(ctx, host, port, username, password)
}

// TestProxy probes one stored proxy and returns connectivity failure as an
// unsuccessful result rather than a service error. Repository lookup failure
// still returns an error; a configured prober is required before executing the
// request.
//
// Successful observations include response-header latency and exit metadata,
// and failed observations include the prober's message. Each observation is
// best-effort cached, with an earlier quality group retained when the incoming
// probe has no quality values. Cache failure is diagnostic and does not replace
// the probe result returned to the administrator.
func (s *ProxyAdmin) TestProxy(ctx context.Context, id int64) (*ProxyTestResult, error) {
	proxy, err := s.proxyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	proxyURL := proxy.URL()
	exitInfo, latencyMs, err := s.proxyProber.ProbeProxy(ctx, proxyURL)
	if err != nil {
		s.saveProxyLatency(ctx, id, &ProxyLatencyInfo{
			Success:   false,
			Message:   err.Error(),
			UpdatedAt: s.now(),
		})
		return &ProxyTestResult{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	latency := latencyMs
	s.saveProxyLatency(ctx, id, &ProxyLatencyInfo{
		Success:     true,
		LatencyMs:   &latency,
		Message:     "Proxy is accessible",
		IPAddress:   exitInfo.IP,
		Country:     exitInfo.Country,
		CountryCode: exitInfo.CountryCode,
		Region:      exitInfo.Region,
		City:        exitInfo.City,
		UpdatedAt:   s.now(),
	})
	return &ProxyTestResult{
		Success:     true,
		Message:     "Proxy is accessible",
		LatencyMs:   latencyMs,
		IPAddress:   exitInfo.IP,
		City:        exitInfo.City,
		Region:      exitInfo.Region,
		Country:     exitInfo.Country,
		CountryCode: exitInfo.CountryCode,
	}, nil
}

// CheckProxyQuality combines base reachability with sequential configured
// target assessments and returns one weighted result. Missing base probing or
// base failure ends target probing with a failure item; a successful base probe
// requires qualityProbe for the next phase.
//
// Target-client construction failure becomes an item, while individual target
// classifications update pass, warning, failure, or challenge counters. Final
// scoring and cached status use those counters independently of base success. A
// best-effort snapshot stores quality details and available exit metadata;
// cache failure does not replace the returned assessment.
func (s *ProxyAdmin) CheckProxyQuality(ctx context.Context, id int64) (*ProxyQualityCheckResult, error) {
	proxy, err := s.proxyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	result := &ProxyQualityCheckResult{
		ProxyID:   id,
		Score:     100,
		Grade:     "A",
		CheckedAt: s.now().Unix(),
		Items:     make([]ProxyQualityCheckItem, 0, len(ProxyQualityTargets)+1),
	}

	proxyURL := proxy.URL()
	if s.proxyProber == nil {
		result.Items = append(result.Items, ProxyQualityCheckItem{
			Target:  "base_connectivity",
			Status:  "fail",
			Message: "Proxy probe service is not configured",
		})
		result.FailedCount++
		finalizeProxyQualityResult(result)
		s.saveProxyQualitySnapshot(ctx, id, result, nil)
		return result, nil
	}

	exitInfo, latencyMs, err := s.proxyProber.ProbeProxy(ctx, proxyURL)
	if err != nil {
		result.Items = append(result.Items, ProxyQualityCheckItem{
			Target:    "base_connectivity",
			Status:    "fail",
			LatencyMs: latencyMs,
			Message:   err.Error(),
		})
		result.FailedCount++
		finalizeProxyQualityResult(result)
		s.saveProxyQualitySnapshot(ctx, id, result, nil)
		return result, nil
	}

	result.ExitIP = exitInfo.IP
	result.Country = exitInfo.Country
	result.CountryCode = exitInfo.CountryCode
	result.BaseLatencyMs = latencyMs
	result.Items = append(result.Items, ProxyQualityCheckItem{
		Target:    "base_connectivity",
		Status:    "pass",
		LatencyMs: latencyMs,
		Message:   "Proxy egress connection is healthy",
	})
	result.PassedCount++

	items, err := s.qualityProbe.ProbeTargets(ctx, proxyURL, ProxyQualityTargets)
	if err != nil {
		result.Items = append(result.Items, ProxyQualityCheckItem{
			Target:  "http_client",
			Status:  "fail",
			Message: fmt.Sprintf("failed to create probe client: %v", err),
		})
		result.FailedCount++
		finalizeProxyQualityResult(result)
		s.saveProxyQualitySnapshot(ctx, id, result, exitInfo)
		return result, nil
	}

	for _, item := range items {
		result.Items = append(result.Items, item)
		switch item.Status {
		case "pass":
			result.PassedCount++
		case "warn":
			result.WarnCount++
		case "challenge":
			result.ChallengeCount++
		default:
			result.FailedCount++
		}
	}

	finalizeProxyQualityResult(result)
	s.saveProxyQualitySnapshot(ctx, id, result, exitInfo)
	return result, nil
}

// finalizeProxyQualityResult computes weighted score, grade, and count summary
// from accumulated assessment outcomes. Nil input does nothing. Warnings
// subtract ten, failures twenty-two, and challenges thirty points from one
// hundred, clamped at zero.
//
// Passed outcomes appear in the summary but do not add score. proxyQualityGrade
// converts the resulting total into a display letter, so a base failure and
// endpoint challenge can affect grade differently even when both make overall
// status unhealthy. The caller increments outcome counters before finalization
// and retains the underlying Items for diagnosis.
func finalizeProxyQualityResult(result *ProxyQualityCheckResult) {
	if result == nil {
		return
	}
	// The assessment subtracts different penalties for warning, failure, and
	// challenge evidence; clamp the combined score before applying display grades.
	score := max(100-result.WarnCount*10-result.FailedCount*22-result.ChallengeCount*30, 0)
	result.Score = score
	result.Grade = proxyQualityGrade(score)
	result.Summary = fmt.Sprintf(
		"Passed: %d, warnings: %d, failed: %d, challenges: %d",
		result.PassedCount,
		result.WarnCount,
		result.FailedCount,
		result.ChallengeCount,
	)
}

// proxyQualityGrade maps scores to A, B, C, D, or F for admin quality display.
// Thresholds are inclusive at ninety, seventy-five, sixty, and forty; lower
// values receive F.
func proxyQualityGrade(score int) string {
	// Score bands determine letters; cached overall status uses outcome-counter
	// precedence separately and can remain unhealthy despite a positive score.
	switch {
	case score >= 90:
		return "A"
	case score >= 75:
		return "B"
	case score >= 60:
		return "C"
	case score >= 40:
		return "D"
	default:
		return "F"
	}
}

// proxyQualityOverallStatus chooses a cached assessment status from accumulated
// counters. Nil input returns empty, and an assessment with no outcomes returns
// failed. Any challenge takes precedence, then failures, then warnings, with
// healthy selected only when passed outcomes remain as the highest applicable
// category.
//
// This priority differs from weighted scoring: a result can have a positive
// score and still be failed or challenged. The caller supplies finalized
// counters, and cache consumers use this classification alongside base
// connectivity and the separate display grade.
func proxyQualityOverallStatus(result *ProxyQualityCheckResult) string {
	if result == nil {
		return ""
	}
	if result.ChallengeCount > 0 {
		return "challenge"
	}
	if result.FailedCount > 0 {
		return "failed"
	}
	if result.WarnCount > 0 {
		return "warn"
	}
	if result.PassedCount > 0 {
		return "healthy"
	}
	return "failed"
}

// proxyQualityFirstCFRay returns the first nonempty ray from the ordered
// assessment items. Nil results or items without ray evidence return empty, so
// cache snapshots keep a sampled diagnostic identifier.
func proxyQualityFirstCFRay(result *ProxyQualityCheckResult) string {
	if result == nil {
		return ""
	}
	for _, item := range result.Items {
		if item.CFRay != "" {
			return item.CFRay
		}
	}
	return ""
}

// proxyQualityBaseConnectivityPass reports whether the first base_connectivity
// item has pass status. Nil input or an absent base item returns false, letting
// cache connectivity success stay separate from target-quality counts.
func proxyQualityBaseConnectivityPass(result *ProxyQualityCheckResult) bool {
	if result == nil {
		return false
	}
	for _, item := range result.Items {
		if item.Target == "base_connectivity" {
			return item.Status == "pass"
		}
	}
	return false
}

// saveProxyQualitySnapshot builds cached connectivity and quality values from a
// completed assessment. Nil input does nothing, and positive base latency is
// retained as an optional value. Available exit metadata supplies the cached
// address and location fields.
//
// Connectivity success comes from the base item, while overall quality status
// comes from outcome-counter precedence and the score and grade already
// computed. The first nonempty ray is retained as diagnostic evidence. This
// separation allows successful base reachability to coexist with target failures
// or challenges; saveProxyLatency performs best-effort storage and can report
// cache failure independently of the returned assessment.
func (s *ProxyAdmin) saveProxyQualitySnapshot(ctx context.Context, proxyID int64, result *ProxyQualityCheckResult, exitInfo *ProxyExitInfo) {
	if result == nil {
		return
	}
	score := result.Score
	checkedAt := result.CheckedAt
	info := &ProxyLatencyInfo{
		Success:          proxyQualityBaseConnectivityPass(result),
		Message:          result.Summary,
		QualityStatus:    proxyQualityOverallStatus(result),
		QualityScore:     &score,
		QualityGrade:     result.Grade,
		QualitySummary:   result.Summary,
		QualityCheckedAt: &checkedAt,
		QualityCFRay:     proxyQualityFirstCFRay(result),
		UpdatedAt:        s.now(),
	}
	if result.BaseLatencyMs > 0 {
		latency := result.BaseLatencyMs
		info.LatencyMs = &latency
	}
	if exitInfo != nil {
		info.IPAddress = exitInfo.IP
		info.Country = exitInfo.Country
		info.CountryCode = exitInfo.CountryCode
		info.Region = exitInfo.Region
		info.City = exitInfo.City
	}
	s.saveProxyLatency(ctx, proxyID, info)
}

// probeProxyLatency reads connection values for probing and the proxy ID when
// storing its observation. The caller keeps ID and connection values stable
// throughout this call. Missing probing or nil proxy input returns immediately;
// probe failure becomes cached failure metadata and cache failure remains
// diagnostic.
func (s *ProxyAdmin) probeProxyLatency(ctx context.Context, proxy *Proxy) {
	if s.proxyProber == nil || proxy == nil {
		return
	}
	exitInfo, latencyMs, err := s.proxyProber.ProbeProxy(ctx, proxy.URL())
	if err != nil {
		s.saveProxyLatency(ctx, proxy.ID, &ProxyLatencyInfo{
			Success:   false,
			Message:   err.Error(),
			UpdatedAt: s.now(),
		})
		return
	}

	latency := latencyMs
	s.saveProxyLatency(ctx, proxy.ID, &ProxyLatencyInfo{
		Success:     true,
		LatencyMs:   &latency,
		Message:     "Proxy is accessible",
		IPAddress:   exitInfo.IP,
		Country:     exitInfo.Country,
		CountryCode: exitInfo.CountryCode,
		Region:      exitInfo.Region,
		City:        exitInfo.City,
		UpdatedAt:   s.now(),
	})
}

// attachProxyLatency mutates the supplied listing slice with available cached
// observations and quality details. An absent cache or empty list does nothing;
// command failure is logged and retains repository listing values. Cached
// optional numeric pointers are retained in the listing.
func (s *ProxyAdmin) attachProxyLatency(ctx context.Context, proxies []ProxyWithProviderCount) {
	if s.proxyLatencyCache == nil || len(proxies) == 0 {
		return
	}

	ids := make([]int64, 0, len(proxies))
	for i := range proxies {
		ids = append(ids, proxies[i].ID)
	}

	latencies, err := s.proxyLatencyCache.GetProxyLatencies(ctx, ids)
	if err != nil {
		s.diagnostics.Log("service.admin", "Warning: load proxy latency cache failed: %v", err)
		return
	}

	for i := range proxies {
		info := latencies[proxies[i].ID]
		if info == nil {
			continue
		}
		if info.Success {
			proxies[i].LatencyStatus = "success"
			proxies[i].LatencyMs = info.LatencyMs
		} else {
			proxies[i].LatencyStatus = "failed"
		}
		proxies[i].LatencyMessage = info.Message
		proxies[i].IPAddress = info.IPAddress
		proxies[i].Country = info.Country
		proxies[i].CountryCode = info.CountryCode
		proxies[i].Region = info.Region
		proxies[i].City = info.City
		proxies[i].QualityStatus = info.QualityStatus
		proxies[i].QualityScore = info.QualityScore
		proxies[i].QualityGrade = info.QualityGrade
		proxies[i].QualitySummary = info.QualitySummary
		proxies[i].QualityChecked = info.QualityCheckedAt
	}
}

// saveProxyLatency writes one observation after merging an existing quality
// group when every incoming quality field is empty. Nil cache or input does
// nothing, and both lookup and write errors are suppressed or logged.
//
// The input is shallow-copied, and preserved quality pointers remain associated
// with the retrieved observation. Read and write are separate cache commands,
// so competing probes can overwrite one another; this merge does not serialize
// remote updates. Incoming partial quality data prevents carrying the older
// whole group, letting callers replace the assessment deliberately.
func (s *ProxyAdmin) saveProxyLatency(ctx context.Context, proxyID int64, info *ProxyLatencyInfo) {
	if s.proxyLatencyCache == nil || info == nil {
		return
	}

	// The incoming observation has its own connectivity timestamp. Carry the
	// stored assessment group only when none of its quality values was supplied.
	// Separate read/write commands can still race with another probe update.
	merged := *info
	if latencies, err := s.proxyLatencyCache.GetProxyLatencies(ctx, []int64{proxyID}); err == nil {
		if existing := latencies[proxyID]; existing != nil {
			if merged.QualityCheckedAt == nil &&
				merged.QualityScore == nil &&
				merged.QualityGrade == "" &&
				merged.QualityStatus == "" &&
				merged.QualitySummary == "" &&
				merged.QualityCFRay == "" {
				merged.QualityStatus = existing.QualityStatus
				merged.QualityScore = existing.QualityScore
				merged.QualityGrade = existing.QualityGrade
				merged.QualitySummary = existing.QualitySummary
				merged.QualityCheckedAt = existing.QualityCheckedAt
				merged.QualityCFRay = existing.QualityCFRay
			}
		}
	}

	if err := s.proxyLatencyCache.SetProxyLatency(ctx, proxyID, &merged); err != nil {
		s.diagnostics.Log("service.admin", "Warning: store proxy latency cache failed: %v", err)
	}
}
