package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Ollama browser usage is shared by eligible providers using the same API key,
// even when their proxy choices differ. Group resolution selects the newest
// configured session and allows matching encrypted sessions to supply newer
// snapshot observations. Snapshot and refresh-switch writes compare the
// anchor and observed managed state; session replacement and deletion compare
// the anchor while accepting the requested managed-state replacement.
//
// Manual and periodic queries coalesce by that API-key identity. The first
// callback owns the synchronous flight's context and mode, and four shared
// slots limit distinct fetches. Periodic cycles wait for their launched work
// before releasing local/instance ownership. Success and failure snapshots
// retain separate attempt/display times, and tracked shutdown checks
// cancellation before late persistence while waiting for actual query
// completion.

// Ollama manual refresh spacing, cycle selection count, and shared lease
// identity bound maintenance work before the service queries or persists a
// group snapshot.
const (
	ollamaCloudUsageManualRefreshInterval = 30 * time.Second            // minimum manual attempt spacing
	ollamaCloudUsageMaxPerCycle           = 20                          // maximum selected groups per cycle
	ollamaCloudUsageLeaderLockKey         = "ollama:cloud:usage:leader" // shared periodic cycle lease key
	ollamaCloudUsageLeaderLockTTL         = 2 * time.Minute             // periodic cycle lease lifetime
)

// OllamaProviderReader supplies records whose current identity anchors grouped
// browser usage maintenance.
type OllamaProviderReader interface {
	// GetByID returns the provider identity used to anchor one usage group
	// operation.
	GetByID(context.Context, int64) (*Record, error)
}

// OllamaUsageSettingsStore reads and saves global browser usage maintenance
// settings.
type OllamaUsageSettingsStore interface {
	// GetOllamaCloudUsageSettings returns nonnil global settings on
	// success.
	// RunDue reads Enabled before selecting groups, and refreshProvider
	// uses
	// the timing fields; the settings owner supplies decoding and defaults.
	GetOllamaCloudUsageSettings(context.Context) (*OllamaCloudUsageSettings, error)
	// SetOllamaCloudUsageSettings validates and persists the supplied
	// global
	// settings. The service forwards the pointer directly, so the settings
	// implementation controls timing defaults and caller-visible validation
	// errors.
	SetOllamaCloudUsageSettings(context.Context, *OllamaCloudUsageSettings) error
}

// OllamaSessionCipher encrypts stored browser cookies and decrypts them for
// controlled fetches.
type OllamaSessionCipher interface {
	// Encrypt converts normalized browser cookies into the opaque stored
	// session string.
	Encrypt(string) (string, error)
	// Decrypt recovers the browser cookie text used by the controlled
	// upstream
	// fetch.
	Decrypt(string) (string, error)
}

// OllamaUsageOptions supplies process configuration, transport fetches,
// scheduling randomness, and round leasing.
type OllamaUsageOptions struct {
	EncryptionKeyConfigured bool             // fixed encryption configuration available
	Now                     func() time.Time // observation and retry clock
	// Jitter receives a nanosecond upper bound from
	// NextOllamaCloudUsageDelay;
	// callers supply a nonnil bounded callback, and constructor does not
	// default it.
	Jitter     func(int64) int64
	InstanceID string // shared cycle lease owner
	// Fetch supplies a nonnil observation when its error is nil. Failure
	// labels
	// inside that observation are persisted, while a returned error stops
	// snapshot creation. Successful Data remains shared with the returned
	// snapshot; fetch owners keep it stable while storage and callers use
	// it.
	Fetch func(context.Context, OllamaUsageFetchInput) (*OllamaUsageObservation, error)
	// Lease optionally arbitrates cycle ownership; admitted calls supply a
	// usable release function and rejected calls cause RunDue to skip the
	// cycle.
	Lease func(context.Context, string, string, time.Duration) (func(), bool)
	Log   func(string, ...any) // maintenance failure diagnostic callback
}

// OllamaCloudUsageService manages API-key groups sharing browser session and
// usage snapshots.
type OllamaCloudUsageService struct {
	// providerRepo supplies retained row reads; group resolution and writes
	// require its OllamaUsageRepository capability, with unsupported
	// resolution
	// skipped and unsupported mutations rejected.
	providerRepo OllamaProviderReader // retained provider state reader
	// settingService supplies dynamic global settings; nil reads use
	// disabled
	// defaults, while UpdateSettings rejects persistence when the settings
	// dependency is absent.
	settingService OllamaUsageSettingsStore // global usage settings dependency
	// encryptor converts browser cookies for storage and fetching; saving
	// or
	// refreshing rejects a missing cipher dependency before using a
	// session.
	encryptor OllamaSessionCipher // browser cookie encryption dependency
	// encryptionKeyConfigured gates session saving and fetching from
	// process
	// configuration; a supplied cipher alone cannot authorize either path
	// when
	// this flag is false.
	encryptionKeyConfigured bool // fixed encryption availability flag
	// runtime tracks initial, periodic, and manual refresh activity until
	// actual completion; StopContext delegates admission closure,
	// cancellation,
	// and waiting to this holder.
	runtime *OllamaUsageRuntime
	// cycleMu serializes RunDue scans through their group waits; lock
	// acquisition follows cancellation so shutdown can end a queued cycle.
	cycleMu *RefreshLock
	// refreshGroup and refreshSlots coordinate API-key callbacks across
	// manual
	// and periodic callers. The first callback supplies context and mode,
	// joining calls wait synchronously, and four slots bound distinct
	// callbacks.
	refreshGroup singleflight.Group // API-key group callback coalescing
	refreshSlots chan struct{}      // active shared callback permits
	now          func() time.Time   // normalized observation and retry clock
	options      OllamaUsageOptions // retained transport and lease options
}

// OllamaUsageRepository compares provider/group identity while maintaining
// managed browser usage state.
type OllamaUsageRepository interface {
	// ListOllamaCloudUsageGroupProviders returns eligible records sharing
	// the
	// selected Ollama API-key identities. ResolveProviders chooses a
	// configured
	// source and may assign borrowed managed values to the caller-owned
	// records.
	ListOllamaCloudUsageGroupProviders(context.Context, []*Record) ([]Record, error)
	// SaveOllamaCloudUsageSession replaces group session and refresh
	// settings
	// using encrypted cookies. The writer compares the anchor identity and
	// clears previous snapshot data before the service rereads
	// administrator
	// state.
	SaveOllamaCloudUsageSession(context.Context, *Record, string, bool) error
	// DeleteOllamaCloudUsageSession removes the group's three managed usage
	// keys after comparing the supplied anchor identity. The service
	// performs
	// its display reread separately, so a read error can follow this
	// accepted
	// deletion.
	DeleteOllamaCloudUsageSession(context.Context, *Record) error
	// SetOllamaCloudUsageAutoRefresh changes the group refresh flag with
	// the
	// observed session/state comparison. Changes require a nonempty stored
	// session, and identity or managed-state changes can reject the write.
	SetOllamaCloudUsageAutoRefresh(context.Context, *Record, bool) error
	// UpdateOllamaCloudUsageSnapshot replaces group observations while
	// retaining the supplied managed session and refresh settings. The
	// writer
	// compares anchor/proxy identity and the jointly observed managed
	// values
	// before accepting replacement.
	UpdateOllamaCloudUsageSnapshot(context.Context, *Record, *OllamaCloudUsageSnapshot) error
	// DisableOllamaCloudUsageAutoRefresh conditionally disables the
	// observed
	// group and removes its loaded snapshot. A newer identity or managed
	// state
	// can reject the change; RunDue logs failure and continues other
	// groups.
	DisableOllamaCloudUsageAutoRefresh(context.Context, *Record) error
	// ListDueOllamaCloudUsageProviders selects due group representatives
	// using
	// supplied activity timing and count limit. Each returned LastUsedAt
	// carries the group-wide latest activity, and the service rechecks
	// eligibility before starting a refresh.
	ListDueOllamaCloudUsageProviders(context.Context, time.Time, time.Duration, time.Duration, int) ([]Record, error)
}

// NewOllamaCloudUsageService constructs an idle service with four shared fetch
// slots and a tracked usage runtime. Missing clock and logger receive defaults;
// callers provide a usable Jitter and Fetch before successful or failed
// snapshots need retry scheduling.
func NewOllamaCloudUsageService(repo OllamaProviderReader, settings OllamaUsageSettingsStore, cipher OllamaSessionCipher, options OllamaUsageOptions) *OllamaCloudUsageService {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Log == nil {
		options.Log = func(string, ...any) {}
	}
	s := &OllamaCloudUsageService{providerRepo: repo, settingService: settings, encryptor: cipher, encryptionKeyConfigured: options.EncryptionKeyConfigured, cycleMu: NewRefreshLock(), refreshSlots: make(chan struct{}, 4), now: options.Now, options: options}
	s.runtime = NewOllamaUsageRuntime(s.RunDue, options.Log)
	return s
}

// StartContext starts the retained runtime's immediate and periodic maintenance
// scans under ctx. Nil services succeed without work; the runtime owns
// repeated-start handling, cancellation, and permanent shutdown admission.
func (s *OllamaCloudUsageService) StartContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.runtime.StartContext(ctx)
}

// Stop cancels and waits for tracked manual and periodic work using a
// background context. It discards the delegated result; callers requiring a
// bounded wait or shutdown reporting use StopContext.
func (s *OllamaCloudUsageService) Stop() { _ = s.StopContext(context.Background()) }

// StopContext delegates cancellation and bounded completion waiting to the
// retained usage runtime. Nil services succeed, and the runtime retains its
// first shutdown result even when unfinished callbacks later return.
func (s *OllamaCloudUsageService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.runtime.StopContext(ctx)
}

// GetSettings returns stored global usage settings when the settings dependency
// exists. A nil service or absent store returns fresh disabled defaults so
// callers can inspect configuration with maintenance unavailable.
func (s *OllamaCloudUsageService) GetSettings(ctx context.Context) (*OllamaCloudUsageSettings, error) {
	if s == nil || s.settingService == nil {
		return DefaultOllamaCloudUsageSettings(), nil
	}
	return s.settingService.GetOllamaCloudUsageSettings(ctx)
}

// UpdateSettings passes the supplied settings to the configured store for
// validation and persistence. A nil service or absent settings store returns
// ErrOllamaCloudUsageUnavailable; this method supplies no local settings cache.
func (s *OllamaCloudUsageService) UpdateSettings(ctx context.Context, settings *OllamaCloudUsageSettings) error {
	if s == nil || s.settingService == nil {
		return ErrOllamaCloudUsageUnavailable
	}
	return s.settingService.SetOllamaCloudUsageSettings(ctx, settings)
}

// GetState loads a provider, resolves its shared API-key group's managed
// fields, and returns decoded administrator state with encryption-key
// availability. Missing service storage returns unavailable; group lookup
// errors propagate before a display state is built.
func (s *OllamaCloudUsageService) GetState(ctx context.Context, providerID int64) (*OllamaCloudUsageState, error) {
	if s == nil || s.providerRepo == nil {
		return nil, ErrOllamaCloudUsageUnavailable
	}
	provider, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if err := s.ResolveProviders(ctx, []*Record{provider}); err != nil {
		return nil, err
	}
	state := OllamaCloudUsageStateFromProvider(provider)
	s.EnrichState(state)
	return state, nil
}

// ResolveProviders replaces managed Extra fields on supplied eligible records
// with their API-key group's selected state. It skips absent dependencies,
// empty input, or a reader lacking group repository support. Callers own these
// record mutations; copied top-level maps still retain nested values borrowed
// from the loaded siblings.
//
// The newest configured record supplies session and refresh settings, with
// lower ID breaking an UpdatedAt tie. Matching encrypted-session strings can
// supply a newer snapshot by LastAttemptAt. Temporary top-level copies keep
// that selection from changing a loaded sibling's Extra; eligible records with
// no configured source have their managed fields removed. Unrelated Extra
// values remain on each target.
func (s *OllamaCloudUsageService) ResolveProviders(ctx context.Context, providers []*Record) error {
	if s == nil || s.providerRepo == nil || len(providers) == 0 {
		return nil
	}
	writer, ok := s.providerRepo.(OllamaUsageRepository)
	if !ok {
		return nil
	}
	eligible := make([]*Record, 0, len(providers))
	for _, provider := range providers {
		if _, ok := OllamaCloudUsageGroupFingerprint(provider); ok {
			eligible = append(eligible, provider)
		}
	}
	if len(eligible) == 0 {
		return nil
	}
	siblings, err := writer.ListOllamaCloudUsageGroupProviders(ctx, eligible)
	if err != nil {
		return fmt.Errorf("resolve Ollama Cloud usage groups: %w", err)
	}
	sources := make(map[string]*Record)
	// Choose session and refresh settings first; later snapshot selection
	// excludes observations belonging to a different encrypted session
	// string.
	for index := range siblings {
		candidate := &siblings[index]
		fingerprint, valid := OllamaCloudUsageGroupFingerprint(candidate)
		if !valid || !OllamaCloudUsageConfigured(candidate) {
			continue
		}
		current := sources[fingerprint]
		if current == nil || candidate.UpdatedAt.After(current.UpdatedAt) ||
			(candidate.UpdatedAt.Equal(current.UpdatedAt) && candidate.ID < current.ID) {
			sources[fingerprint] = candidate
		}
	}
	resolvedSources := make(map[string]*Record, len(sources))
	for fingerprint, source := range sources {
		// Copy the selected source's top-level Extra before changing
		// snapshot
		// selection, so loaded sibling maps retain their own managed
		// entries.
		clone := *source
		clone.Extra = make(map[string]any, len(source.Extra))
		maps.Copy(clone.Extra, source.Extra)
		resolvedSources[fingerprint] = &clone
	}
	for index := range siblings {
		candidate := &siblings[index]
		fingerprint, valid := OllamaCloudUsageGroupFingerprint(candidate)
		source := resolvedSources[fingerprint]
		if !valid || source == nil || !sameOllamaCloudUsageSession(source, candidate) {
			continue
		}
		// Select display data by attempt time only after matching
		// session text,
		// keeping a newer observation from another browser session out
		// of this group.
		candidateSnapshot := DecodeOllamaCloudUsageSnapshot(candidate.Extra)
		currentSnapshot := DecodeOllamaCloudUsageSnapshot(source.Extra)
		if candidateSnapshot != nil && (currentSnapshot == nil || candidateSnapshot.LastAttemptAt.After(currentSnapshot.LastAttemptAt)) {
			source.Extra[OllamaCloudUsageSnapshotExtraKey] = candidate.Extra[OllamaCloudUsageSnapshotExtraKey]
		}
	}
	for _, provider := range eligible {
		fingerprint, _ := OllamaCloudUsageGroupFingerprint(provider)
		applyOllamaCloudUsageManagedExtra(provider, resolvedSources[fingerprint])
	}
	return nil
}

// SaveSession normalizes and encrypts browser cookies for an eligible
// provider's API-key group. It requires fixed encryption configuration and a
// writable group repository, retains an existing enabled refresh switch, then
// rereads state; a reread error can follow a completed save.
func (s *OllamaCloudUsageService) SaveSession(ctx context.Context, providerID int64, session string) (*OllamaCloudUsageState, error) {
	if s == nil || s.providerRepo == nil || s.encryptor == nil {
		return nil, ErrOllamaCloudUsageUnavailable
	}
	if !s.encryptionKeyConfigured {
		return nil, ErrOllamaCloudUsageEncryptionKey
	}
	normalized, err := egress.NormalizeOllamaCloudUsageCookie(session)
	if err != nil {
		return nil, apperror.BadRequest("INVALID_OLLAMA_CLOUD_USAGE_SESSION", err.Error())
	}
	provider, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if !IsOllamaCloudUsageProvider(provider) {
		return nil, ErrOllamaCloudUsageProviderInvalid
	}
	if err := s.ResolveProviders(ctx, []*Record{provider}); err != nil {
		return nil, err
	}
	ciphertext, err := s.encryptor.Encrypt(normalized)
	if err != nil {
		return nil, fmt.Errorf("encrypt Ollama web session: %w", err)
	}
	writer, ok := s.providerRepo.(OllamaUsageRepository)
	if !ok {
		return nil, ErrOllamaCloudUsageUnavailable
	}
	preserveAutoRefresh := OllamaCloudUsageConfigured(provider) && OllamaCloudUsageAutoRefreshEnabled(provider)
	if err := writer.SaveOllamaCloudUsageSession(ctx, provider, ciphertext, preserveAutoRefresh); err != nil {
		return nil, err
	}
	// Read display state after session persistence, whose success can
	// remain
	// committed even if this subsequent read returns an error.
	return s.GetState(ctx, providerID)
}

// DeleteSession clears managed session, refresh, and snapshot state for an
// eligible provider's API-key group, then rereads administrator state. Storage
// or resolution errors propagate; failure during the final reread can occur
// after deletion succeeded.
func (s *OllamaCloudUsageService) DeleteSession(ctx context.Context, providerID int64) (*OllamaCloudUsageState, error) {
	if s == nil || s.providerRepo == nil {
		return nil, ErrOllamaCloudUsageUnavailable
	}
	provider, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if !IsOllamaCloudUsageProvider(provider) {
		return nil, ErrOllamaCloudUsageProviderInvalid
	}
	if err := s.ResolveProviders(ctx, []*Record{provider}); err != nil {
		return nil, err
	}
	writer, ok := s.providerRepo.(OllamaUsageRepository)
	if !ok {
		return nil, ErrOllamaCloudUsageUnavailable
	}
	// Delete the managed group state before rereading its display; an
	// accepted
	// delete remains stored even when that later read fails.
	if err := writer.DeleteOllamaCloudUsageSession(ctx, provider); err != nil {
		return nil, err
	}
	return s.GetState(ctx, providerID)
}

// SetAutoRefresh saves the requested group refresh switch and rereads
// administrator state. Enabling requires a configured session on an eligible
// provider; persistence is delegated to the identity-comparing repository, and
// final read failure can follow an accepted write.
func (s *OllamaCloudUsageService) SetAutoRefresh(ctx context.Context, providerID int64, enabled bool) (*OllamaCloudUsageState, error) {
	if s == nil || s.providerRepo == nil {
		return nil, ErrOllamaCloudUsageUnavailable
	}
	provider, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if !IsOllamaCloudUsageProvider(provider) {
		return nil, ErrOllamaCloudUsageProviderInvalid
	}
	if err := s.ResolveProviders(ctx, []*Record{provider}); err != nil {
		return nil, err
	}
	if enabled && !OllamaCloudUsageConfigured(provider) {
		return nil, ErrOllamaCloudUsageSessionRequired
	}
	writer, ok := s.providerRepo.(OllamaUsageRepository)
	if !ok {
		return nil, ErrOllamaCloudUsageUnavailable
	}
	// Persist the compared refresh flag before rebuilding the displayed
	// state;
	// a later state-read error cannot undo an accepted toggle.
	if err := writer.SetOllamaCloudUsageAutoRefresh(ctx, provider, enabled); err != nil {
		return nil, err
	}
	return s.GetState(ctx, providerID)
}

// Refresh registers a manual query with shutdown tracking, loads settings, and
// refreshes the provider's shared API-key group before rereading state. The
// group callback enforces manual spacing unless it joins an already-running
// callback; errors stop the final state read. A final state-read error can
// follow an already stored snapshot and does not undo that accepted write.
func (s *OllamaCloudUsageService) Refresh(ctx context.Context, providerID int64) (*OllamaCloudUsageState, error) {
	if s == nil {
		return nil, ErrOllamaCloudUsageUnavailable
	}
	ctx, finish, err := s.runtime.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()

	settings, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.refreshProvider(ctx, providerID, settings, false); err != nil {
		return nil, err
	}
	return s.GetState(ctx, providerID)
}

// RunDue performs a tracked scan when global maintenance is enabled,
// serializing cycles through the cancellable local lock and optional instance
// lease. The repository selects at most twenty due API-key groups, and the
// service rechecks activity, active state, session configuration, and per-group
// enablement before launching refresh work.
//
// Duplicate fingerprints share one candidate, while distinct callbacks use the
// shared four-slot limit in refreshProvider. Per-group failures are logged so
// other groups continue; those errors do not become the scan result. An
// identity-change failure attempts to disable the
// observed group's automatic refresh through a conditional repository write.
// The cycle waits for launched work before releasing its lease and activity
// registration; cancellation-sensitive dependencies allow shutdown to complete
// that wait.
func (s *OllamaCloudUsageService) RunDue(ctx context.Context) error {
	if s == nil || s.providerRepo == nil {
		return nil
	}
	ctx, finish, err := s.runtime.Begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if err := s.cycleMu.Lock(ctx); err != nil {
		return err
	}
	defer s.cycleMu.Unlock()
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return err
	}
	if !settings.Enabled {
		return nil
	}
	release, acquired := s.acquireLease(ctx)
	if !acquired {
		return nil
	}
	defer release()

	writer, ok := s.providerRepo.(OllamaUsageRepository)
	if !ok {
		return ErrOllamaCloudUsageUnavailable
	}
	now := s.currentTime()
	debounce, maxWait := OllamaCloudUsageDurations(settings)
	providers, err := writer.ListDueOllamaCloudUsageProviders(ctx, now, debounce, maxWait, ollamaCloudUsageMaxPerCycle)
	if err != nil {
		return fmt.Errorf("list due Ollama Cloud usage providers: %w", err)
	}
	var group errgroup.Group
	seenGroups := make(map[string]struct{}, len(providers))
	for index := range providers {
		provider := providers[index]
		fingerprint, valid := OllamaCloudUsageGroupFingerprint(&provider)
		if !valid || !provider.IsActive() || !OllamaCloudUsageConfigured(&provider) || !OllamaCloudUsageAutoRefreshEnabled(&provider) {
			continue
		}
		if _, duplicate := seenGroups[fingerprint]; duplicate {
			continue
		}
		seenGroups[fingerprint] = struct{}{}
		snapshot := DecodeOllamaCloudUsageSnapshot(provider.Extra)
		// ListDueOllamaCloudUsageProviders puts the group's latest
		// activity in
		// LastUsedAt, so this selected record can be due after another
		// group
		// member was used.
		if !OllamaCloudUsageIsAutoRefreshDue(snapshot, provider.LastUsedAt, now, debounce, maxWait) {
			continue
		}
		providerID := provider.ID
		expected := provider
		// Launch one refresh per selected fingerprint, then wait before
		// releasing cycle ownership; logged per-group failures leave
		// the
		// remaining group work reachable.
		group.Go(func() error {
			if _, refreshErr := s.refreshProvider(ctx, providerID, settings, true); refreshErr != nil {
				// A stale observed identity cannot safely stay
				// in automatic
				// refresh; attempt a conditional disable using
				// the selected
				// candidate and log failure.
				if errors.Is(refreshErr, ErrOllamaCloudUsageIdentityChanged) {
					if disableErr := writer.DisableOllamaCloudUsageAutoRefresh(ctx, &expected); disableErr != nil {
						s.options.Log("disable_auto_refresh_failed: provider_id=%d err=%v", providerID, disableErr)
					}
					return nil
				}
				s.options.Log("refresh_due_failed: provider_id=%d err=%v", providerID, refreshErr)
			}
			return nil
		})
	}
	return group.Wait()
}

// refreshProvider coalesces an API-key group's refresh and returns the
// callback's shared snapshot pointer. It captures the first caller's context
// and mode inside synchronous singleflight.Do; joining callers wait for that
// callback and cannot cancel their wait independently. Callers keep shared
// returned data stable and use GetState for decoded display copies.
//
// The callback waits for one of four fetch slots, rereads group identity,
// resolves its session, and applies the initiating mode's manual cooldown or
// periodic activity checks. Missing periodic eligibility returns nil without
// error. Group-activity lookup failure keeps this provider's narrower activity
// signal. A settings nil value uses defaults; current record/session
// comparisons are ultimately enforced by the repository snapshot writer.
func (s *OllamaCloudUsageService) refreshProvider(ctx context.Context, providerID int64, settings *OllamaCloudUsageSettings, requireEnabled bool) (*OllamaCloudUsageSnapshot, error) {
	if s == nil || s.providerRepo == nil {
		return nil, ErrOllamaCloudUsageUnavailable
	}
	if settings == nil {
		settings = DefaultOllamaCloudUsageSettings()
	}
	intervalMinutes := settings.IntervalMinutes
	debounce, maxWait := OllamaCloudUsageDurations(settings)
	anchor, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil {
		return nil, err
	}
	key, valid := OllamaCloudUsageGroupFingerprint(anchor)
	if !valid {
		return nil, ErrOllamaCloudUsageProviderInvalid
	}
	// Use one callback per API-key group so session and snapshot work is
	// shared; joining callers inherit that callback's context and
	// initiating
	// mode.
	value, err, _ := s.refreshGroup.Do(key, func() (any, error) {
		// Admit the shared callback through the four-slot channel
		// before
		// loading and fetching; release its permit even when later
		// preparation
		// or persistence fails.
		select {
		case s.refreshSlots <- struct{}{}:
			defer func() { <-s.refreshSlots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		provider, loadErr := s.providerRepo.GetByID(ctx, providerID)
		if loadErr != nil {
			return nil, loadErr
		}
		currentKey, currentValid := OllamaCloudUsageGroupFingerprint(provider)
		if !currentValid {
			return nil, ErrOllamaCloudUsageProviderInvalid
		}
		if currentKey != key {
			return nil, ErrOllamaCloudUsageIdentityChanged
		}
		if err := s.ResolveProviders(ctx, []*Record{provider}); err != nil {
			return nil, err
		}
		if !OllamaCloudUsageConfigured(provider) {
			return nil, ErrOllamaCloudUsageSessionRequired
		}
		if !requireEnabled {
			if snapshot := DecodeOllamaCloudUsageSnapshot(provider.Extra); snapshot != nil && !snapshot.LastAttemptAt.IsZero() {
				retryAt := snapshot.LastAttemptAt.Add(ollamaCloudUsageManualRefreshInterval)
				if now := s.currentTime(); now.Before(retryAt) {
					remaining := retryAt.Sub(now)
					// Round the manual cooldown upward to
					// whole seconds.
					seconds := int((remaining + time.Second - 1) / time.Second)
					return nil, ErrOllamaCloudUsageRefreshRateLimited.WithMetadata(map[string]string{
						"retry_after_seconds": strconv.Itoa(seconds),
					})
				}
			}
		}
		if requireEnabled {
			if !provider.IsActive() || !OllamaCloudUsageAutoRefreshEnabled(provider) {
				return nil, nil
			}
			groupLastUsed := provider.LastUsedAt
			if writer, ok := s.providerRepo.(OllamaUsageRepository); ok {
				siblings, listErr := writer.ListOllamaCloudUsageGroupProviders(ctx, []*Record{provider})
				if listErr != nil {
					// Use this provider's activity if group
					// lookup fails. The narrower
					// signal may skip a due refresh, so
					// retain the lookup error in
					// diagnostics while evaluating that
					// available activity.
					s.options.Log(
						"group_last_used_lookup_failed: provider_id=%d err=%v", provider.ID, listErr)
				} else {
					groupLastUsed = MaxOllamaCloudUsageGroupLastUsed(siblings)
				}
			}
			if !OllamaCloudUsageIsAutoRefreshDue(DecodeOllamaCloudUsageSnapshot(provider.Extra), groupLastUsed, s.currentTime(), debounce, maxWait) {
				return nil, nil
			}
		}
		return s.refreshLoadedProvider(ctx, provider, intervalMinutes)
	})
	if err != nil || value == nil {
		return nil, err
	}
	snapshot, ok := value.(*OllamaCloudUsageSnapshot)
	if !ok {
		return nil, fmt.Errorf("invalid Ollama Cloud usage refresh result")
	}
	return snapshot, nil
}

// refreshLoadedProvider fetches one resolved provider after decrypting and
// normalizing its configured browser session. The proxy ID must match its
// loaded proxy; Fetch must return a nonnil observation on success, and Jitter
// must be usable when retry timing is computed.
//
// A nonempty observation Failure selects persistFailure even when Fetch itself
// returned no error. Transport errors propagate without a new snapshot.
// Successful observations retain their Data pointer, stamp the time captured
// before the fetch, compute retry metadata, and ask updateSnapshot to compare
// cancellation and stored identity before returning the new snapshot.
func (s *OllamaCloudUsageService) refreshLoadedProvider(ctx context.Context, provider *Record, intervalMinutes int) (*OllamaCloudUsageSnapshot, error) {
	now := s.currentTime().UTC()
	ciphertext, _ := provider.Extra[OllamaCloudUsageSessionExtraKey].(string)
	if ciphertext == "" {
		return nil, ErrOllamaCloudUsageSessionRequired
	}
	if !s.encryptionKeyConfigured || s.encryptor == nil {
		return nil, ErrOllamaCloudUsageEncryptionKey
	}
	cookie, err := s.encryptor.Decrypt(ciphertext)
	if err != nil {
		return nil, apperror.ServiceUnavailable("OLLAMA_CLOUD_USAGE_SESSION_DECRYPT_FAILED", "stored Ollama web session cannot be decrypted")
	}
	cookie, err = egress.NormalizeOllamaCloudUsageCookie(cookie)
	if err != nil {
		return nil, apperror.ServiceUnavailable("OLLAMA_CLOUD_USAGE_SESSION_INVALID", "stored Ollama web session is invalid")
	}
	if s.options.Fetch == nil {
		return nil, ErrOllamaCloudUsageUnavailable
	}
	proxyURL := ""
	if provider.ProxyID != nil {
		if provider.Proxy == nil || provider.Proxy.ID != *provider.ProxyID {
			return nil, ErrOllamaCloudUsageIdentityChanged
		}
		proxyURL = provider.Proxy.URL()
	}
	observation, err := s.options.Fetch(ctx, OllamaUsageFetchInput{ProviderID: provider.ID, Concurrency: provider.Concurrency, ProxyURL: proxyURL, Cookie: cookie, ObservedAt: now})
	if err != nil {
		return nil, err
	}
	if observation.Failure != "" {
		return s.persistFailure(ctx, provider, intervalMinutes, now, observation.HTTPStatus, observation.Failure, observation.RetryAfter, observation.Unauthorized)
	}
	snapshot := &OllamaCloudUsageSnapshot{
		Status:        OllamaCloudUsageStatusOK,
		Data:          observation.Data,
		FetchedAt:     &now,
		LastAttemptAt: now,
		NextRefreshAt: now.Add(NextOllamaCloudUsageDelay(intervalMinutes, 0, 0, s.options.Jitter)),
		HTTPStatus:    observation.HTTPStatus,
	}
	if err := s.updateSnapshot(ctx, provider, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// persistFailure constructs and stores the latest failed attempt while
// retaining prior successful display data and FetchedAt. It increments the
// prior stored failure count when present, selects unauthorized status from the
// supplied flag, and derives retry timing through NextOllamaCloudUsageDelay.
//
// The retained pointers come from a decoded previous snapshot and are assigned
// to the replacement snapshot. The group writer receives the observed
// provider/session for comparison, and write failure returns an error rather
// than a successful replacement. Cancellation is checked by updateSnapshot
// before the repository call; no local retry or rollback follows a rejected
// write.
func (s *OllamaCloudUsageService) persistFailure(
	ctx context.Context,
	provider *Record,
	intervalMinutes int,
	now time.Time,
	httpStatus int,
	reason string,
	retryAfterDuration time.Duration,
	unauthorized bool,
) (*OllamaCloudUsageSnapshot, error) {
	previous := DecodeOllamaCloudUsageSnapshot(provider.Extra)
	failureCount := 1
	if previous != nil {
		failureCount = previous.FailureCount + 1
	}
	status := OllamaCloudUsageStatusFailed
	if unauthorized {
		status = OllamaCloudUsageStatusUnauthorized
	}
	snapshot := &OllamaCloudUsageSnapshot{
		Status:        status,
		LastAttemptAt: now,
		NextRefreshAt: now.Add(NextOllamaCloudUsageDelay(intervalMinutes, failureCount, retryAfterDuration, s.options.Jitter)),
		FailureCount:  failureCount,
		HTTPStatus:    httpStatus,
		LastError:     reason,
	}
	// Keep the last successful display data and observation time after
	// failure;
	// retry status and timing describe this new attempt independently.
	if previous != nil {
		snapshot.Data = previous.Data
		snapshot.FetchedAt = previous.FetchedAt
	}
	if err := s.updateSnapshot(ctx, provider, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// updateSnapshot checks cancellation before asking the group repository to
// persist the observation. Missing writer support returns unavailable, and
// repository identity/state comparison errors propagate; a concurrent
// cancellation after the check is handled by the store's context use.
func (s *OllamaCloudUsageService) updateSnapshot(ctx context.Context, provider *Record, snapshot *OllamaCloudUsageSnapshot) error {
	// Check cancellation before storage so a fetch returning after shutdown
	// can stop at this point; repository context handling covers a later
	// race.
	if err := ctx.Err(); err != nil {
		return err
	}
	writer, ok := s.providerRepo.(OllamaUsageRepository)
	if !ok {
		return ErrOllamaCloudUsageUnavailable
	}
	return writer.UpdateOllamaCloudUsageSnapshot(ctx, provider, snapshot)
}

// EnrichState fills administrator encryption-key availability from this
// service's process configuration. Nil state is ignored, and a nil service
// records false so display construction can be enriched with either dependency
// state.
func (s *OllamaCloudUsageService) EnrichState(state *OllamaCloudUsageState) {
	if state == nil {
		return
	}
	state.EncryptionKeyConfigured = s != nil && s.encryptionKeyConfigured
}

// currentTime samples the service clock when it exists, otherwise time.Now. Nil
// services use the system clock too, allowing observation and retry
// calculations to obtain a timestamp through this helper.
func (s *OllamaCloudUsageService) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

// sameOllamaCloudUsageSession reports equality of nonempty stored
// encrypted-session strings. Nil records, absent Extra, and non-string values
// return false; equality identifies snapshots that can be selected together
// during group resolution.
func sameOllamaCloudUsageSession(left, right *Record) bool {
	if left == nil || right == nil || left.Extra == nil || right.Extra == nil {
		return false
	}
	leftSession, leftOK := left.Extra[OllamaCloudUsageSessionExtraKey].(string)
	rightSession, rightOK := right.Extra[OllamaCloudUsageSessionExtraKey].(string)
	return leftOK && rightOK && leftSession != "" && leftSession == rightSession
}

// applyOllamaCloudUsageManagedExtra replaces three managed keys in the target's
// existing Extra map and leaves unrelated keys present. Nil target is ignored;
// absent Extra is allocated. A nil source or missing source key removes the
// corresponding managed target value.
//
// Source and target need separate top-level Extra maps: deletion precedes
// source lookup, so sharing that map would remove the selected keys. Present
// source values are assigned directly and nested storage remains shared;
// callers coordinate access to those retained values. The session, refresh
// flag, and snapshot are treated as a selected group state;
// this helper does not validate their types or perform persistence.
// ResolveProviders owns the choice of that source before calling it.
func applyOllamaCloudUsageManagedExtra(target, source *Record) {
	if target == nil {
		return
	}
	if target.Extra == nil {
		target.Extra = make(map[string]any)
	}
	for _, key := range []string{
		OllamaCloudUsageSessionExtraKey,
		OllamaCloudUsageAutoRefreshExtraKey,
		OllamaCloudUsageSnapshotExtraKey,
	} {
		delete(target.Extra, key)
		if source != nil && source.Extra != nil {
			if value, ok := source.Extra[key]; ok {
				target.Extra[key] = value
			}
		}
	}
}

// acquireLease requests the configured two-minute instance lease and returns
// its release function with admission. With no lease callback, a no-op release
// admits the cycle; callers release ownership after all cycle work finishes.
func (s *OllamaCloudUsageService) acquireLease(ctx context.Context) (func(), bool) {
	if s.options.Lease == nil {
		return func() {}, true
	}
	return s.options.Lease(ctx, ollamaCloudUsageLeaderLockKey, s.options.InstanceID, ollamaCloudUsageLeaderLockTTL)
}
