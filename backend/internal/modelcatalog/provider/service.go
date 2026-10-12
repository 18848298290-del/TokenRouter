package provider

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	purepricing "github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// RemoteClient fetches the model catalog using an ETag validator.
type RemoteClient interface {
	FetchCatalog(ctx context.Context, url, etag string) ([]byte, string, bool, error)
}

// Service loads, synchronizes, and publishes catalog prices and display attributes together.
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#model_catalog_metadata_lookup
type Service struct {
	billingDefaults  purepricing.OperationPrices
	updateMu         sync.Mutex
	modelCatalog     *modelcatalog.Catalog
	attributeIndex   modelcatalog.Index
	priceModels      []PriceModel
	catalogETag      string
	lastCatalogError string
	catalogBody      []byte
	options          *Options
	remoteClient     RemoteClient
	mu               sync.RWMutex
	pricingData      map[string]*CatalogModelPricing
	lastUpdated      time.Time
	localHash        string
	// Fingerprint of supplement contents from the last successful rebuild.
	// The update loop compares it to decide whether to rebuild local supplement layers.
	customFilesHash string

	// Closed to stop the update loop.
	stopCh    chan struct{}
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once
}

// CatalogModelPricing uses the billing package's price representation.
type (
	CatalogModelPricing = purepricing.CatalogModelPricing
)

// Options contains catalog configuration supplied by app at startup.
type Options struct {
	DataDir              string
	RemoteURL            string
	FallbackFile         string
	CheckIntervalMinutes int
	URLAllowlistEnabled  bool
	AllowInsecureHTTP    bool
	AllowPrivateHosts    bool
	PricingHosts         []string
	// Acquires one complete model-identity candidate generator per query.
	ModelLookupCandidates func() func(string) []string
}

// Snapshot contains independently mutable pricing data and a fixed catalog identity for queries and tests.
type Snapshot struct {
	BillingDefaults            purepricing.OperationPrices
	catalogIdentity            *modelcatalog.Catalog
	Data                       map[string]*CatalogModelPricing
	LastUpdated                time.Time
	LocalHash, CustomFilesHash string
	LastError                  string
}

// NewService constructs the catalog service. The application calls Initialize and Start to load and
// synchronize it.
func NewService(options Options, remoteClient RemoteClient) *Service {
	s := &Service{
		options:      &options,
		remoteClient: remoteClient,
		pricingData:  make(map[string]*CatalogModelPricing),
		stopCh:       make(chan struct{}),
	}
	return s
}

// NewServiceFromSnapshot initializes from parsed data without I/O or background work. It takes ownership of
// snapshot.Data.
func NewServiceFromSnapshot(options Options, remote RemoteClient, snapshot Snapshot) *Service {
	s := NewService(options, remote)
	s.pricingData = snapshot.Data
	s.billingDefaults = snapshot.BillingDefaults.Clone()
	s.modelCatalog = snapshot.catalogIdentity
	s.attributeIndex = modelcatalog.NewIndex(s.modelCatalog)
	s.priceModels = buildPriceModels(s.modelCatalog, s.pricingData)
	s.lastCatalogError = snapshot.LastError
	s.lastUpdated = snapshot.LastUpdated
	s.localHash = snapshot.LocalHash
	s.customFilesHash = snapshot.CustomFilesHash
	return s
}

// Initialize loads the initial model catalog and attempts to create its cache directory.
func (s *Service) Initialize() error {
	// A cache-directory error is logged; an in-memory catalog can still serve queries.
	if err := os.MkdirAll(s.currentOptions().DataDir, 0o755); err != nil {
		logging.LegacyPrintf("service.modelcatalog", "[ModelCatalog] Failed to create data directory: %v", err)
	}

	// Load the catalog before starting background synchronization.
	if err := s.loadInitialCatalog(); err != nil {
		return fmt.Errorf("failed to load model catalog: %w", err)
	}

	logging.LegacyPrintf("service.modelcatalog", "[ModelCatalog] Service initialized with %d models", len(s.pricingData))
	return nil
}

// Stop signals the update loop and waits for its in-flight work to finish.
func (s *Service) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.Wait()
	logging.LegacyPrintf("service.modelcatalog", "%s", "[ModelCatalog] Service stopped")
}

// startUpdateScheduler starts remote conditional synchronization followed by local supplement checks each cycle.
// It starts no loop when neither a remote URL nor a supplement path is configured.
func (s *Service) startUpdateScheduler() {
	if s == nil || s.options == nil {
		return
	}
	remoteEnabled := strings.TrimSpace(s.currentOptions().RemoteURL) != ""
	watchCustom := s.hasCustomPricingFiles()
	if !remoteEnabled {
		logging.LegacyPrintf("service.modelcatalog", "%s", "[ModelCatalog] Remote sync disabled: pricing remote URL is empty")
	}
	if !remoteEnabled && !watchCustom {
		return
	}

	checkInterval := time.Duration(s.currentOptions().CheckIntervalMinutes) * time.Minute
	if checkInterval < time.Minute {
		checkInterval = 10 * time.Minute
	}

	s.wg.Go(func() {
		if remoteEnabled {
			_ = s.syncWithRemote()
		}
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if remoteEnabled {
					if err := s.syncWithRemote(); err != nil {
						logging.LegacyPrintf("service.modelcatalog", "[ModelCatalog] Sync failed: %v", err)
					}
				}
				if watchCustom {
					s.reloadIfCustomFilesChanged()
				}
			case <-s.stopCh:
				return
			}
		}
	})

	logging.LegacyPrintf("service.modelcatalog", "[ModelCatalog] Update scheduler started (check every %v, remote sync=%t, custom file watch=%t)", checkInterval, remoteEnabled, watchCustom)
}

// loadInitialCatalog loads the cached or embedded catalog before background remote synchronization.
func (s *Service) loadInitialCatalog() error {
	return s.loadModelsCatalog()
}

// syncWithRemote conditionally fetches the remote catalog.
func (s *Service) syncWithRemote() error {
	return s.updateModelsCatalog(false)
}

// hasCustomPricingFiles reports whether a supplement path is configured, regardless of file existence.
func (s *Service) hasCustomPricingFiles() bool {
	if s == nil || s.options == nil {
		return false
	}
	return strings.TrimSpace(s.currentOptions().FallbackFile) != ""
}

// customPricingFilesFingerprint hashes the length-prefixed supplement body with SHA-256.
// Unreadable files contribute an empty body. An unconfigured path returns an empty string.
func (s *Service) customPricingFilesFingerprint() string {
	if !s.hasCustomPricingFiles() {
		return ""
	}
	h := sha256.New()
	body, _ := os.ReadFile(strings.TrimSpace(s.currentOptions().FallbackFile))
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(body)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// loadLocalPricingEntries reads a local pricing layer. An unconfigured or deleted file is an empty layer.
func loadLocalPricingEntries(path string) (map[string]json.RawMessage, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeModelSupplement(body, path)
}

// decodeModelSupplement applies the same validation to embedded and external supplements.
func decodeModelSupplement(body []byte, source string) (map[string]json.RawMessage, error) {
	entries, err := purepricing.DecodeCatalogEntries(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	// All supplied pricing fields require validation, even when catalog values take precedence.
	_, diagnostics, _ := purepricing.ParsePricingEntries(entries)
	if err := diagnostics.ValidationError(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return entries, nil
}

// reloadIfCustomFilesChanged rebuilds local layers when their fingerprint changes.
// Deleting the supplement clears that layer. Invalid or unreadable files retain the published data
// and fingerprint so later cycles retry and report the failure.
// The catalog body and remote synchronization hash remain independent of supplement contents.
func (s *Service) reloadIfCustomFilesChanged() {
	fingerprint := s.customPricingFilesFingerprint()
	s.mu.RLock()
	unchanged := fingerprint == s.customFilesHash
	s.mu.RUnlock()
	if unchanged {
		return
	}
	if err := s.reloadCustomPricingLayers(); err != nil {
		logging.LegacyPrintf("service.modelcatalog", "[ModelCatalog] Custom pricing file changed but reload failed: %v", err)
	}
}

// reloadCustomPricingLayers rebuilds supplements using the cached or in-memory catalog.
func (s *Service) reloadCustomPricingLayers() error {
	return s.loadModelsCatalog()
}

// warnLopsidedLongContextLadders reports tiers with a surcharge on just one side.
func warnLopsidedLongContextLadders(entries []string) {
	if len(entries) == 0 {
		return
	}
	sort.Strings(entries)
	total := len(entries)
	if total > 20 {
		entries = append(entries[:20], "...")
	}
	logging.LegacyPrintf("service.modelcatalog", "[ModelCatalog] Warning: %d model(s) derive a one-sided long-context ladder (surcharge on only input or only output); base prices and above-tier prices likely come from different price versions: %s", total, strings.Join(entries, ", "))
}

// warnOrphanCacheTierFields reports above-tier cache prices lacking the base price required for billing.
func warnOrphanCacheTierFields(entries []string) {
	if len(entries) == 0 {
		return
	}
	sort.Strings(entries)
	total := len(entries)
	if total > 20 {
		entries = append(entries[:20], "...")
	}
	logging.LegacyPrintf("service.modelcatalog", "[ModelCatalog] Warning: %d model(s) carry cache above-tier prices without a base cache price; that cache item bills at $0 until the catalog/supplement supplies the base: %s", total, strings.Join(entries, ", "))
}

// warnDroppedLongContextLadders reports models whose context tiers disappear during reload.
// A catalog rollback can remove surcharges, so administrators need to verify these changes.
func warnDroppedLongContextLadders(old, next map[string]*CatalogModelPricing) {
	if len(old) == 0 {
		return
	}
	var dropped []string
	for name, previous := range old {
		if previous == nil || previous.LongContextInputTokenThreshold <= 0 && len(previous.ContextPrices) == 0 {
			continue
		}
		if current, ok := next[name]; ok && (current == nil || current.LongContextInputTokenThreshold <= 0 && len(current.ContextPrices) == 0) {
			dropped = append(dropped, name)
		}
	}
	if len(dropped) == 0 {
		return
	}
	sort.Strings(dropped)
	total := len(dropped)
	if total > 20 {
		dropped = append(dropped[:20], "...")
	}
	logging.LegacyPrintf("service.modelcatalog", "[ModelCatalog] Warning: Long-context ladder dropped for %d model(s) after reload: %s (verify catalog/supplement data if unintended)", total, strings.Join(dropped, ", "))
}

func (s *Service) validateCatalogURL(raw string) (string, error) {
	if (s.options != nil) && !s.currentOptions().URLAllowlistEnabled {
		normalized, err := egress.ValidateURLFormat(raw, s.currentOptions().AllowInsecureHTTP)
		if err != nil {
			return "", fmt.Errorf("invalid pricing url: %w", err)
		}
		return normalized, nil
	}
	normalized, err := egress.ValidateHTTPSURL(raw, egress.ValidationOptions{
		AllowedHosts:     s.currentOptions().PricingHosts,
		RequireAllowlist: true,
		AllowPrivate:     s.currentOptions().AllowPrivateHosts,
	})
	if err != nil {
		return "", fmt.Errorf("invalid pricing url: %w", err)
	}
	return normalized, nil
}

// GetModelPricing reads prices under the catalog lock. Callers must treat the returned price as read-only.
func (s *Service) GetModelPricing(modelName string) *CatalogModelPricing {
	s.mu.RLock()
	defer s.mu.RUnlock()
	query := s.catalogQuery()
	if s.modelCatalog != nil {
		candidates := []string{modelName}
		if query.Candidates != nil {
			candidates = append(candidates, query.Candidates(modelName)...)
		}
		for _, name := range s.modelCatalog.IdentityCandidates(modelName, candidates) {
			if value := s.pricingData[strings.ToLower(strings.TrimSpace(name))]; value != nil {
				return value
			}
		}
		if s.modelCatalog.RequiresExact(modelName) {
			return &CatalogModelPricing{Source: "unpriced", TokenPricingAbsent: true}
		}
	}
	return query.GetModelPricing(modelName)
}

// GetModelModalities returns input and output modalities from the pricing catalog under its read lock.
func (s *Service) GetModelModalities(modelName string) ([]string, []string) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	query := s.catalogQuery()
	return query.GetModelModalities(modelName)
}

// GetStatus returns the catalog version, model count, and last update outcome.
func (s *Service) GetStatus() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	version := s.localHash
	if s.modelCatalog != nil {
		version = s.modelCatalog.Version
	}

	return map[string]any{
		"model_count":  len(s.pricingData),
		"version":      version,
		"last_error":   s.lastCatalogError,
		"last_updated": s.lastUpdated,
		"local_hash":   s.localHash[:min(8, len(s.localHash))],
	}
}

// ForceUpdate downloads the catalog unconditionally, or reloads local supplements when no remote URL is
// configured.
func (s *Service) ForceUpdate() error {
	return s.updateModelsCatalog(true)
}

// catalogFilePath returns the models.dev cache path.
func (s *Service) catalogFilePath() string {
	return filepath.Join(s.currentOptions().DataDir, "models_dev_catalog.json")
}

// ListModelNamesByProvider returns sorted model names from the pricing catalog.
// Provider matching is case-insensitive.
func (s *Service) ListModelNamesByProvider(provider string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	provider = strings.ToLower(strings.TrimSpace(provider))
	names := make([]string, 0)
	for name, p := range s.pricingData {
		if strings.EqualFold(p.Provider, provider) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Start begins synchronization once. Call it after initialization and dependency binding.
func (s *Service) Start() {
	s.startOnce.Do(s.startUpdateScheduler)
}

func (s *Service) currentOptions() Options {
	if s.options == nil {
		return Options{}
	}
	return *s.options
}

// Snapshot copies pricing maps, entries, and slices so callers can modify them independently.
func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Snapshot{BillingDefaults: s.billingDefaults.Clone(), catalogIdentity: s.modelCatalog, LastError: s.lastCatalogError, LastUpdated: s.lastUpdated, LocalHash: s.localHash, CustomFilesHash: s.customFilesHash}
	if s.pricingData != nil {
		out.Data = make(map[string]*CatalogModelPricing, len(s.pricingData))
	}
	for key, value := range s.pricingData {
		if value == nil {
			out.Data[key] = nil
			continue
		}
		out.Data[key] = purepricing.CloneCatalogPrice(value)
	}
	return out
}

// Wait blocks until started update work exits. Stop calls it after signaling the loop.
func (s *Service) Wait() {
	s.wg.Wait()
}

// catalogQuery acquires one model-identity candidate generator for this query.
func (s *Service) catalogQuery() *purepricing.CatalogQuery {
	options := s.currentOptions()
	var candidates func(string) []string
	if options.ModelLookupCandidates != nil {
		candidates = options.ModelLookupCandidates()
	}
	return &purepricing.CatalogQuery{
		Entries:    s.pricingData,
		Candidates: candidates,
	}
}

// ReadOnlySnapshot returns an in-memory catalog fixed for one administrative query.
func (s *Service) ReadOnlySnapshot() *Service {
	return NewServiceFromSnapshot(s.currentOptions(), nil, s.Snapshot())
}
