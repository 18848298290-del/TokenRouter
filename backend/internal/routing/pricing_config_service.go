package routing

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

const (
	pricingConfigCacheTTL       = 10 * time.Minute
	pricingConfigErrorTTL       = 5 * time.Second // Short cache lifetime after a database error.
	pricingConfigCacheDBTimeout = 10 * time.Second
)

var (
	ErrPricingConfigNotFound       = infraerrors.NotFound("PRICING_CONFIG_NOT_FOUND", "price configuration not found")
	ErrPricingConfigExists         = infraerrors.Conflict("PRICING_CONFIG_EXISTS", "price configuration name already exists")
	ErrGroupAlreadyInPricingConfig = infraerrors.Conflict(
		"GROUP_ALREADY_IN_PRICING_CONFIG",
		"one or more groups already belong to another price configuration",
	)
)

// GroupAuthInvalidator invalidates authentication snapshots after group changes commit.
type (
	GroupAuthInvalidator interface{ InvalidateAuthCacheByGroupID(context.Context, int64) }
	PricingConfigOptions struct {
		ReadGroup func(context.Context, int64) (*Group, error)
		// Warn receives read-failure diagnostics through the caller's logger.
		Warn         func(string, ...any)
		Now          func() time.Time
		LoadLocation func(string) (*time.Location, error)
	}
)

// PricingConfigValidation validates price entries, conflicts, and time rules.
type PricingConfigValidation struct {
	LoadLocation func(string) (*time.Location, error)
}

// PricingConfigRepository persists pricing configurations and their associations.
type PricingConfigRepository interface {
	Create(ctx context.Context, pricingConfig *PricingConfig) error
	GetByID(ctx context.Context, id int64) (*PricingConfig, error)
	Update(ctx context.Context, pricingConfig *PricingConfig) error
	Delete(ctx context.Context, id int64) error
	List(ctx context.Context, params pagination.PaginationParams, status, search string) ([]PricingConfig, *pagination.PaginationResult, error)
	ListAll(ctx context.Context) ([]PricingConfig, error)
	ExistsByName(ctx context.Context, name string) (bool, error)
	ExistsByNameExcluding(ctx context.Context, name string, excludeID int64) (bool, error)

	// Group associations.
	GetGroupIDs(ctx context.Context, pricingConfigID int64) ([]int64, error)
	SetGroupIDs(ctx context.Context, pricingConfigID int64, groupIDs []int64) error
	GetPricingConfigIDByGroupID(ctx context.Context, groupID int64) (int64, error)
	GetGroupsInOtherPricingConfigs(ctx context.Context, pricingConfigID int64, groupIDs []int64) ([]int64, error)

	// Model prices.
	ListModelPricing(ctx context.Context, pricingConfigID int64) ([]ModelPricingEntry, error)
	CreateModelPricing(ctx context.Context, pricing *ModelPricingEntry) error
	UpdateModelPricing(ctx context.Context, pricing *ModelPricingEntry) error
	DeleteModelPricing(ctx context.Context, id int64) error
	ReplaceModelPricing(ctx context.Context, pricingConfigID int64, pricingList []ModelPricingEntry) error
}

// pricingModelKey identifies a group's price for a normalized model name.
type pricingModelKey struct {
	groupID int64
	model   string // lowercase
}

// wildcardPricingEntry pairs a normalized prefix with a price entry.
type wildcardPricingEntry struct {
	prefix  string
	pricing *ModelPricingEntry
}

// pricingConfigCache indexes exact prices and group configurations, with ordered wildcard lists.
type pricingConfigCache struct {
	pricingByGroupModel map[pricingModelKey]*ModelPricingEntry // Exact prices by group and normalized model.
	wildcardByGroup     map[int64][]*wildcardPricingEntry      // First matching wildcard in configuration order.

	pricingConfigByGroupID map[int64]*PricingConfig // Associated configuration by group.

	// Configuration lookup for management operations.
	byID     map[int64]*PricingConfig
	loadedAt time.Time
}

// GroupMappingResult carries group mapping, whitelist stage, and billing metadata.
type GroupMappingResult struct {
	RestrictionModelSource string
	RestrictModels         bool
	MappedModel            string // Group-mapped model; unchanged when no mapping applies.
	PricingConfigID        int64  // Zero means no associated pricing configuration.
	Mapped                 bool   // Whether group mapping changed the model.
	BillingModelSource     string // Price source: requested, upstream, or group_mapped.
	// ClientModel is for display and mapping records; pricing starts from the caller's key-redirected reqModel.
	ClientModel      string
	APIKeyRedirected bool
}

// PricingConfigService manages shared prices and caches group/model lookups.
type PricingConfigService struct {
	options              PricingConfigOptions
	validation           PricingConfigValidation
	repo                 PricingConfigRepository
	authCacheInvalidator GroupAuthInvalidator

	cache   atomic.Value // *pricingConfigCache
	cacheSF singleflight.Group
}

// pricingConfigLookup pairs the cache snapshot with a group's active configuration.
type pricingConfigLookup struct {
	cache         *pricingConfigCache
	pricingConfig *PricingConfig
}

// modelEntry is a normalized model pattern used for overlap detection.
type modelEntry struct {
	pattern  string // Original pattern, such as "claude-*" or "claude-opus-4".
	prefix   string // Normalized prefix; a trailing wildcard is removed.
	wildcard bool
}

// CreatePricingConfigInput contains settings for a new pricing configuration.
type CreatePricingConfigInput struct {
	BillingSettingsPatch
	Name         string
	Description  string
	GroupIDs     []int64
	ModelPricing []ModelPricingEntry

	BillingModelSource string

	ProviderStatsPricingRules []ProviderStatsPricingRule
}

// UpdatePricingConfigInput contains changes to an existing pricing configuration.
type UpdatePricingConfigInput struct {
	BillingSettingsPatch
	Name         string
	Description  *string
	Status       string
	GroupIDs     *[]int64
	ModelPricing *[]ModelPricingEntry

	BillingModelSource string

	ProviderStatsPricingRules *[]ProviderStatsPricingRule
}

// normalizePriceModelName uses the shared price-card identity rules.
func normalizePriceModelName(model string) string {
	return pricing.NormalizePriceModelName(model)
}

// BuildModelMappingChain builds the display chain from request and execution model stages.
// reqModel is the name after API key redirection.
// upstreamModel is the executed model from ForwardResult.UpstreamModel.
// It returns an empty string when fewer than two distinct stages remain.
func (r GroupMappingResult) BuildModelMappingChain(reqModel, upstreamModel string) string {
	stages := make([]string, 0, 4)
	if r.APIKeyRedirected {
		stages = append(stages, r.ClientModel)
	}
	stages = append(stages, reqModel)
	if r.Mapped {
		stages = append(stages, r.MappedModel)
	}
	stages = append(stages, upstreamModel)
	return BuildModelMappingChain(stages...)
}

// ToUsageFields converts group mapping and billing metadata into usage fields.
func (r GroupMappingResult) ToUsageFields(reqModel, upstreamModel string) PricingUsageFields {
	groupMappedModel := reqModel
	if r.Mapped {
		groupMappedModel = r.MappedModel
	}
	return PricingUsageFields{
		PricingConfigID:    r.PricingConfigID,
		OriginalModel:      reqModel,
		GroupMappedModel:   groupMappedModel,
		BillingModelSource: r.BillingModelSource,
		ModelMappingChain:  r.BuildModelMappingChain(reqModel, upstreamModel),
	}
}

// NewPricingConfigService creates a pricing configuration service.
func NewPricingConfigService(repo PricingConfigRepository, authCacheInvalidator GroupAuthInvalidator, options ...PricingConfigOptions) *PricingConfigService {
	optionsValue := PricingConfigOptions{Now: time.Now}
	if len(options) > 0 {
		optionsValue = options[0]
		if optionsValue.Now == nil {
			optionsValue.Now = time.Now
		}
	}
	s := &PricingConfigService{
		options: optionsValue, validation: PricingConfigValidation{LoadLocation: optionsValue.LoadLocation},
		repo:                 repo,
		authCacheInvalidator: authCacheInvalidator,
	}
	return s
}

// loadCache reuses an unexpired snapshot and coalesces cache rebuilds.
func (s *PricingConfigService) loadCache(ctx context.Context) (*pricingConfigCache, error) {
	if cached, ok := s.cache.Load().(*pricingConfigCache); ok && cached != nil {
		if s.options.Now().Sub(cached.loadedAt) < pricingConfigCacheTTL {
			return cached, nil
		}
	}

	result, err, _ := s.cacheSF.Do("pricing_config_cache", func() (any, error) {
		// Another caller may have published a snapshot before this singleflight call began.
		if cached, ok := s.cache.Load().(*pricingConfigCache); ok && cached != nil {
			if s.options.Now().Sub(cached.loadedAt) < pricingConfigCacheTTL {
				return cached, nil
			}
		}
		return s.buildCache(ctx)
	})
	if err != nil {
		return nil, err
	}
	cache, ok := result.(*pricingConfigCache)
	if !ok {
		return nil, fmt.Errorf("unexpected cache type")
	}
	return cache, nil
}

// newEmptyPricingConfigCache initializes every lookup map.
func newEmptyPricingConfigCache() *pricingConfigCache {
	return &pricingConfigCache{
		pricingByGroupModel: make(map[pricingModelKey]*ModelPricingEntry),
		wildcardByGroup:     make(map[int64][]*wildcardPricingEntry),

		pricingConfigByGroupID: make(map[int64]*PricingConfig),
		byID:                   make(map[int64]*PricingConfig),
	}
}

// expandPricingToCache indexes exact names and ordered wildcard prefixes for a group.
func expandPricingToCache(cache *pricingConfigCache, config *PricingConfig, groupID int64) {
	for i := range config.ModelPricing {
		entry := &config.ModelPricing[i]
		for _, model := range entry.Models {
			if before, ok := strings.CutSuffix(model, "*"); ok {
				cache.wildcardByGroup[groupID] = append(cache.wildcardByGroup[groupID], &wildcardPricingEntry{prefix: normalizePriceModelName(before), pricing: entry})
			} else {
				cache.pricingByGroupModel[pricingModelKey{groupID: groupID, model: normalizePriceModelName(model)}] = entry
			}
		}
	}
}

// storeErrorCache publishes an empty snapshot briefly to limit retries after database failures.
// Backdating loadedAt leaves pricingConfigErrorTTL before expiry.
func (s *PricingConfigService) storeErrorCache() {
	errorCache := newEmptyPricingConfigCache()
	errorCache.loadedAt = s.options.Now().Add(-(pricingConfigCacheTTL - pricingConfigErrorTTL))
	s.cache.Store(errorCache)
}

// buildCache loads and publishes a pricing snapshot.
// Detach cancellation from the request and bound the shared database read with its own timeout.
func (s *PricingConfigService) buildCache(ctx context.Context) (*pricingConfigCache, error) {
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pricingConfigCacheDBTimeout)
	defer cancel()

	pricingConfigs, err := s.fetchPricingConfigData(dbCtx)
	if err != nil {
		return nil, err
	}

	cache := populatePricingConfigCache(pricingConfigs)
	cache.loadedAt = s.options.Now()
	s.cache.Store(cache)
	return cache, nil
}

// fetchPricingConfigData loads configurations with group associations through ListAll.
func (s *PricingConfigService) fetchPricingConfigData(ctx context.Context) ([]PricingConfig, error) {
	configs, err := s.repo.ListAll(ctx)
	if err != nil {
		s.warn("failed to build pricing configuration cache", "error", err)
		s.storeErrorCache()
		return nil, fmt.Errorf("list all price configurations: %w", err)
	}
	return configs, nil
}

// populatePricingConfigCache clones configurations before indexing them.
func populatePricingConfigCache(configs []PricingConfig) *pricingConfigCache {
	cache := newEmptyPricingConfigCache()
	cache.loadedAt = time.Now()
	for i := range configs {
		config := configs[i].Clone()
		cache.byID[config.ID] = config
		for _, id := range config.GroupIDs {
			cache.pricingConfigByGroupID[id] = config
			expandPricingToCache(cache, config, id)
		}
	}
	return cache
}

// InvalidateCache discards the snapshot and immediately attempts a rebuild.
// Call it after changes outside this service that affect prices or group associations.
func (s *PricingConfigService) InvalidateCache() {
	s.invalidateCache()
}

// invalidateCache clears the snapshot and logs any immediate rebuild failure.
func (s *PricingConfigService) invalidateCache() {
	s.cache.Store((*pricingConfigCache)(nil))
	s.cacheSF.Forget("pricing_config_cache")

	if _, err := s.buildCache(context.Background()); err != nil {
		s.warn("failed to rebuild pricing configuration cache after invalidation", "error", err)
	}
}

// lookupPricing prefers exact names, then configuration-ordered wildcards; effective lookups skip entries with no
// price.
func lookupPricing(cache *pricingConfigCache, groupID int64, model string, effective bool) *ModelPricingEntry {
	model = normalizePriceModelName(model)
	matches := func(entry *ModelPricingEntry) bool {
		return entry != nil && (!effective || entry.HasEffectivePricing())
	}
	if entry := cache.pricingByGroupModel[pricingModelKey{groupID: groupID, model: model}]; matches(entry) {
		return entry
	}
	for _, wildcard := range cache.wildcardByGroup[groupID] {
		if strings.HasPrefix(model, wildcard.prefix) && matches(wildcard.pricing) {
			return wildcard.pricing
		}
	}
	return nil
}

// GetPricingConfigForGroup returns a clone of the group's active configuration, or nil when absent.
func (s *PricingConfigService) GetPricingConfigForGroup(ctx context.Context, groupID int64) (*PricingConfig, error) {
	cache, err := s.loadCache(ctx)
	if err != nil {
		return nil, err
	}

	ch, ok := cache.pricingConfigByGroupID[groupID]
	if !ok || !ch.IsActive() {
		return nil, nil
	}

	return ch.Clone(), nil
}

// lookupGroupPricingConfig loads the snapshot and finds the group's active configuration.
// A nil result with no error means no active configuration; an error means loading failed.
func (s *PricingConfigService) lookupGroupPricingConfig(ctx context.Context, groupID int64) (*pricingConfigLookup, error) {
	cache, err := s.loadCache(ctx)
	if err != nil {
		return nil, err
	}
	ch, ok := cache.pricingConfigByGroupID[groupID]
	if !ok || !ch.IsActive() {
		return nil, nil
	}
	return &pricingConfigLookup{
		cache:         cache,
		pricingConfig: ch,
	}, nil
}

// GetConfigModelPricing returns a cloned group/model price, or nil on a miss or load failure.
// Groups associated with one configuration share its model prices.
func (s *PricingConfigService) GetConfigModelPricing(ctx context.Context, groupID int64, model string) *ModelPricingEntry {
	lk, err := s.lookupGroupPricingConfig(ctx, groupID)
	if err != nil {
		s.warn("failed to load pricing configuration cache", "group_id", groupID, "error", err)
		return nil
	}
	if lk == nil {
		return nil
	}

	modelLower := strings.ToLower(model)
	pricing := lookupPricing(lk.cache, groupID, modelLower, false)
	if pricing == nil {
		return nil
	}

	cp := pricing.Clone()
	return &cp
}

func (s *PricingConfigService) GetEffectiveConfigModelPricing(ctx context.Context, groupID int64, model string) *ModelPricingEntry {
	lk, err := s.lookupGroupPricingConfig(ctx, groupID)
	if err != nil {
		s.warn("failed to load pricing configuration cache", "group_id", groupID, "error", err)
		return nil
	}
	if lk == nil {
		return nil
	}

	modelLower := strings.ToLower(model)
	pricing := lookupPricing(lk.cache, groupID, modelLower, true)
	if pricing == nil {
		return nil
	}

	cp := pricing.Clone()
	return &cp
}

// ResolveGroupMapping reads group policy and attaches billing metadata from the pricing configuration.
func (s *PricingConfigService) ResolveGroupMapping(ctx context.Context, groupID int64, model string) GroupMappingResult {
	result := GroupMappingResult{MappedModel: model, RestrictionModelSource: BillingModelSourceGroupMapped}
	policy, err := s.GetGroupPolicy(ctx, groupID)
	if err != nil {
		s.warn("failed to read group routing policy", "group_id", groupID, "error", err)
		result.RestrictModels = true
	}
	if policy != nil {
		result.MappedModel = policy.ResolveModel(model)
		result.Mapped = result.MappedModel != model
		result.RestrictionModelSource = policy.RestrictionSource()
		result.RestrictModels = policy.RestrictModels
	}
	config, err := s.GetPricingConfigForGroup(ctx, groupID)
	if err == nil && config != nil {
		result.PricingConfigID = config.ID
		result.BillingModelSource = config.BillingModelSource
		if result.BillingModelSource == "" {
			result.BillingModelSource = BillingModelSourceGroupMapped
		}
	}
	return result
}

// IsModelRestricted checks the group whitelist and rejects on a policy-read failure.
func (s *PricingConfigService) IsModelRestricted(ctx context.Context, groupID int64, model string) bool {
	policy, err := s.GetGroupPolicy(ctx, groupID)
	if err != nil {
		return true
	}
	return policy.IsModelRestricted(model)
}

// PricingEntries validates pattern conflicts, intervals, billing modes, and time rules.
// It is shared by user prices and provider-cost prices.
func (v PricingConfigValidation) PricingEntries(pricing []ModelPricingEntry) error {
	if err := validateNoConflictingModels(pricing); err != nil {
		return err
	}
	if err := validatePricingIntervals(pricing); err != nil {
		return err
	}
	if err := validatePricingBillingMode(pricing); err != nil {
		return err
	}
	return v.PricingTime(pricing)
}

// PricingTime validates daily multipliers for token prices and clears configurations with no periods.
func (v PricingConfigValidation) PricingTime(pricing []ModelPricingEntry) error {
	for i := range pricing {
		config := pricing[i].TimePricing
		if config == nil {
			continue
		}
		if len(config.Periods) == 0 {
			pricing[i].TimePricing = nil
			continue
		}
		mode := pricing[i].BillingMode
		if mode != "" && mode != BillingModeToken {
			return infraerrors.BadRequest("TIME_PRICING_UNSUPPORTED_MODE", "time pricing only supports token billing mode")
		}
		if err := v.TimePricing(config); err != nil {
			return infraerrors.BadRequest("INVALID_TIME_PRICING", fmt.Sprintf(
				"invalid time pricing for models %v: %v", pricing[i].Models, err))
		}
	}
	return nil
}

// validatePricingBillingMode checks required prices, nonnegative values, and populated intervals.
func validatePricingBillingMode(pricing []ModelPricingEntry) error {
	for _, p := range pricing {
		if err := CheckBillingModeRequirements(p); err != nil {
			return err
		}
		if err := checkPricesNotNegative(p); err != nil {
			return err
		}
		if err := checkIntervalsHavePrices(p); err != nil {
			return err
		}
	}
	return nil
}

func CheckBillingModeRequirements(p ModelPricingEntry) error {
	if p.BillingMode == BillingModePerRequest || p.BillingMode == BillingModeImage || p.BillingMode == BillingModeVideo {
		if p.PerRequestPrice == nil && len(p.Intervals) == 0 {
			return infraerrors.BadRequest(
				"BILLING_MODE_MISSING_PRICE",
				"per-request price or intervals required for per_request/image billing mode",
			)
		}
	}
	if p.PriceMultiplier != nil && !hasExplicitPricingPrice(p) {
		return infraerrors.BadRequest(
			"PRICE_MULTIPLIER_MISSING_PRICE",
			"price_multiplier requires at least one explicit price",
		)
	}
	if p.FastModeMultiplier != nil {
		mode := p.BillingMode
		if mode == "" {
			mode = BillingModeToken
		}
		if mode != BillingModeToken {
			return infraerrors.BadRequest(
				"FAST_MODE_MULTIPLIER_UNSUPPORTED_BILLING_MODE",
				"fast_mode_multiplier is only supported for token billing mode",
			)
		}
		if !hasExplicitPricingPrice(p) {
			return infraerrors.BadRequest(
				"FAST_MODE_MULTIPLIER_MISSING_PRICE",
				"fast_mode_multiplier requires at least one explicit price",
			)
		}
	}
	// Reject non-finite multipliers before they can propagate into calculated costs.
	if p.MaxReasoningEffortMultiplier != nil && (math.IsNaN(*p.MaxReasoningEffortMultiplier) || math.IsInf(*p.MaxReasoningEffortMultiplier, 0)) {
		return infraerrors.BadRequest("INVALID_MULTIPLIER", "max_reasoning_effort_multiplier must be finite and > 0")
	}
	for _, c := range []struct {
		field string
		val   *float64
	}{
		{"fast_multiplier", p.FastMultiplier},
		{"flex_multiplier", p.FlexMultiplier},
		{"max_reasoning_effort_multiplier", p.MaxReasoningEffortMultiplier},
	} {
		if c.val != nil && (math.IsNaN(*c.val) || math.IsInf(*c.val, 0) || *c.val <= 0) {
			return infraerrors.BadRequest("INVALID_MULTIPLIER", fmt.Sprintf("%s must be finite and > 0", c.field))
		}
	}
	if p.FastMultiplier != nil || p.FlexMultiplier != nil || p.MaxReasoningEffortMultiplier != nil {
		mode := p.BillingMode
		if mode == "" {
			mode = BillingModeToken
		}
		if mode != BillingModeToken {
			return infraerrors.BadRequest(
				"TIER_MULTIPLIER_UNSUPPORTED_BILLING_MODE",
				"fast_multiplier, flex_multiplier and max_reasoning_effort_multiplier are only supported for token billing mode",
			)
		}
	}
	return nil
}

// hasExplicitPricingPrice uses the shared price-card presence check.
func hasExplicitPricingPrice(p ModelPricingEntry) bool { return pricing.HasExplicitPricingPrice(p) }

func checkPricesNotNegative(p ModelPricingEntry) error {
	checks := []struct {
		field string
		val   *float64
	}{
		{"price_multiplier", p.PriceMultiplier},
		{"fast_mode_multiplier", p.FastModeMultiplier},
		{"input_price", p.InputPrice},
		{"output_price", p.OutputPrice},
		{"cache_write_price", p.CacheWritePrice},
		{"cache_write_1h_price", p.CacheWrite1hPrice},
		{"cache_read_price", p.CacheReadPrice},
		{"image_input_price", p.ImageInputPrice},
		{"image_output_price", p.ImageOutputPrice},
		{"per_request_price", p.PerRequestPrice},
	}
	for _, c := range checks {
		if c.val != nil && (math.IsNaN(*c.val) || math.IsInf(*c.val, 0) || *c.val < 0) {
			return infraerrors.BadRequest("NEGATIVE_PRICE", fmt.Sprintf("%s must be >= 0", c.field))
		}
	}
	return nil
}

// ProviderStatsPricing rejects tier, reasoning, and time modifiers reserved for user billing, then validates prices.
func (v PricingConfigValidation) ProviderStatsPricing(pricing []ModelPricingEntry) error {
	for _, p := range pricing {
		if p.FastModeMultiplier != nil {
			return infraerrors.BadRequest(
				"PROVIDER_STATS_FAST_MODE_MULTIPLIER_UNSUPPORTED",
				"fast_mode_multiplier is not supported for provider stats pricing",
			)
		}
		if p.FastMultiplier != nil || p.FlexMultiplier != nil {
			return infraerrors.BadRequest(
				"PROVIDER_STATS_TIER_MULTIPLIER_UNSUPPORTED",
				"service tier multipliers are not supported for provider stats pricing",
			)
		}
		if p.MaxReasoningEffortMultiplier != nil {
			return infraerrors.BadRequest(
				"PROVIDER_STATS_REASONING_MULTIPLIER_UNSUPPORTED",
				"max_reasoning_effort_multiplier is not supported for provider stats pricing",
			)
		}
		if p.TimePricing != nil && len(p.TimePricing.Periods) > 0 {
			return infraerrors.BadRequest(
				"PROVIDER_STATS_TIME_PRICING_UNSUPPORTED",
				"provider stats pricing does not support time pricing",
			)
		}
	}
	return v.PricingEntries(pricing)
}

func checkIntervalsHavePrices(p ModelPricingEntry) error {
	for _, iv := range p.Intervals {
		if iv.InputPrice == nil && iv.OutputPrice == nil &&
			iv.CacheWritePrice == nil && iv.CacheWrite1hPrice == nil && iv.CacheReadPrice == nil &&
			iv.PerRequestPrice == nil && iv.InputMultiplier == nil &&
			iv.OutputMultiplier == nil && iv.CacheWriteMultiplier == nil &&
			iv.CacheReadMultiplier == nil {
			return infraerrors.BadRequest(
				"INTERVAL_MISSING_PRICE",
				fmt.Sprintf("interval [%d, %s] has no price fields set for model %v",
					iv.MinTokens, formatMaxTokens(iv.MaxTokens), p.Models),
			)
		}
	}
	return nil
}

func formatMaxTokens(max *int) string {
	if max == nil {
		return "∞"
	}
	return fmt.Sprintf("%d", *max)
}

// Create validates and saves a pricing configuration, then refreshes price and authentication caches.
func (s *PricingConfigService) Create(ctx context.Context, input *CreatePricingConfigInput) (*PricingConfig, error) {
	exists, err := s.repo.ExistsByName(ctx, input.Name)
	if err != nil {
		return nil, fmt.Errorf("check price configuration exists: %w", err)
	}
	if exists {
		return nil, ErrPricingConfigExists
	}

	if err := s.checkGroupConflicts(ctx, 0, input.GroupIDs); err != nil {
		return nil, err
	}

	pricingConfig := &PricingConfig{
		BillingSettings:    pricing.DefaultBillingSettings(),
		Name:               input.Name,
		Description:        input.Description,
		Status:             StatusActive,
		BillingModelSource: input.BillingModelSource,

		GroupIDs:     input.GroupIDs,
		ModelPricing: input.ModelPricing,

		ProviderStatsPricingRules: input.ProviderStatsPricingRules,
	}
	if err := input.Apply(&pricingConfig.BillingSettings); err != nil {
		return nil, err
	}
	if pricingConfig.BillingModelSource == "" {
		pricingConfig.BillingModelSource = BillingModelSourceGroupMapped
	}

	if err := s.validation.PricingEntries(pricingConfig.ModelPricing); err != nil {
		return nil, err
	}
	for i, rule := range pricingConfig.ProviderStatsPricingRules {
		if err := s.validation.ProviderStatsPricing(rule.Pricing); err != nil {
			return nil, fmt.Errorf("provider stats pricing rule #%d: %w", i+1, err)
		}
	}

	if err := s.repo.Create(ctx, pricingConfig); err != nil {
		return nil, fmt.Errorf("create price configuration: %w", err)
	}

	s.invalidateCache()
	s.invalidateAuthCacheForGroups(ctx, pricingConfig.GroupIDs)
	return s.repo.GetByID(ctx, pricingConfig.ID)
}

// GetByID reads a pricing configuration by ID.
func (s *PricingConfigService) GetByID(ctx context.Context, id int64) (*PricingConfig, error) {
	return s.repo.GetByID(ctx, id)
}

// Update validates and saves changes, then invalidates prices and both old and new group auth snapshots.
func (s *PricingConfigService) Update(ctx context.Context, id int64, input *UpdatePricingConfigInput) (*PricingConfig, error) {
	pricingConfig, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get price configuration: %w", err)
	}

	if err := s.applyUpdateInput(ctx, pricingConfig, input); err != nil {
		return nil, err
	}

	if err := s.validation.PricingEntries(pricingConfig.ModelPricing); err != nil {
		return nil, err
	}
	for i, rule := range pricingConfig.ProviderStatsPricingRules {
		if err := s.validation.ProviderStatsPricing(rule.Pricing); err != nil {
			return nil, fmt.Errorf("provider stats pricing rule #%d: %w", i+1, err)
		}
	}

	oldGroupIDs := s.getOldGroupIDs(ctx, id)

	if err := s.repo.Update(ctx, pricingConfig); err != nil {
		return nil, fmt.Errorf("update price configuration: %w", err)
	}

	s.invalidateCache()
	s.invalidateAuthCacheForGroups(ctx, oldGroupIDs, pricingConfig.GroupIDs)

	return s.repo.GetByID(ctx, id)
}

// applyUpdateInput applies supplied fields and checks name and group conflicts.
func (s *PricingConfigService) applyUpdateInput(ctx context.Context, pricingConfig *PricingConfig, input *UpdatePricingConfigInput) error {
	if err := input.Apply(&pricingConfig.BillingSettings); err != nil {
		return err
	}
	if input.Name != "" && input.Name != pricingConfig.Name {
		exists, err := s.repo.ExistsByNameExcluding(ctx, input.Name, pricingConfig.ID)
		if err != nil {
			return fmt.Errorf("check price configuration exists: %w", err)
		}
		if exists {
			return ErrPricingConfigExists
		}
		pricingConfig.Name = input.Name
	}
	if input.Description != nil {
		pricingConfig.Description = *input.Description
	}
	if input.Status != "" {
		pricingConfig.Status = input.Status
	}

	if input.GroupIDs != nil {
		if err := s.checkGroupConflicts(ctx, pricingConfig.ID, *input.GroupIDs); err != nil {
			return err
		}
		pricingConfig.GroupIDs = *input.GroupIDs
	}
	if input.ModelPricing != nil {
		pricingConfig.ModelPricing = *input.ModelPricing
	}

	if input.BillingModelSource != "" {
		pricingConfig.BillingModelSource = input.BillingModelSource
	}

	if input.ProviderStatsPricingRules != nil {
		pricingConfig.ProviderStatsPricingRules = *input.ProviderStatsPricingRules
	}
	return nil
}

// checkGroupConflicts rejects groups already associated with another configuration.
// pricingConfigID identifies the current configuration; pass zero when creating one.
func (s *PricingConfigService) checkGroupConflicts(ctx context.Context, pricingConfigID int64, groupIDs []int64) error {
	if len(groupIDs) == 0 {
		return nil
	}
	conflicting, err := s.repo.GetGroupsInOtherPricingConfigs(ctx, pricingConfigID, groupIDs)
	if err != nil {
		return fmt.Errorf("check group conflicts: %w", err)
	}
	if len(conflicting) > 0 {
		return ErrGroupAlreadyInPricingConfig
	}
	return nil
}

// getOldGroupIDs reads previous associations for authentication-cache invalidation when an invalidator is installed.
func (s *PricingConfigService) getOldGroupIDs(ctx context.Context, pricingConfigID int64) []int64 {
	if s.authCacheInvalidator == nil {
		return nil
	}
	oldGroupIDs, err := s.repo.GetGroupIDs(ctx, pricingConfigID)
	if err != nil {
		s.warn("failed to get old group IDs for cache invalidation", "pricing_config_id", pricingConfigID, "error", err)
	}
	return oldGroupIDs
}

// invalidateAuthCacheForGroups invalidates each distinct group across the supplied sets.
func (s *PricingConfigService) invalidateAuthCacheForGroups(ctx context.Context, groupIDSets ...[]int64) {
	if s.authCacheInvalidator == nil {
		return
	}
	seen := make(map[int64]struct{})
	for _, ids := range groupIDSets {
		for _, gid := range ids {
			if _, ok := seen[gid]; ok {
				continue
			}
			seen[gid] = struct{}{}
			s.authCacheInvalidator.InvalidateAuthCacheByGroupID(ctx, gid)
		}
	}
}

// Delete removes a configuration and refreshes price and group authentication caches.
func (s *PricingConfigService) Delete(ctx context.Context, id int64) error {
	groupIDs, err := s.repo.GetGroupIDs(ctx, id)
	if err != nil {
		s.warn("failed to get group IDs before delete", "pricing_config_id", id, "error", err)
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete price configuration: %w", err)
	}

	s.invalidateCache()
	s.invalidateAuthCacheForGroups(ctx, groupIDs)

	return nil
}

// List returns paginated pricing configurations.
func (s *PricingConfigService) List(ctx context.Context, params pagination.PaginationParams, status, search string) ([]PricingConfig, *pagination.PaginationResult, error) {
	return s.repo.List(ctx, params, status, search)
}

// conflictsBetween reports whether two exact or prefix patterns overlap.
func conflictsBetween(a, b modelEntry) bool {
	switch {
	case !a.wildcard && !b.wildcard:
		return a.prefix == b.prefix
	case a.wildcard && !b.wildcard:
		return strings.HasPrefix(b.prefix, a.prefix)
	case !a.wildcard && b.wildcard:
		return strings.HasPrefix(a.prefix, b.prefix)
	default:
		return strings.HasPrefix(a.prefix, b.prefix) ||
			strings.HasPrefix(b.prefix, a.prefix)
	}
}

// toModelEntry prepares a model-mapping source for conflict detection.
// It lowercases the prefix and retains model punctuation.
func toModelEntry(pattern string) modelEntry {
	lower := strings.ToLower(pattern)
	isWild := strings.HasSuffix(lower, "*")
	prefix := lower
	if isWild {
		prefix = strings.TrimSuffix(lower, "*")
	}
	return modelEntry{pattern: pattern, prefix: prefix, wildcard: isWild}
}

// toPricingModelEntry prepares a price pattern for conflict detection.
//
// Use the same normalization as cache keys, including whitespace trimming,
// so two patterns cannot pass validation and then silently overwrite
// the same cached price.
func toPricingModelEntry(pattern string) modelEntry {
	// Strip the wildcard before normalization, matching expandPricingToCache.
	isWild := strings.HasSuffix(pattern, "*")
	prefix := pattern
	if isWild {
		prefix = strings.TrimSuffix(pattern, "*")
	}
	return modelEntry{
		pattern:  pattern,
		prefix:   normalizePriceModelName(prefix),
		wildcard: isWild,
	}
}

// validateNoConflictingModels rejects duplicate names and overlapping ranges across the price list.
func validateNoConflictingModels(pricingList []ModelPricingEntry) error {
	var entries []modelEntry
	for _, price := range pricingList {
		for _, model := range price.Models {
			entries = append(entries, toPricingModelEntry(model))
		}
	}
	return detectConflicts(entries, "MODEL_PATTERN_CONFLICT", "model patterns")
}

func validateNoConflictingMappings(mapping map[string]string) error {
	entries := make([]modelEntry, 0, len(mapping))
	for source := range mapping {
		entries = append(entries, toModelEntry(source))
	}
	return detectConflicts(entries, "MAPPING_PATTERN_CONFLICT", "mapping source patterns")
}

func validatePricingIntervals(pricingList []ModelPricingEntry) error {
	for _, pricing := range pricingList {
		if err := ValidateIntervals(pricing.Intervals, pricing.BillingMode); err != nil {
			return infraerrors.BadRequest(
				"INVALID_PRICING_INTERVALS",
				fmt.Sprintf("invalid pricing intervals for models %v: %v", pricing.Models, err),
			)
		}
	}
	return nil
}

// detectConflicts returns a labeled error for the first overlapping pair.
func detectConflicts(entries []modelEntry, errCode, label string) error {
	for i := range entries {
		for j := i + 1; j < len(entries); j++ {
			if conflictsBetween(entries[i], entries[j]) {
				return infraerrors.BadRequest(errCode,
					fmt.Sprintf("%s '%s' and '%s' conflict: overlapping match range "+
						"(model names are matched case-insensitively, so an existing entry already covers all case variants)",
						label, entries[i].pattern, entries[j].pattern))
			}
		}
	}
	return nil
}

// BuildModelMappingChain joins distinct nonempty models in first-occurrence order; fewer than two yields an empty
// string.
func BuildModelMappingChain(models ...string) string {
	stages := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		stages = append(stages, model)
	}
	if len(stages) < 2 {
		return ""
	}
	return strings.Join(stages, "→")
}

func (v PricingConfigValidation) TimePricing(config *TimePricingConfig) error {
	if config == nil || len(config.Periods) == 0 {
		return nil
	}
	if err := pricing.ValidateTimezoneName(config.Timezone); err != nil {
		return fmt.Errorf("timezone: %w", err)
	}
	if v.LoadLocation == nil {
		return fmt.Errorf("timezone: pricing timezone loader is not configured")
	}
	if _, err := v.LoadLocation(config.Timezone); err != nil {
		return fmt.Errorf("timezone: %w", err)
	}
	return pricing.ValidateTimePricingConfig(config)
}

func (s *PricingConfigService) warn(message string, args ...any) {
	if s.options.Warn != nil {
		s.options.Warn(message, args...)
	}
}

// ResolveRoutingModel applies group mapping, retaining the request model for missing inputs or an empty result.
// Pass an already resolved model directly to its consumer to avoid mapping it again.
func (s *PricingConfigService) ResolveRoutingModel(ctx context.Context, groupID *int64, requestedModel string) string {
	if s == nil || groupID == nil || strings.TrimSpace(requestedModel) == "" {
		return requestedModel
	}
	mapping := s.ResolveGroupMapping(ctx, *groupID, requestedModel)
	if mapped := strings.TrimSpace(mapping.MappedModel); mapped != "" {
		return mapped
	}
	return requestedModel
}

// GetEffectiveBillingSettings returns cloned configuration settings or defaults when loading fails or no
// configuration exists.
func (s *PricingConfigService) GetEffectiveBillingSettings(ctx context.Context, groupID int64) pricing.BillingSettings {
	config, err := s.GetPricingConfigForGroup(ctx, groupID)
	if err != nil || config == nil {
		return pricing.DefaultBillingSettings()
	}
	return config.BillingSettings.Clone()
}

// BillingModelForPrice selects the configured model stage; requestedModel must be the name after key redirection.
func BillingModelForPrice(mapping GroupMappingResult, requestedModel, groupMappedModel, upstreamModel string) string {
	switch mapping.BillingModelSource {
	case BillingModelSourceRequested:
		return strings.TrimSpace(requestedModel)
	case BillingModelSourceUpstream:
		return strings.TrimSpace(upstreamModel)
	case BillingModelSourceGroupMapped:
		return strings.TrimSpace(groupMappedModel)
	default:
		return strings.TrimSpace(groupMappedModel)
	}
}
